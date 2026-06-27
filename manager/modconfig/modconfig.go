// Package modconfig persists per-host, per-module configuration (the "change settings"
// surface). It is kept separate from state.json because it may hold secrets.
package modconfig

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
)

// Store holds host -> module -> raw JSON config.
type Store struct {
	mu   sync.RWMutex
	path string
	m    map[string]map[string]json.RawMessage
}

// Load reads config from path (missing file yields an empty store).
func Load(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

// Get returns the stored config for a host's module (or nil).
func (s *Store) Get(hostID, moduleName string) json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if mods, ok := s.m[hostID]; ok {
		return mods[moduleName]
	}
	return nil
}

// Set stores config for a host's module and persists.
func (s *Store) Set(hostID, moduleName string, cfg json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[hostID] == nil {
		s.m[hostID] = map[string]json.RawMessage{}
	}
	s.m[hostID][moduleName] = cfg
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
