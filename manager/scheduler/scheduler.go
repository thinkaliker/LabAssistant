// Package scheduler runs tasks on a cron schedule: it dispatches a module action to one
// or more hosts, persists each task's fire state, and handles missed runs (skip/catch-up)
// and offline targets (retry). Destructive tasks must opt in at creation; the scheduler
// dispatches them as pre-approved.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"
)

const (
	tickInterval  = 20 * time.Second
	missedGrace   = 90 * time.Second
	retryInterval = 60 * time.Second
	// catchupWindow bounds catch-up: a job overdue beyond this (manager down a long time)
	// is skipped to its next slot instead of fired late.
	catchupWindow = 6 * time.Hour
	// maxConcurrent caps fleet-wide simultaneous dispatches across all firing tasks.
	maxConcurrent = 4
	// maxJitter staggers each host dispatch to avoid a thundering herd.
	maxJitter = 5 * time.Second
)

// Misfire policies for a run missed because the manager was down or a host was offline.
const (
	MisfireSkip    = "skip"
	MisfireCatchup = "catchup"
	MisfireRetry   = "retry"
)

// Task is a scheduled action.
type Task struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Schedule              string          `json:"schedule"` // 5-field cron
	Timezone              string          `json:"timezone"` // IANA name; empty = manager local
	HostIDs               []string        `json:"hostIds"`
	Module                string          `json:"module"`
	Action                string          `json:"action"`
	Params                json.RawMessage `json:"params,omitempty"`
	InterHostDelaySeconds int             `json:"interHostDelaySeconds"`
	Misfire               string          `json:"misfire"`
	AllowDestructive      bool            `json:"allowDestructive"`
	Enabled               bool            `json:"enabled"`
	CreatedAt             time.Time       `json:"createdAt"`
	LastRun               time.Time       `json:"lastRun,omitempty"`
	NextRun               time.Time       `json:"nextRun,omitempty"`
	LastStatus            string          `json:"lastStatus,omitempty"` // dispatched | failed | skipped
	LastError             string          `json:"lastError,omitempty"`
}

// DispatchFunc runs an action on a host (pre-approved). ConnectedFunc reports liveness.
type DispatchFunc func(hostID, module, action string, params json.RawMessage) error
type ConnectedFunc func(hostID string) bool

// ReportFunc surfaces a per-host run outcome (err != nil = failure) for audit/notification.
type ReportFunc func(t Task, hostID string, err error)

// Scheduler holds tasks and runs the cron loop.
type Scheduler struct {
	path      string
	loc       *time.Location
	dispatch  DispatchFunc
	connected ConnectedFunc
	notify    func()
	report    ReportFunc
	sem       chan struct{} // fleet-wide dispatch concurrency cap

	mu    sync.Mutex
	tasks map[string]*Task
}

// Load reads tasks from path and prepares the scheduler.
func Load(path string, dispatch DispatchFunc, connected ConnectedFunc, notify func(), report ReportFunc) (*Scheduler, error) {
	s := &Scheduler{
		path: path, loc: time.Local, dispatch: dispatch, connected: connected,
		notify: notify, report: report, tasks: map[string]*Task{},
		sem: make(chan struct{}, maxConcurrent),
	}
	if notify == nil {
		s.notify = func() {}
	}
	if report == nil {
		s.report = func(Task, string, error) {}
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tasks %s: %w", path, err)
	}
	var tasks []*Task
	if err := json.Unmarshal(b, &tasks); err != nil {
		return nil, fmt.Errorf("parse tasks %s: %w", path, err)
	}
	for _, t := range tasks {
		s.tasks[t.ID] = t
	}
	return s, nil
}

// Start runs the cron loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(time.Now())
		}
	}
}

func (s *Scheduler) tick(now time.Time) {
	s.mu.Lock()
	var due []Task
	for _, t := range s.tasks {
		if !t.Enabled {
			continue
		}
		if t.NextRun.IsZero() {
			if next, err := s.next(t, now); err == nil {
				t.NextRun = next
			}
			continue
		}
		if now.Before(t.NextRun) {
			continue
		}
		late := now.Sub(t.NextRun)
		// Offline targets with retry policy: re-arm soon, don't fire yet.
		if t.Misfire == MisfireRetry && !s.allConnected(t) {
			t.NextRun = now.Add(retryInterval)
			continue
		}
		// Missed run while down, skip policy: advance without firing.
		if late > missedGrace && t.Misfire == MisfireSkip {
			t.NextRun = s.nextOr(t, now)
			t.LastStatus = "skipped"
			continue
		}
		// Catch-up bounded: a job overdue beyond the window (extended downtime) is skipped
		// to its next slot rather than fired stale.
		if late > catchupWindow && t.Misfire == MisfireCatchup {
			t.NextRun = s.nextOr(t, now)
			t.LastStatus = "skipped"
			continue
		}
		t.LastRun = now
		t.NextRun = s.nextOr(t, now)
		due = append(due, *t)
	}
	s.save()
	s.mu.Unlock()
	s.notify()

	for i := range due {
		go s.fire(due[i])
	}
}

func (s *Scheduler) fire(t Task) {
	delay := time.Duration(t.InterHostDelaySeconds) * time.Second
	failed := false
	for i, hostID := range t.HostIDs {
		if i > 0 && delay > 0 {
			time.Sleep(delay)
		}
		if err := s.dispatchOne(t, hostID); err != nil {
			failed = true
		}
	}
	status := "dispatched"
	if failed {
		status = "failed"
	}
	s.mu.Lock()
	if cur, ok := s.tasks[t.ID]; ok && cur.LastStatus != "failed" {
		// preserve a per-host failure already recorded; otherwise reflect the aggregate
		cur.LastStatus = status
		s.save()
	}
	s.mu.Unlock()
	s.notify()
}

// dispatchOne runs one host through the global concurrency cap with jitter, then records and
// surfaces the outcome.
func (s *Scheduler) dispatchOne(t Task, hostID string) error {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	if maxJitter > 0 {
		time.Sleep(time.Duration(rand.Int64N(int64(maxJitter))))
	}
	err := s.dispatch(hostID, t.Module, t.Action, t.Params)

	s.mu.Lock()
	if cur, ok := s.tasks[t.ID]; ok {
		if err != nil {
			cur.LastStatus = "failed"
			cur.LastError = err.Error()
		} else {
			cur.LastError = ""
		}
		s.save()
	}
	s.mu.Unlock()

	s.report(t, hostID, err)
	return err
}

func (s *Scheduler) allConnected(t *Task) bool {
	if s.connected == nil {
		return true
	}
	for _, h := range t.HostIDs {
		if !s.connected(h) {
			return false
		}
	}
	return true
}

func (s *Scheduler) next(t *Task, after time.Time) (time.Time, error) {
	sched, err := cron.ParseStandard(t.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	loc := s.loc
	if t.Timezone != "" {
		if l, err := time.LoadLocation(t.Timezone); err == nil {
			loc = l
		}
	}
	return sched.Next(after.In(loc)), nil
}

func (s *Scheduler) nextOr(t *Task, after time.Time) time.Time {
	if n, err := s.next(t, after); err == nil {
		return n
	}
	return time.Time{}
}
