// Package state holds the manager's host registry and persists the durable parts to a
// JSON file. Runtime fields (liveness, health, advertised modules, latest status) live
// only in memory and are repopulated when an associate connects.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/thinkaliker/labassistant/module"
)

// HostStatus is a host's liveness state.
type HostStatus string

const (
	StatusEnrolling HostStatus = "enrolling"
	StatusOnline    HostStatus = "online"
	StatusOffline   HostStatus = "offline"
	StatusError     HostStatus = "error"
)

// Host is one managed host. All fields are returned by the API; only the durable subset
// (see persisted) is written to state.json.
type Host struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Tailscale bool   `json:"tailscale"`
	SSHUser   string `json:"sshUser"`
	// Mode is how the associate was installed: "ssh" (remote host) or "local"
	// (child process on the manager box). Selected per host at enroll time.
	Mode string `json:"mode,omitempty"`
	// ConnMode is the stream direction: "dial_home" (associate dials the manager, the
	// default) or "manager_dial" (the manager dials the associate). Empty means dial_home
	// for hosts enrolled before per-host connection mode existed.
	ConnMode string `json:"connMode,omitempty"`
	// ConnPort is the TCP port the associate listens on and the manager dials in
	// manager_dial mode. Zero in dial_home mode.
	ConnPort   int       `json:"connPort,omitempty"`
	CertSerial string    `json:"certSerial,omitempty"`
	CertExpiry time.Time `json:"certExpiry,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`

	Status   HostStatus    `json:"status"`
	LastSeen time.Time     `json:"lastSeen,omitempty"`
	Health   *Health       `json:"health,omitempty"`
	Modules  []ModuleState `json:"modules,omitempty"`
}

// persisted is the durable subset of Host written to state.json (runtime fields excluded).
type persisted struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IP         string    `json:"ip"`
	Tailscale  bool      `json:"tailscale"`
	SSHUser    string    `json:"sshUser"`
	Mode       string    `json:"mode,omitempty"`
	ConnMode   string    `json:"connMode,omitempty"`
	ConnPort   int       `json:"connPort,omitempty"`
	CertSerial string    `json:"certSerial,omitempty"`
	CertExpiry time.Time `json:"certExpiry,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Health is the latest host vitals from a heartbeat.
type Health struct {
	CPUPercent    float64 `json:"cpuPercent"`
	MemPercent    float64 `json:"memPercent"`
	MemUsedBytes  uint64  `json:"memUsedBytes"`
	MemTotalBytes uint64  `json:"memTotalBytes"`
	UptimeSeconds uint64  `json:"uptimeSeconds"`
	Disks         []Disk  `json:"disks,omitempty"`
}

// Disk is per-mount usage.
type Disk struct {
	Mount      string `json:"mount"`
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

// ModuleState is a module advertised by a host, plus its latest status.
type ModuleState struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Description  string              `json:"description"`
	Actions      []module.ActionSpec `json:"actions"`
	Detection    module.Detection    `json:"detection"`
	ConfigSchema json.RawMessage     `json:"configSchema,omitempty"`
	Status       json.RawMessage     `json:"status,omitempty"`
	StatusAt     time.Time           `json:"statusAt,omitempty"`
}

// Change describes a registry mutation, broadcast to subscribers (e.g. the SSE hub).
type Change struct {
	Kind   string `json:"kind"` // host_added | host_updated | host_removed
	HostID string `json:"hostId"`
}

// Store is the in-memory host registry with JSON persistence.
type Store struct {
	mu     sync.RWMutex
	path   string
	hosts  map[string]*Host
	notify func(Change)
}

// Load reads the registry from path. A missing file yields an empty store. All hosts load
// as offline until an associate connects.
func Load(path string) (*Store, error) {
	s := &Store{path: path, hosts: map[string]*Host{}, notify: func(Change) {}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var ps []persisted
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	for _, p := range ps {
		s.hosts[p.ID] = &Host{
			ID: p.ID, Name: p.Name, IP: p.IP, Tailscale: p.Tailscale,
			SSHUser: p.SSHUser, Mode: p.Mode, ConnMode: p.ConnMode, ConnPort: p.ConnPort,
			CertSerial: p.CertSerial, CertExpiry: p.CertExpiry,
			CreatedAt: p.CreatedAt, Status: StatusOffline,
		}
	}
	return s, nil
}

// SetNotify registers a callback invoked after each mutation.
func (s *Store) SetNotify(fn func(Change)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = fn
}

// Hosts returns a snapshot copy of all hosts.
func (s *Store) Hosts() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, *h)
	}
	return out
}

// Get returns a copy of one host.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return Host{}, false
	}
	return *h, true
}

// Add inserts a new host and persists.
func (s *Store) Add(h Host) error {
	s.mu.Lock()
	if _, exists := s.hosts[h.ID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("host %s already exists", h.ID)
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now()
	}
	if h.Status == "" {
		h.Status = StatusOffline
	}
	hp := h
	s.hosts[h.ID] = &hp
	err := s.save()
	s.mu.Unlock()
	s.fire(Change{Kind: "host_added", HostID: h.ID})
	return err
}

// Remove deletes a host and persists.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	if _, ok := s.hosts[id]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.hosts, id)
	err := s.save()
	s.mu.Unlock()
	s.fire(Change{Kind: "host_removed", HostID: id})
	return err
}

// update mutates a host under lock, persists if persist is true, then fires host_updated.
func (s *Store) update(id string, persist bool, fn func(*Host)) {
	s.mu.Lock()
	h, ok := s.hosts[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	fn(h)
	if persist {
		_ = s.save()
	}
	s.mu.Unlock()
	s.fire(Change{Kind: "host_updated", HostID: id})
}

// SetOnline marks a host online and replaces its advertised modules (from Hello).
func (s *Store) SetOnline(id string, modules []ModuleState) {
	s.update(id, false, func(h *Host) {
		h.Status = StatusOnline
		h.LastSeen = time.Now()
		h.Modules = modules
	})
}

// SetOffline marks a host offline.
func (s *Store) SetOffline(id string) {
	s.update(id, false, func(h *Host) { h.Status = StatusOffline })
}

// SetStatus sets a host's status (e.g. enrolling, error).
func (s *Store) SetStatus(id string, st HostStatus) {
	s.update(id, false, func(h *Host) { h.Status = st })
}

// Edit mutates a host's durable fields, persists, and reports whether it existed.
func (s *Store) Edit(id string, fn func(*Host)) bool {
	s.mu.Lock()
	h, ok := s.hosts[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	fn(h)
	_ = s.save()
	s.mu.Unlock()
	s.fire(Change{Kind: "host_updated", HostID: id})
	return true
}

// SetHealth records the latest heartbeat vitals.
func (s *Store) SetHealth(id string, health Health) {
	s.update(id, false, func(h *Host) {
		h.Health = &health
		h.LastSeen = time.Now()
	})
}

// SetModuleStatus records the latest status payload for one module.
func (s *Store) SetModuleStatus(id, moduleName string, data json.RawMessage, at time.Time) {
	s.update(id, false, func(h *Host) {
		for i := range h.Modules {
			if h.Modules[i].Name == moduleName {
				h.Modules[i].Status = data
				h.Modules[i].StatusAt = at
				return
			}
		}
	})
}

// save writes the durable host list atomically. Caller holds the lock.
func (s *Store) save() error {
	hosts := make([]persisted, 0, len(s.hosts))
	for _, h := range s.hosts {
		hosts = append(hosts, persisted{
			ID: h.ID, Name: h.Name, IP: h.IP, Tailscale: h.Tailscale,
			SSHUser: h.SSHUser, Mode: h.Mode, ConnMode: h.ConnMode, ConnPort: h.ConnPort,
			CertSerial: h.CertSerial, CertExpiry: h.CertExpiry,
			CreatedAt: h.CreatedAt,
		})
	}
	b, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) fire(c Change) {
	s.mu.RLock()
	fn := s.notify
	s.mu.RUnlock()
	if fn != nil {
		fn(c)
	}
}
