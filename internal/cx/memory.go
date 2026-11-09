package cx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Event struct {
	ID   int64             `json:"id"`
	TS   int64             `json:"ts"`
	Kind string            `json:"kind"`
	Text string            `json:"text"`
	Meta map[string]string `json:"meta,omitempty"`
}

// Triple 是璇玑式「实体-关系-事件」的最小表示。
type Triple struct {
	Subject  string `json:"s"`
	Relation string `json:"r"`
	Object   string `json:"o"`
}

type scored struct {
	idx   int
	score float64
}

type snapshot struct {
	NextID  int64    `json:"next_id"`
	Triples []Triple `json:"triples"`
}

// Store 只有一个写者（Gateway 的 mu），索引与快照都是它的派生视图。
type Store struct {
	mu        sync.Mutex
	logPath   string
	snapPath  string
	events    []Event
	triples   []Triple
	index     map[string][]int
	maxEvents int
	maxPosts  int
	maxLog    int64
	logBytes  int64
	nextID    int64
}

func NewStore(c *Config) (*Store, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	s := &Store{
		logPath:   filepath.Join(c.DataDir, "events.jsonl"),
		snapPath:  filepath.Join(c.DataDir, "snapshot.json"),
		index:     make(map[string][]int),
		maxEvents: 4096,
		maxPosts:  c.Budgets.MaxIndexEntries,
		maxLog:    c.Budgets.MaxLogBytes,
	}
	_ = s.loadSnapshot()
	if err := s.replayLog(); err != nil {
		return nil, err
	}
	s.rebuildIndex()
	return s, nil
}

func (s *Store) loadSnapshot() error {
	b, err := os.ReadFile(s.snapPath)
	if err != nil {
		return err
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return err
	}
	s.triples = snap.Triples
	s.nextID = snap.NextID
	return nil
}

func (s *Store) replayLog() error {
	f, err := os.Open(s.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		s.events = append(s.events, e)
		if e.ID >= s.nextID {
			s.nextID = e.ID + 1
		}
		s.logBytes += int64(len(line)) + 1
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(s.events) > s.maxEvents {
		s.events = append([]Event(nil), s.events[len(s.events)-s.maxEvents:]...)
	}
	return nil
}

// tokenize：英文按词、中文按单字 + 相邻 bigram。纯标准库，替代 FTS5。
func tokenize(text string) []string {
	runes := []rune(strings.ToLower(text))
	out := make([]string, 0, len(runes)/2+1)
	var word strings.Builder
	flush := func() {
		if word.Len() > 1 {
			out = append(out, word.String())
		}
		word.Reset()
	}
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			flush()
			out = append(out, string(r))
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	for i := 0; i+1 < len(runes); i++ {
		if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
			out = append(out, string(runes[i:i+2]))
		}
	}
	return out
}

func (s *Store) indexEvent(i int) {
	for _, t := range tokenize(s.events[i].Text) {
		posts := s.index[t]
		if len(posts) >= s.maxPosts {
			posts = posts[1:]
		}
		s.index[t] = append(posts, i)
	}
}

func (s *Store) rebuildIndex() {
	s.index = make(map[string][]int)
	for i := range s.events {
		s.indexEvent(i)
	}
}

func (s *Store) Append(kind, text string, meta map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := Event{
		ID:   s.nextID,
		TS:   time.Now().Unix(),
		Kind: kind,
		Text: text,
		Meta: meta,
	}
	s.nextID++
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	s.logBytes += int64(len(line))
	s.events = append(s.events, e)
	if len(s.events) > s.maxEvents {
		s.events = append([]Event(nil), s.events[len(s.events)-s.maxEvents:]...)
		s.rebuildIndex()
	} else {
		s.indexEvent(len(s.events) - 1)
	}
	if s.logBytes > s.maxLog {
		return s.compactLocked()
	}
	return nil
}

// compactLocked 把旧事件压成一条摘要后截断日志。历史只允许追加，
// 压缩是唯一被允许的重写，且只在磁盘日志超限时发生一次。
func (s *Store) compactLocked() error {
	// 只保留最近的非摘要事件，避免每轮压缩再叠加一条 summary 造成缓慢膨胀。
	keep := make([]Event, 0, 256)
	for _, e := range s.events {
		if e.Kind != "summary" {
			keep = append(keep, e)
		}
	}
	if len(keep) > 256 {
		keep = keep[len(keep)-256:]
	}
	summary := Event{
		ID:   s.nextID,
		TS:   time.Now().Unix(),
		Kind: "summary",
		Text: fmt.Sprintf("已压缩 %d 条历史事件，保留最近 %d 条；此条之前的细节不可恢复。", len(s.events), len(keep)),
	}
	s.nextID++
	lines := make([]byte, 0, 64*1024)
	b, _ := json.Marshal(summary)
	lines = append(lines, b...)
	lines = append(lines, '\n')
	for _, e := range keep {
		b, _ := json.Marshal(e)
		lines = append(lines, b...)
		lines = append(lines, '\n')
	}
	tmp := s.logPath + ".tmp"
	if err := os.WriteFile(tmp, lines, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.logPath); err != nil {
		return err
	}
	s.events = append([]Event{summary}, keep...)
	s.logBytes = int64(len(lines))
	s.rebuildIndex()
	return s.Snapshot()
}

func (s *Store) Recent(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.events) {
		n = len(s.events)
	}
	out := make([]Event, n)
	copy(out, s.events[len(s.events)-n:])
	return out
}

func (s *Store) Search(query string, k int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	terms := make(map[string]bool)
	for _, t := range tokenize(query) {
		terms[t] = true
	}
	scores := make(map[int]float64)
	for t := range terms {
		for _, i := range s.index[t] {
			scores[i]++
		}
	}
	hits := make([]scored, 0, len(scores))
	for i, sc := range scores {
		hits = append(hits, scored{i, sc})
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return s.events[hits[a].idx].TS > s.events[hits[b].idx].TS
	})
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]Event, 0, k)
	for _, h := range hits[:k] {
		out = append(out, s.events[h.idx])
	}
	return out
}

func (s *Store) AddTriple(sub, rel, obj string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.triples) >= s.maxPosts {
		s.triples = s.triples[1:]
	}
	s.triples = append(s.triples, Triple{sub, rel, obj})
	_ = s.Snapshot()
}

func (s *Store) Corrections() []Triple {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Triple, 0, 8)
	for _, t := range s.triples {
		if t.Relation == "correction" {
			out = append(out, t)
		}
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return out
}

func (s *Store) Snapshot() error {
	snap := snapshot{NextID: s.nextID, Triples: s.triples}
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := s.snapPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.snapPath)
}

func (s *Store) Stats() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]int64{
		"events":      int64(len(s.events)),
		"triples":     int64(len(s.triples)),
		"index_terms": int64(len(s.index)),
		"log_kb":      s.logBytes / 1024,
	}
}
