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
	"os"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"
)

const (
	tickInterval  = 20 * time.Second
	missedGrace   = 90 * time.Second
	retryInterval = 60 * time.Second
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
}

// DispatchFunc runs an action on a host (pre-approved). ConnectedFunc reports liveness.
type DispatchFunc func(hostID, module, action string, params json.RawMessage) error
type ConnectedFunc func(hostID string) bool

// Scheduler holds tasks and runs the cron loop.
type Scheduler struct {
	path      string
	loc       *time.Location
	dispatch  DispatchFunc
	connected ConnectedFunc
	notify    func()

	mu    sync.Mutex
	tasks map[string]*Task
}

// Load reads tasks from path and prepares the scheduler.
func Load(path string, dispatch DispatchFunc, connected ConnectedFunc, notify func()) (*Scheduler, error) {
	s := &Scheduler{
		path: path, loc: time.Local, dispatch: dispatch, connected: connected,
		notify: notify, tasks: map[string]*Task{},
	}
	if notify == nil {
		s.notify = func() {}
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
	for i, hostID := range t.HostIDs {
		if i > 0 && delay > 0 {
			time.Sleep(delay)
		}
		_ = s.dispatch(hostID, t.Module, t.Action, t.Params)
	}
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
