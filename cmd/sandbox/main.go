// Command sandbox is a reference code execution service for the Oasis framework.
//
// It receives code via HTTP, executes it in a subprocess, and returns results.
// Designed to run as a sidecar container alongside the Oasis app.
//
// The reference sandbox is a minimal, single-tenant execution service suitable
// for development and small-scale deployments. For production workloads requiring
// multi-tenancy, auto-scaling, or stronger isolation guarantees, consider a managed
// code execution service or build a custom CodeRunner implementation.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type config struct {
	addr            string
	workspaceRoot   string
	pythonBin       string
	nodeBin         string
	maxConcurrent   int
	sessionTTL      time.Duration
	cleanupInterval time.Duration
	maxOutputBytes  int
}

func loadConfig() config {
	cfg := config{
		addr:            ":9000",
		workspaceRoot:   "/var/sandbox",
		pythonBin:       "python3",
		nodeBin:         "node",
		maxConcurrent:   4,
		sessionTTL:      time.Hour,
		cleanupInterval: 5 * time.Minute,
		maxOutputBytes:  512 * 1024,
	}
	if v := os.Getenv("SANDBOX_ADDR"); v != "" {
		cfg.addr = v
	}
	if v := os.Getenv("SANDBOX_WORKSPACE"); v != "" {
		cfg.workspaceRoot = v
	}
	if v := os.Getenv("SANDBOX_PYTHON_BIN"); v != "" {
		cfg.pythonBin = v
	}
	if v := os.Getenv("SANDBOX_NODE_BIN"); v != "" {
		cfg.nodeBin = v
	}
	if v := os.Getenv("SANDBOX_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.maxConcurrent = n
		}
	}
	if v := os.Getenv("SANDBOX_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.sessionTTL = d
		}
	}
	if v := os.Getenv("SANDBOX_MAX_OUTPUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.maxOutputBytes = n
		}
	}
	return cfg
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[sandbox] ")

	cfg := loadConfig()

	sessions := newSessionManager(cfg.workspaceRoot, cfg.sessionTTL)
	sessions.start(cfg.cleanupInterval)

	run := newRunner(cfg.pythonBin, cfg.nodeBin, cfg.maxOutputBytes)
	sem := make(chan struct{}, cfg.maxConcurrent)

	mux := http.NewServeMux()
	mux.HandleFunc("/execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleExecute(sem, sessions, run, w, r)
	})
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/workspace/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleDeleteWorkspace(sessions, w, r)
	})

	srv := &http.Server{
		Addr:         cfg.addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("listening on %s", cfg.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	sessions.close()
	log.Println("stopped")
}
