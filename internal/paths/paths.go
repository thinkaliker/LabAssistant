// Package paths resolves LabAssistant's on-disk layout: a single base directory
// containing config/, data/, and logs/ subdirectories.
//
// The base directory is resolved in priority order:
//  1. an explicit override (e.g. the --home flag),
//  2. the LABASSISTANT_HOME environment variable,
//  3. a platform default (/var/lib/labassistant on Linux, else <user config dir>/labassistant).
//
// Keeping config/data/logs under one base makes a custom config trivial to volume-mount.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const envHome = "LABASSISTANT_HOME"

// Layout holds the resolved base directory and its subdirectories.
type Layout struct {
	Base   string
	Config string // <base>/config
	Data   string // <base>/data
	Logs   string // <base>/logs
}

// Resolve determines the base directory. A non-empty override wins; otherwise
// LABASSISTANT_HOME, then a platform default.
func Resolve(override string) (Layout, error) {
	base := override
	if base == "" {
		base = os.Getenv(envHome)
	}
	if base == "" {
		base = defaultBase()
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve home %q: %w", base, err)
	}
	return Layout{
		Base:   abs,
		Config: filepath.Join(abs, "config"),
		Data:   filepath.Join(abs, "data"),
		Logs:   filepath.Join(abs, "logs"),
	}, nil
}

// EnsureDirs creates the base, config, data, and logs directories if they do not exist.
// data is created with 0700 since it holds the CA key and issued certificates.
func (l Layout) EnsureDirs() error {
	for _, d := range []string{l.Base, l.Config, l.Logs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := os.MkdirAll(l.Data, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", l.Data, err)
	}
	return nil
}

// ConfigFile returns the path to the manager's config file.
func (l Layout) ConfigFile() string { return filepath.Join(l.Config, "config.toml") }

// CertsDir returns the directory holding the CA and issued certificates (under data/).
func (l Layout) CertsDir() string { return filepath.Join(l.Data, "certs") }

// StateFile returns the path to the manager's JSON state file.
func (l Layout) StateFile() string { return filepath.Join(l.Data, "state.json") }

// TasksFile returns the path to the scheduler's persisted tasks.
func (l Layout) TasksFile() string { return filepath.Join(l.Data, "tasks.json") }

func defaultBase() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/labassistant"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "labassistant")
	}
	return filepath.Join(os.TempDir(), "labassistant")
}
