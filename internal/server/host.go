package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/notify"
	"github.com/neotoxicfr/dockerr/internal/ops"
	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/updates"
)

// HostDeps describes one Docker host; the first one given to New is the
// local host, the others are reached over TCP.
type HostDeps struct {
	Name     string
	Endpoint string // tcp://host:port; "" for the local host (DOCKER_HOST)
	TLS      bool
	// StacksDir is where the host's compose folders are mounted, "" if not.
	StacksDir string
	// PortHost is the name used in links to published ports; "" for the
	// local host, which follows DOCKERR_PORT_HOST or the request's host.
	PortHost string
	// Connect builds what depends on the daemon. It fails when the host
	// cannot be reached; a remote host is then retried in the background.
	Connect func(ctx context.Context) (*Bundle, error)
}

// Bundle is what dockerr keeps per Docker host.
type Bundle struct {
	Docker  *docker.Docker
	Stacks  *stacks.Manager
	Runner  *ops.Runner
	Updates *updates.Checker
	Stats   *docker.Stats
	Events  *docker.Events
}

// dockerHost is a host and, once connected, the server bound to it.
type dockerHost struct {
	HostDeps
	srv atomic.Pointer[Server]
	up  atomic.Bool
	mu  sync.Mutex
	err string // why the last connection or ping failed
}

func (h *dockerHost) set(err error) {
	h.up.Store(err == nil)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.err = ""
	} else {
		h.err = err.Error()
	}
}

func (h *dockerHost) lastErr() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// plain reports a remote host reached without TLS: whoever can reach that
// port, or read the traffic, is root on it. The local host's endpoint is
// left out: a socket proxy on an internal Docker network is the usual setup.
func (h *dockerHost) plain() bool { return h.Endpoint != "" && !h.TLS }

// bind makes the server copy that serves h: same pages and store, h's daemon.
func (s *Server) bind(h *dockerHost, b *Bundle) *Server {
	c := *s
	c.cur = h
	c.docker, c.stacks, c.runner, c.updates, c.stats, c.events = b.Docker, b.Stacks, b.Runner, b.Updates, b.Stats, b.Events
	c.alertsNudge = make(chan struct{}, 1)
	if c.notifier.Enabled() {
		dw := &deaths{stopped: map[string]time.Time{}}
		c.events.OnContainer = func(m events.Message) {
			if e, ok := dw.event(m, time.Now()); ok {
				c.notify(e)
			}
		}
		c.updates.OnAvailable = c.notifyUpdates
	}
	c.mux = c.routes()
	h.srv.Store(&c)
	return &c
}

// notify sends e, naming the host when there are several.
func (s *Server) notify(e notify.Event) {
	if len(s.hosts) > 1 {
		e.Host = s.cur.Name
	}
	s.notifier.Notify(e)
}

// storeHost is the host as operations are recorded: "" for the local one.
func (s *Server) storeHost() string {
	if s.cur == s.hosts[0] {
		return ""
	}
	return s.cur.Name
}

// histKey is the stack name the compose history is kept under: names
// cannot hold a slash, so "host/stack" never meets a local stack.
func (s *Server) histKey(stack string) string {
	if h := s.storeHost(); h != "" {
		return h + "/" + stack
	}
	return stack
}

// hostOf returns the host a request is for: the "host" cookie, or the local one.
func (s *Server) hostOf(r *http.Request) *dockerHost {
	if c, err := r.Cookie("host"); err == nil {
		for _, h := range s.hosts {
			if h.Name == c.Value {
				return h
			}
		}
	}
	return s.hosts[0]
}

// global paths do not depend on the selected host: the local server serves them.
func global(p string) bool {
	for _, g := range []string{"/static/", "/api/", "/settings/", "/notify/"} {
		if strings.HasPrefix(p, g) {
			return true
		}
	}
	switch p {
	case "/healthz", "/sw.js", "/manifest.webmanifest", "/host", "/prefs", "/operations", "/metrics":
		return true
	}
	return false
}

// dispatch routes a request to the server of its host. A host that is down
// gets an error at once: its requests would only wait for timeouts.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if global(r.URL.Path) {
		s.mux.ServeHTTP(w, r)
		return
	}
	h := s.hostOf(r)
	// A change goes to the host of the page it comes from: the cookie is
	// shared by every tab, and another tab may have switched it since.
	if name := r.Header.Get("X-Dockerr-Host"); name != "" && r.Method != http.MethodGet && r.Method != http.MethodHead {
		h = nil
		for _, x := range s.hosts {
			if x.Name == name {
				h = x
			}
		}
		if h == nil {
			jsonError(w, http.StatusBadRequest, "hôte inconnu : "+name)
			return
		}
	}
	srv := h.srv.Load()
	if srv != nil && h.up.Load() {
		srv.mux.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/settings" {
		// Settings show every host, the unreachable one included.
		s.mux.ServeHTTP(w, r)
		return
	}
	msg := "hôte " + h.Name + " injoignable"
	if e := h.lastErr(); e != "" {
		msg += " : " + e
	}
	if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		jsonError(w, http.StatusServiceUnavailable, msg)
		return
	}
	// 503: live fragments fetched as HTML are not replaced by this page.
	s.render(&statusWriter{ResponseWriter: w, code: http.StatusServiceUnavailable}, r, "down", h.Name, "",
		hostDown{Name: h.Name, Endpoint: h.Endpoint, Err: h.lastErr()})
}

// statusWriter sends code instead of the status the handler writes.
type statusWriter struct {
	http.ResponseWriter
	code int
	sent bool
}

func (w *statusWriter) WriteHeader(int) {
	if !w.sent {
		w.sent = true
		w.ResponseWriter.WriteHeader(w.code)
	}
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.WriteHeader(0)
	return w.ResponseWriter.Write(b)
}

type hostDown struct{ Name, Endpoint, Err string }

// selectHost sets the host the next pages are for.
func (s *Server) selectHost(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("host")
	known := false
	for _, h := range s.hosts {
		known = known || h.Name == name
	}
	if !known {
		http.Error(w, "hôte inconnu", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "host", Value: name, Path: "/", MaxAge: 400 * 24 * 3600, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	// Stacks and containers belong to one host: only the settings pages
	// mean the same thing on the other.
	back := "/"
	if u, err := url.Parse(r.Referer()); err == nil && (u.Path == "/settings" || u.Path == "/operations") {
		back = u.Path
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// hostView is a host as the selector and the settings page show it.
type hostView struct {
	Name, Endpoint, StacksDir, Err string
	TLSVar                         string // the variable naming its TLS folder
	Current, Up, Connected, Plain  bool
	DaemonVersion, APIVersion      string
	Caps                           []capView
}

// hostViews lists the hosts; with details, the daemon version and rights of
// each reachable one, asked in parallel with a short timeout.
func (s *Server) hostViews(r *http.Request, details bool) []hostView {
	cur := s.hostOf(r)
	out := make([]hostView, len(s.hosts))
	var wg sync.WaitGroup
	for i, h := range s.hosts {
		v := hostView{Name: h.Name, Endpoint: h.Endpoint, StacksDir: h.StacksDir, Current: h == cur, Up: h.up.Load(),
			Plain: h.plain(), Err: h.lastErr(), TLSVar: "DOCKERR_TLS_" + strings.ToUpper(strings.ReplaceAll(h.Name, "-", "_")) + "_DIR"}
		srv := h.srv.Load()
		v.Connected = srv != nil
		if srv != nil {
			if v.Endpoint == "" {
				v.Endpoint = srv.docker.Host
			}
			if details {
				for _, c := range srv.docker.Caps.List() {
					v.Caps = append(v.Caps, capView{c.Name, c.Env, c.OK})
				}
			}
		}
		out[i] = v
		if details && srv != nil && v.Up {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				defer cancel()
				if ver, err := srv.docker.API.ServerVersion(ctx, client.ServerVersionOptions{}); err == nil {
					out[i].DaemonVersion, out[i].APIVersion = ver.Version, ver.APIVersion
				}
			}()
		}
	}
	wg.Wait()
	return out
}

// watchHosts keeps each host's state: a ping every 15 s, and for a remote
// host not connected yet, a connection attempt that starts its loops.
func (s *Server) watchHosts(ctx context.Context) {
	for _, h := range s.hosts {
		go func() {
			t := time.NewTicker(15 * time.Second)
			defer t.Stop()
			for {
				if srv := h.srv.Load(); srv == nil {
					cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
					b, err := h.Connect(cctx)
					cancel()
					if err == nil {
						s.bind(h, b).loops(ctx)
						slog.Info("docker host connected", "host", h.Name, "endpoint", h.Endpoint)
					} else if h.lastErr() != err.Error() {
						slog.Warn("docker host unreachable", "host", h.Name, "endpoint", h.Endpoint, "err", err)
					}
					h.set(err)
				} else {
					pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					_, err := srv.docker.API.Ping(pctx, client.PingOptions{})
					cancel()
					if was := h.up.Load(); was != (err == nil) {
						slog.Info("docker host state", "host", h.Name, "up", err == nil, "err", err)
					}
					h.set(err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
}
