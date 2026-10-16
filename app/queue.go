package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Queue decisions share Engine.mu with task state, so priority changes and
// cancellation cannot race a slot reservation. Running jobs are never preempted.
type DownloadPlan struct {
	Priority    int       `json:"priority"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

func validateDownloadPlan(plan DownloadPlan) error {
	if plan.Priority < -1 || plan.Priority > 1 {
		return errors.New("下载优先级无效")
	}
	if !plan.ScheduledAt.IsZero() && plan.ScheduledAt.After(time.Now().AddDate(1, 0, 0)) {
		return errors.New("预约时间请设置在一年以内")
	}
	return nil
}

func queueBefore(a, b *Job) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func (e *Engine) wakeQueueLocked() {
	if e.queueWake != nil {
		close(e.queueWake)
	}
	e.queueWake = make(chan struct{})
}

func (e *Engine) awaitDownloadSlot(ctx context.Context, j *Job) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		e.mu.Lock()
		if err := ctx.Err(); err != nil {
			e.mu.Unlock()
			return err
		}
		now := time.Now()
		var first *Job
		if !e.config.QueuePaused && e.runningDownloads < e.config.Concurrent {
			for _, other := range e.jobs {
				if other.Status == "queued" && other.cancel != nil && !other.pauseRequested && !other.ScheduledAt.After(now) && (first == nil || queueBefore(other, first)) {
					first = other
				}
			}
		}
		if first == j {
			e.runningDownloads++
			j.Status, j.started = "downloading", now
			j.speedSampleAt = now
			j.limiter = &downloadLimiter{bytesPerSecond: int64(e.config.SpeedLimitKB) * 1024}
			e.wakeQueueLocked()
			e.mu.Unlock()
			return nil
		}
		if e.queueWake == nil {
			e.queueWake = make(chan struct{})
		}
		wake := e.queueWake
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-tick.C:
		}
	}
}

func (e *Engine) changeJobPlan(id string, plan DownloadPlan) error {
	if err := validateDownloadPlan(plan); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.jobs[id]
	if j == nil || j.Status != "queued" && j.Status != "paused" {
		return errors.New("只能调整排队或暂停任务；进行中的任务请先暂停")
	}
	j.Priority, j.ScheduledAt = plan.Priority, plan.ScheduledAt
	e.wakeQueueLocked()
	return nil
}

func (e *Engine) setQueuePaused(paused bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg := e.config
	cfg.QueuePaused = paused
	if err := e.persistConfig(cfg); err != nil {
		return err
	}
	e.config = cfg
	e.wakeQueueLocked()
	return nil
}

// One limiter is shared by every HTTP segment of a job. Waiting is cancellable
// even at very low rates; pausing never waits for an entire buffer to drain.
type downloadLimiter struct {
	mu             sync.Mutex
	bytesPerSecond int64
	next           time.Time
}

func (l *downloadLimiter) wait(ctx context.Context, bytes int) error {
	if l == nil || l.bytesPerSecond <= 0 || bytes <= 0 {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(time.Duration(float64(bytes) / float64(l.bytesPerSecond) * float64(time.Second)))
	delay := l.next.Sub(now)
	l.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
