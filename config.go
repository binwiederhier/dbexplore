package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	defaultListen  = "127.0.0.1:7879"
	defaultModel   = "claude-opus-5-5"
	defaultMaxRows = 1000
	defaultTimeout = "30s"
)

type sourceConfig struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DSN              string `json:"dsn"`                         // postgres://user:pass@host:5432/db?sslmode=require, or key=value form
	HostitConnection string `json:"hostit_connection,omitempty"` // password (and a missing host) from this hostit connection
	ReadWrite        bool   `json:"read_write,omitempty"`
}

type aiConfig struct {
	APIKey  string `json:"api_key,omitempty"`  // falls back to $ANTHROPIC_API_KEY
	Model   string `json:"model,omitempty"`    // Anthropic: defaults to defaultModel; hostit: its own default
	BaseURL string `json:"base_url,omitempty"` // for an API gateway or proxy; defaults to Anthropic's API
	Context string `json:"context,omitempty"`  // free text about the data, added to the prompt
}

type config struct {
	Listen           string          `json:"listen"`
	Password         string          `json:"password"`
	Secret           string          `json:"secret"`
	NoAuth           bool            `json:"no_auth,omitempty"` // only behind a proxy that authenticates
	Sources          []*sourceConfig `json:"sources"`
	MaxRows          int             `json:"max_rows,omitempty"`
	StatementTimeout string          `json:"statement_timeout,omitempty"`
	AI               *aiConfig       `json:"ai,omitempty"`

	path    string
	timeout time.Duration
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// loadConfig reads the config file, fills in defaults, generates the password and secret on first
// start, and writes the file back so the generated values stick.
func loadConfig(path string) (*config, error) {
	c := &config{path: path}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.Listen == "" {
		c.Listen = defaultListen
	}
	dirty := os.IsNotExist(err)
	if c.Password == "" && !c.NoAuth {
		c.Password, dirty = randomHex(8), true
	}
	if c.Secret == "" {
		c.Secret, dirty = randomHex(32), true
	}
	if c.Sources == nil {
		c.Sources = []*sourceConfig{}
	}
	if c.MaxRows <= 0 {
		c.MaxRows = defaultMaxRows
	}
	if c.StatementTimeout == "" {
		c.StatementTimeout = defaultTimeout
	}
	if c.timeout, err = time.ParseDuration(c.StatementTimeout); err != nil {
		return nil, fmt.Errorf("statement_timeout: %w", err)
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if !validID.MatchString(s.ID) || seen[s.ID] {
			return nil, fmt.Errorf("source id %q must be unique and contain only letters, digits, - and _", s.ID)
		}
		if s.DSN == "" {
			return nil, fmt.Errorf("source %q has no dsn", s.ID)
		}
		if s.Name == "" {
			s.Name = s.ID
		}
		seen[s.ID] = true
	}
	if !dirty {
		return c, nil // never rewrite a complete config, it may be read-only or hand-formatted
	}
	return c, c.save()
}

func (c *config) save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *config) source(id string) *sourceConfig {
	for _, s := range c.Sources {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func defaultTimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(defaultTimeout)
	return d
}

// listenAddr is Listen with environment variables expanded, e.g. "0.0.0.0:${PORT}" on hostit.
func (c *config) listenAddr() string {
	return os.ExpandEnv(c.Listen)
}
