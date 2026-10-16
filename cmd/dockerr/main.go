// dockerr: a small web manager for Docker compose stacks.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neotoxicfr/dockerr/internal/config"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/ops"
	"github.com/neotoxicfr/dockerr/internal/server"
	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/store"
	"github.com/neotoxicfr/dockerr/internal/updates"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("dockerr", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.InsecureNoAuth {
		slog.Warn("DOCKERR_INSECURE_NOAUTH=1: no authentication, bench use only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := docker.New(ctx)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	mgr := stacks.NewManager(d, cfg.StacksDir)
	runner := ops.NewRunner(mgr, cfg.RepairNetDeps)
	events := docker.NewEvents(d.API)
	creds := func(host string) (string, string) {
		key := host
		if host == "docker.io" {
			key = "https://index.docker.io/v1/"
		}
		a, err := d.CLI.ConfigFile().GetAuthConfig(key)
		if err != nil {
			return "", ""
		}
		return a.Username, a.Password
	}
	upd := updates.New(d.API, creds)
	runner.OnDone = func(ops.Snapshot) { events.Notify() }

	srv, err := server.New(server.Deps{Config: cfg, Docker: d, Stacks: mgr, Runner: runner, Store: st,
		Updates: upd, Stats: docker.NewStats(d.API), Events: events})
	if err != nil {
		return err
	}
	srv.Background(ctx)

	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("dockerr listening", "addr", cfg.Listen, "stacks", cfg.StacksDir, "version", server.Version)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}

func healthcheck() int {
	addr := os.Getenv("DOCKERR_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
