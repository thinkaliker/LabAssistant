// Package config loads the manager's configuration from a TOML file, falling back
// to sensible defaults when the file or individual fields are absent.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the manager's runtime configuration.
type Config struct {
	// HTTPAddr is the listen address for the dashboard and REST API.
	HTTPAddr string `toml:"http_addr"`
	// GRPCAddr is the listen address for the associate mTLS stream.
	GRPCAddr string `toml:"grpc_addr"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `toml:"log_level"`

	Auth   Auth   `toml:"auth"`
	Enroll Enroll `toml:"enroll"`
}

// Enroll configures how the quartermaster installs associates on hosts.
type Enroll struct {
	// Mode is "local" (spawn the associate as a child process — dev) or "ssh".
	Mode string `toml:"mode"`
	// AssociateBin / HelperBin are paths to the binaries the installer deploys.
	AssociateBin string `toml:"associate_bin"`
	HelperBin    string `toml:"helper_bin"`
	// ManagerAddr / ServerName are baked into each bundle (what the associate dials).
	ManagerAddr string `toml:"manager_addr"`
	ServerName  string `toml:"server_name"`
}

// Auth holds dashboard login settings. The password is stored only as a hash.
type Auth struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{
		HTTPAddr: ":8080",
		GRPCAddr: ":8443",
		LogLevel: "info",
		Auth:     Auth{Username: "admin"},
		Enroll: Enroll{
			Mode:        "local",
			ManagerAddr: "localhost:8443",
			ServerName:  "localhost",
		},
	}
}

// Load reads the config file at path. A missing file yields Default(); present fields
// override the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
