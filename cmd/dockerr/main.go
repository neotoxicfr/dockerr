// dockerr: a small web manager for Docker compose stacks.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/neotoxicfr/dockerr/internal/config"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/notify"
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
	// compose-go logs a warning per unset variable on every load; the editor
	// reports them itself.
	logrus.SetLevel(logrus.ErrorLevel)
	if cfg.InsecureNoAuth {
		slog.Warn("DOCKERR_INSECURE_NOAUTH=1: no authentication, bench use only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	notifier, err := notify.New(cfg.NtfyURL, cfg.NtfyToken, cfg.WebhookURL)
	if err != nil {
		return err
	}

	multi := len(cfg.Hosts) > 0
	hosts := []server.HostDeps{{Name: cfg.HostName, StacksDir: cfg.StacksDir,
		Connect: connect(cfg, st, notifier, config.Host{Name: cfg.HostName, StacksDir: cfg.StacksDir}, "", multi)}}
	for _, h := range cfg.Hosts {
		if h.TLSDir == "" {
			slog.Warn("docker host over plain TCP: anyone on the network path is root on it, use TLS", "host", h.Name, "endpoint", h.URL)
		}
		hosts = append(hosts, server.HostDeps{Name: h.Name, Endpoint: h.URL, TLS: h.TLSDir != "", StacksDir: h.StacksDir,
			PortHost: h.PortHost, Connect: connect(cfg, st, notifier, h, h.Name, multi)})
	}
	srv, err := server.New(ctx, server.Deps{Config: cfg, Store: st, Notify: notifier, Hosts: hosts})
	if err != nil {
		return err
	}
	srv.Background(ctx)

	// Requests live in ctx: on SIGTERM, long streams (logs, events) end at
	// once instead of holding the shutdown for its full timeout.
	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("dockerr listening", "addr", cfg.Listen, "stacks", cfg.StacksDir, "hosts", len(hosts), "version", server.Version)
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

// connect returns what builds a host's daemon client, stacks, runner and
// loops. record is the host as operations are recorded ("" = local);
// multi: notifications name the host.
func connect(cfg *config.Config, st *store.Store, notifier *notify.Notifier, h config.Host, record string, multi bool) func(context.Context) (*server.Bundle, error) {
	return func(ctx context.Context) (*server.Bundle, error) {
		d, err := docker.New(ctx, h.URL, h.TLSDir)
		if err != nil {
			return nil, err
		}
		mgr := stacks.NewManager(d, h.StacksDir)
		mgr.Remote = h.URL != ""
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
		label := ""
		if multi {
			label = h.Name
		}
		runner.OnDone = func(o ops.Snapshot) {
			events.Notify()
			notifyOp(notifier, label, o)
			rec := store.Operation{ID: o.ID, Host: record, Stack: o.Stack, Service: o.Service, Kind: string(o.Kind), User: o.User,
				Started: o.Started, Ended: o.Ended, Err: o.Err}
			for _, t := range o.Tasks {
				rec.Tasks = append(rec.Tasks, store.OpTask{ID: t.ID, Text: t.Text, Details: t.Details, Status: t.Status})
			}
			if err := st.SaveOp(context.Background(), rec); err != nil {
				slog.Warn("operation not recorded", "stack", o.Stack, "err", err)
			}
		}
		return &server.Bundle{Docker: d, Stacks: mgr, Runner: runner, Updates: updates.New(d.API, creds),
			Stats: docker.NewStats(d.API), Events: events}, nil
	}
}

// notifyOp reports a failed operation, and the shared networks it repaired.
func notifyOp(n *notify.Notifier, host string, o ops.Snapshot) {
	what := o.Stack
	if o.Service != "" {
		what = o.Stack + " · " + o.Service
	}
	if o.Err != "" {
		n.Notify(notify.Event{Kind: notify.OpFailed, Host: host, Stack: o.Stack, Container: o.Service, Priority: 4,
			Title: "Échec : " + strings.ToLower(o.Label) + " · " + what, Message: o.Err + " (par " + o.User + ")"})
	}
	var fixed []string
	for _, t := range o.Tasks {
		if t.Status == "done" && (strings.HasPrefix(t.Text, "Reconnecté") || t.Text == "Recréé") {
			fixed = append(fixed, strings.TrimPrefix(t.ID, "Réseau "))
		}
	}
	if len(fixed) > 0 {
		n.Notify(notify.Event{Kind: notify.Repaired, Host: host, Stack: o.Stack, Priority: 3,
			Title: "Réseau partagé réparé : " + o.Stack, Message: strings.Join(fixed, ", ") + " a retrouvé son réseau."})
	}
}
