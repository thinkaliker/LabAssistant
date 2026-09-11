package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	cron "github.com/robfig/cron/v3"
)

// List returns a snapshot of all tasks.
func (s *Scheduler) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, *t)
	}
	return out
}

// Get returns one task.
func (s *Scheduler) Get(id string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, false
	}
	return *t, true
}

// Create validates and stores a new task.
func (s *Scheduler) Create(t Task) (Task, error) {
	if _, err := cron.ParseStandard(t.Schedule); err != nil {
		return Task{}, fmt.Errorf("invalid schedule: %w", err)
	}
	if t.Misfire == "" {
		t.Misfire = MisfireSkip
	}
	t.ID = uuid.NewString()
	t.CreatedAt = time.Now()
	t.LastRun = time.Time{}

	s.mu.Lock()
	if n, err := s.next(&t, time.Now()); err == nil {
		t.NextRun = n
	}
	stored := t
	s.tasks[t.ID] = &stored
	s.save()
	s.mu.Unlock()
	s.notify()
	return stored, nil
}

// Update replaces a task's mutable fields.
func (s *Scheduler) Update(id string, in Task) (Task, error) {
	if _, err := cron.ParseStandard(in.Schedule); err != nil {
		return Task{}, fmt.Errorf("invalid schedule: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task not found")
	}
	t.Name = in.Name
	t.Schedule = in.Schedule
	t.Timezone = in.Timezone
	t.HostIDs = in.HostIDs
	t.Module = in.Module
	t.Action = in.Action
	t.Params = in.Params
	t.InterHostDelaySeconds = in.InterHostDelaySeconds
	t.Misfire = in.Misfire
	if t.Misfire == "" {
		t.Misfire = MisfireSkip
	}
	t.AllowDestructive = in.AllowDestructive
	t.Enabled = in.Enabled
	if n, err := s.next(t, time.Now()); err == nil {
		t.NextRun = n
	}
	s.save()
	result := *t
	go s.notify()
	return result, nil
}

// Delete removes a task.
func (s *Scheduler) Delete(id string) bool {
	s.mu.Lock()
	_, ok := s.tasks[id]
	if ok {
		delete(s.tasks, id)
		s.save()
	}
	s.mu.Unlock()
	if ok {
		s.notify()
	}
	return ok
}

// PruneHosts drops every host id that keep rejects from each task's targets. A task left with
// no hosts is disabled rather than deleted, so it can be re-targeted. Returns the changed tasks
// as they now stand.
func (s *Scheduler) PruneHosts(keep func(hostID string) bool) []Task {
	s.mu.Lock()
	var changed []Task
	for _, t := range s.tasks {
		kept := make([]string, 0, len(t.HostIDs))
		for _, h := range t.HostIDs {
			if keep(h) {
				kept = append(kept, h)
			}
		}
		if len(kept) == len(t.HostIDs) {
			continue
		}
		t.HostIDs = kept
		if len(kept) == 0 {
			t.Enabled = false
		}
		changed = append(changed, *t)
	}
	if len(changed) > 0 {
		s.save()
	}
	s.mu.Unlock()
	if len(changed) > 0 {
		s.notify()
	}
	return changed
}

// save writes tasks to disk atomically. Caller holds the lock.
func (s *Scheduler) save() {
	tasks := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	b, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}
