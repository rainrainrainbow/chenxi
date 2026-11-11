package cx

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

type Cron struct {
	jobs  []CronJob
	app   *App
	mu    sync.Mutex
	fired map[string]bool
}

func NewCron(jobs []CronJob, app *App) *Cron {
	return &Cron{jobs: jobs, app: app, fired: make(map[string]bool)}
}

func (c *Cron) Start(ctx context.Context) {
	c.check(time.Now())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.check(now)
		}
	}
}

func (c *Cron) check(now time.Time) {
	day := now.Format("2006-01-02")
	for _, j := range c.jobs {
		if !matchSpec(j.Spec, now) {
			continue
		}
		mark := day + "|" + j.ID + "|" + now.Format("15:04")
		c.mu.Lock()
		if len(c.fired) > 256 {
			c.fired = make(map[string]bool)
		}
		if c.fired[mark] {
			c.mu.Unlock()
			continue
		}
		c.fired[mark] = true
		c.mu.Unlock()

		go func(job CronJob) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			reply, err := c.app.Handle(ctx, "[定时任务 "+job.ID+"] "+job.Text)
			if err != nil {
				log.Printf("cron %s failed: %v", job.ID, err)
				return
			}
			log.Printf("cron %s -> %s", job.ID, truncate(reply, 200))
			_ = c.app.store.Append("cron", job.ID+": "+reply, map[string]string{"target": job.Target})
			if job.Target == "notify" && c.app.channels != nil {
				if err := c.app.channels.Notify(ctx, "晨曦 · "+job.ID, reply); err != nil {
					log.Printf("cron %s 通知推送失败：%v", job.ID, err)
				}
			}
		}(j)
	}
}

// matchSpec 支持 "daily HH:MM" 与 "every Nm/Nh"，不引入 cron 库。
func matchSpec(spec string, now time.Time) bool {
	parts := strings.Fields(spec)
	if len(parts) != 2 {
		return false
	}
	switch parts[0] {
	case "daily":
		return now.Format("15:04") == parts[1]
	case "every":
		d, err := time.ParseDuration(parts[1])
		if err != nil || d < time.Minute {
			return false
		}
		return now.Unix()%int64(d.Seconds()) < 60
	}
	return false
}
