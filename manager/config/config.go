// Package config loads the manager's configuration from a TOML file, falling back
// to sensible defaults when the file or individual fields are absent.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"reflect"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the manager's runtime configuration.
type Config struct {
	// HTTPAddr is the listen address for the dashboard and REST API.
	HTTPAddr string `toml:"http_addr"`
	// TLSCert / TLSKey are PEM certificate and key files. With both set the dashboard and REST
	// API are served over HTTPS on HTTPAddr; with neither, plain HTTP. The files are re-read
	// when they change, so a renewed certificate takes effect without a restart.
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
	// GRPCAddr is the listen address for the associate mTLS stream.
	GRPCAddr string `toml:"grpc_addr"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `toml:"log_level"`

	Auth   Auth   `toml:"auth"`
	Enroll Enroll `toml:"enroll"`
	Audit  Audit  `toml:"audit"`
}

// Audit configures the audit log.
type Audit struct {
	// Max is the number of most-recent entries kept queryable.
	Max int `toml:"max"`
}

// Enroll configures how the quartermaster installs associates on hosts.
type Enroll struct {
	// Mode is retained for backward compatibility but ignored: the install method
	// is now chosen per host at enroll time (SSH credentials present -> remote SSH
	// install; absent -> local child process on the manager box).
	Mode string `toml:"mode"`
	// AssociateBin / HelperBin are paths to the binaries the installer deploys.
	AssociateBin string `toml:"associate_bin"`
	HelperBin    string `toml:"helper_bin"`
	// ManagerAddr / ServerName are baked into each bundle (what the associate dials in
	// dial-home mode).
	ManagerAddr string `toml:"manager_addr"`
	ServerName  string `toml:"server_name"`
	// AssociatePort is the default TCP port a manager-dial associate listens on and the
	// manager dials. Per-host overrides are chosen at enroll time.
	AssociatePort int `toml:"associate_port"`
	// SSHKey is an optional private key the SSH installer authenticates with. Empty means
	// the agent (SSH_AUTH_SOCK) and the standard ~/.ssh keys, falling back to the password
	// supplied with the request.
	SSHKey string `toml:"ssh_key"`
}

// Auth holds dashboard login settings. The password is stored only as a hash.
type Auth struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
}

// TLSEnabled reports whether the dashboard is served over HTTPS.
func (c Config) TLSEnabled() bool { return c.TLSCert != "" && c.TLSKey != "" }

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{
		HTTPAddr: ":8080",
		GRPCAddr: ":8443",
		LogLevel: "info",
		Auth:     Auth{Username: "admin"},
		Enroll: Enroll{
			Mode:          "local",
			ManagerAddr:   "localhost:8443",
			ServerName:    "localhost",
			AssociatePort: 8444,
		},
		Audit: Audit{Max: 1000},
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
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return Config{}, fmt.Errorf("config %s: tls_cert and tls_key must be set together", path)
	}
	for _, k := range unknownKeys(b) {
		if hint := misplacedHint(k); hint != "" {
			slog.Warn("ignoring config key", "file", path, "key", k, "hint", hint)
		} else {
			slog.Warn("ignoring unknown config key", "file", path, "key", k)
		}
	}
	return cfg, nil
}

// unknownKeys lists the dotted keys in a config file that Config does not define. The lenient
// decode in Load drops them silently, which hides mistakes: a `tls_cert` written below an
// [enroll] header, or into a config read by a build that predates it, quietly leaves the
// dashboard on plain HTTP.
func unknownKeys(b []byte) []string {
	var probe Config
	err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&probe)
	var sme *toml.StrictMissingError
	if !errors.As(err, &sme) {
		return nil
	}
	keys := make([]string, 0, len(sme.Errors))
	for i := range sme.Errors {
		keys = append(keys, strings.Join(sme.Errors[i].Key(), "."))
	}
	return keys
}

// misplacedHint explains an unknown key that is really a top-level setting caught under a
// [section] header (TOML assigns every key after a header to that table).
func misplacedHint(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return ""
	}
	name := parts[len(parts)-1]
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() != reflect.Struct && f.Tag.Get("toml") == name {
			return fmt.Sprintf("%s is a top-level setting; move it above the first [section] header", name)
		}
	}
	return ""
}
