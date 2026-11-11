package cx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

type App struct {
	cfg      *Config
	store    *Store
	reg      *Registry
	prov     *Provider
	host     *HostClient
	skills   *SkillBook
	cron     *Cron
	channels *ChannelManager
	system   string
	mu       sync.Mutex // 单一写者：所有入站串行进入 Agent Loop
	sem      chan struct{}
	started  time.Time
}

func New(cfg *Config) (*App, error) {
	soul, err := os.ReadFile(cfg.SoulFile)
	if err != nil {
		return nil, fmt.Errorf("读取 SOUL.md: %w", err)
	}
	constitution, err := os.ReadFile(cfg.AgentsFile)
	if err != nil {
		return nil, fmt.Errorf("读取 AGENTS.md: %w", err)
	}
	store, err := NewStore(cfg)
	if err != nil {
		return nil, err
	}
	reg, err := NewRegistry(cfg.RegistryFile)
	if err != nil {
		return nil, err
	}
	prov, err := NewProvider(cfg.Provider)
	if err != nil {
		return nil, err
	}
	book, err := NewSkillBook(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg:     cfg,
		store:   store,
		reg:     reg,
		prov:    prov,
		host:    NewHostClient(cfg),
		skills:  book,
		system:  BuildSystem(string(soul), string(constitution)),
		sem:     make(chan struct{}, cfg.Budgets.MaxConcurrency),
		started: time.Now(),
	}
	a.cron = NewCron(cfg.Cron, a)
	channels, err := NewChannelManager(a)
	if err != nil {
		return nil, fmt.Errorf("渠道装配失败：%w", err)
	}
	a.channels = channels
	return a, nil
}

func (a *App) StartCron(ctx context.Context) {
	if len(a.cfg.Cron) > 0 {
		go a.cron.Start(ctx)
	}
}

func (a *App) Serve(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", a.handleHealth)
	mux.HandleFunc("/v1/status", a.handleStatus)
	mux.HandleFunc("/v1/message", a.handleMessage)
	mux.HandleFunc("/v1/approve", a.handleApprove)
	if a.channels != nil {
		a.channels.ServeRoutes(mux)
	}
	srv := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		MaxHeaderBytes:    4096,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"uptime_s": int64(time.Since(a.started).Seconds()),
	})
}

func (a *App) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(a.cfg.Budgets.MaxMessageBytes*2))
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body: " + err.Error()})
		return
	}
	reply, err := a.Handle(r.Context(), in.Text)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

func (a *App) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
		return
	}
	res, err := a.host.Call(r.Context(), "approve", "", in.Token, "readonly", "allow")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": res.OK, "data": res.Data, "error": res.Error})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	stats := a.store.Stats()
	hostOK := a.host.Ping(r.Context()) == nil
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_s":      int64(time.Since(a.started).Seconds()),
		"heap_alloc_kb": m.Alloc / 1024,
		"heap_sys_kb":   m.Sys / 1024,
		"goroutines":    runtime.NumGoroutine(),
		"agents":        len(a.reg.All()),
		"skills":        a.skills.Count(),
		"events":        stats["events"],
		"triples":       stats["triples"],
		"index_terms":   stats["index_terms"],
		"log_kb":        stats["log_kb"],
		"hostd_ok":      hostOK,
		"concurrency":   cap(a.sem),
		"local_llm":     false,
		"channels":      a.channelNames(),
		"channel_queue": a.channelQueueLen(),
	})
}

func (a *App) channelNames() []string {
	if a.channels == nil {
		return []string{}
	}
	return a.channels.Enabled()
}

func (a *App) channelQueueLen() int {
	if a.channels == nil {
		return 0
	}
	return a.channels.QueueLen()
}

func (a *App) RunCLI(ctx context.Context) {
	fmt.Println("晨曦 · 1 GB 小盒子上的 AI 管家（输入 exit 退出）")
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4096), a.cfg.Budgets.MaxMessageBytes*2)
	for {
		fmt.Print("> ")
		if !sc.Scan() {
			return
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return
		}
		reply, err := a.Handle(ctx, line)
		if err != nil {
			log.Printf("错误：%v", err)
			continue
		}
		fmt.Println(reply)
	}
}
