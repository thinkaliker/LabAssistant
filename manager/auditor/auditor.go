// Package auditor records an append-only, hash-chained audit log of control-plane and
// security events (host add/remove, cert issuance, approvals, dispatched actions, job
// results). Entries are tamper-evident: each carries the hash of the previous entry.
//
// Slice 5 uses a JSONL file backend with a configurable in-memory/queryable retention.
// TODO(storage): pluggable SQLite/external-DB backends; the README also calls for a
// dedicated DB user when SQLite is used.
package auditor

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry is one audit record.
type Entry struct {
	ID       string          `json:"id"`
	Time     time.Time       `json:"time"`
	Type     string          `json:"type"`
	HostID   string          `json:"hostId,omitempty"`
	Actor    string          `json:"actor"`
	Summary  string          `json:"summary"`
	Details  json.RawMessage `json:"details,omitempty"`
	PrevHash string          `json:"prevHash"`
	Hash     string          `json:"hash"`
}

// Auditor appends and serves audit entries.
type Auditor struct {
	mu       sync.Mutex
	path     string
	max      int
	entries  []Entry // most recent, capped at max
	prevHash string
	notify   func(Entry)
}

// Open loads the audit log at path, keeping the last max entries in memory.
func Open(path string, max int, notify func(Entry)) (*Auditor, error) {
	if max <= 0 {
		max = 1000
	}
	a := &Auditor{path: path, max: max, notify: notify}
	if a.notify == nil {
		a.notify = func(Entry) {}
	}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

// Record appends an event. details may be nil; it is JSON-encoded if non-nil.
func (a *Auditor) Record(typ, hostID, actor, summary string, details any) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e := Entry{
		ID: uuid.NewString(), Time: time.Now().UTC(), Type: typ,
		HostID: hostID, Actor: actor, Summary: summary, PrevHash: a.prevHash,
	}
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			e.Details = b
		}
	}
	e.Hash = hashEntry(e)
	a.prevHash = e.Hash

	a.entries = append(a.entries, e)
	if len(a.entries) > a.max {
		a.entries = a.entries[len(a.entries)-a.max:]
	}
	a.appendLine(e)
	a.notify(e)
}

// List returns up to limit most-recent entries (newest first). limit <= 0 returns all kept.
func (a *Auditor) List(limit int) []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.entries)
	start := 0
	if limit > 0 && limit < n {
		start = n - limit
	}
	out := make([]Entry, 0, n-start)
	for i := n - 1; i >= start; i-- {
		out = append(out, a.entries[i])
	}
	return out
}

func hashEntry(e Entry) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *Auditor) appendLine(e Entry) {
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, _ = f.Write(append(b, '\n'))
}

// load reads the file, keeps the last max entries, compacts the file if it has grown well
// past the retention window.
func (a *Auditor) load() error {
	f, err := os.Open(a.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	var all []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	lines := 0
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read audit log: %w", err)
	}
	if len(all) > a.max {
		all = all[len(all)-a.max:]
	}
	a.entries = all
	if n := len(all); n > 0 {
		a.prevHash = all[n-1].Hash
	}
	if lines > 2*a.max {
		a.compact()
	}
	return nil
}

// compact rewrites the file with only the retained entries. Caller holds the lock.
func (a *Auditor) compact() {
	tmp := a.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	for _, e := range a.entries {
		b, _ := json.Marshal(e)
		if _, err := f.Write(append(b, '\n')); err != nil {
			f.Close()
			return
		}
	}
	f.Close()
	_ = os.Rename(tmp, a.path)
}
