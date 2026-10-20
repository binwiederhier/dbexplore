package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Password == "" || c.Secret == "" || c.Listen != defaultListen || c.MaxRows != defaultMaxRows {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c2, err := loadConfig(path)
	if err != nil || c2.Password != c.Password {
		t.Fatalf("generated password must persist: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"password":"x","secret":"y","sources":[{"id":"a b","dsn":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("invalid source id must be rejected")
	}
}

func TestListenExpandsEnv(t *testing.T) {
	t.Setenv("PORT", "10123")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"0.0.0.0:${PORT}","password":"x","secret":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.listenAddr(); got != "0.0.0.0:10123" {
		t.Errorf("listenAddr = %q", got)
	}
	if c.Listen != "0.0.0.0:${PORT}" {
		t.Errorf("the config must keep the unexpanded value, got %q", c.Listen)
	}
}
