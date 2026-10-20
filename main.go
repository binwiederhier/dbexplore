// Command dbexplore is a small, read-only, DataGrip-style explorer for PostgreSQL databases.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

var version = "dev" // set by goreleaser

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "config file")
	printPassword := flag.Bool("print-password", false, "print the login password and exit")
	printVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *printVersion {
		fmt.Println("dbexplore", version)
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		slog.Error("cannot load config", "error", err)
		os.Exit(1)
	}
	if *printPassword {
		fmt.Println(cfg.Password)
		return
	}
	if len(cfg.Sources) == 0 {
		slog.Warn("no databases configured, add one to \"sources\" in the config and restart", "config", *cfgPath)
	}
	s := &server{cfg: cfg, db: newDB(cfg), ai: newAssistant(cfg.AI), cache: map[string]cacheEntry{}}
	defer s.db.close()
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	s.routes(mux, http.FileServerFS(static))
	srv := &http.Server{Addr: cfg.listenAddr(), Handler: s.auth(mux), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	aiBackend := "off"
	if s.ai != nil {
		aiBackend = s.ai.backend.name()
	}
	slog.Info("listening", "addr", cfg.listenAddr(), "config", *cfgPath, "sources", len(cfg.Sources), "ai", aiBackend, "version", version)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// defaultConfigPath is $DBEXPLORE_CONFIG, else ~/.config/dbexplore/config.json.
func defaultConfigPath() string {
	if p := os.Getenv("DBEXPLORE_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dbexplore", "config.json")
}
