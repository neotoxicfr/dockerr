// Package server serves dockerr's web interface.
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/config"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/ops"
	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/store"
	"github.com/neotoxicfr/dockerr/internal/updates"
	"github.com/neotoxicfr/dockerr/web"
)

// Version is set at build time.
var Version = "dev"

type Server struct {
	cfg     *config.Config
	docker  *docker.Docker
	stacks  *stacks.Manager
	runner  *ops.Runner
	store   *store.Store
	updates *updates.Checker
	stats   *docker.Stats
	events  *docker.Events

	pages  map[string]*template.Template
	static fs.FS
}

type Deps struct {
	Config  *config.Config
	Docker  *docker.Docker
	Stacks  *stacks.Manager
	Runner  *ops.Runner
	Store   *store.Store
	Updates *updates.Checker
	Stats   *docker.Stats
	Events  *docker.Events
}

func New(d Deps) (*Server, error) {
	s := &Server{cfg: d.Config, docker: d.Docker, stacks: d.Stacks, runner: d.Runner, store: d.Store,
		updates: d.Updates, stats: d.Stats, events: d.Events}
	var err error
	if s.pages, err = parseTemplates(); err != nil {
		return nil, err
	}
	s.static, _ = fs.Sub(web.FS, "static")
	return s, nil
}

func parseTemplates() (map[string]*template.Template, error) {
	base, err := template.New("").Funcs(funcs).ParseFS(web.FS, "templates/layout.html", "templates/partials/*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(web.FS, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		t, err := template.Must(base.Clone()).ParseFS(web.FS, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out[strings.TrimSuffix(path.Base(p), ".html")] = t
	}
	return out, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.staticHandler()))
	mux.HandleFunc("GET /sw.js", s.serviceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /stacks/rows", s.homeRows)
	mux.HandleFunc("GET /new", s.newStack)
	mux.HandleFunc("POST /new", s.createStack)
	mux.HandleFunc("POST /new/validate", s.validateNew)
	mux.HandleFunc("GET /s/{name}", s.stackPage)
	mux.HandleFunc("GET /s/{name}/services", s.stackServices)
	mux.HandleFunc("POST /s/{name}/op", s.stackOp)
	mux.HandleFunc("GET /s/{name}/files", s.stackFiles)
	mux.HandleFunc("POST /s/{name}/validate", s.stackValidate)
	mux.HandleFunc("POST /s/{name}/diff", s.stackDiff)
	mux.HandleFunc("POST /s/{name}/save", s.stackSave)
	mux.HandleFunc("GET /s/{name}/history", s.stackHistory)
	mux.HandleFunc("GET /s/{name}/history/{id}", s.stackVersion)
	mux.HandleFunc("POST /s/{name}/history/{id}/restore", s.stackRestore)
	mux.HandleFunc("POST /compose/check", s.composeCheck)
	mux.HandleFunc("GET /s/{name}/secrets/{secret}", s.secretReveal)
	mux.HandleFunc("POST /s/{name}/secrets", s.secretSave)
	mux.HandleFunc("GET /ops/{id}", s.opStream)
	mux.HandleFunc("GET /events", s.eventStream)
	mux.HandleFunc("POST /updates/check", s.checkUpdates)

	mux.HandleFunc("GET /resources", s.resources)
	mux.HandleFunc("GET /c/{id}", s.containerPage)
	mux.HandleFunc("POST /c/{id}/action", s.containerAction)
	mux.HandleFunc("GET /c/{id}/env", s.containerEnv)
	mux.HandleFunc("POST /resources/prune", s.prune)
	mux.HandleFunc("POST /images/{id}/remove", s.imageRemove)
	mux.HandleFunc("GET /network", s.networkPage)
	mux.HandleFunc("GET /c/{id}/terminal", s.terminalPage)
	mux.HandleFunc("GET /c/{id}/exec", s.execSocket)

	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("POST /settings/mode", s.setMode)
	mux.HandleFunc("POST /prefs", s.setPref)

	a := &auth.Auth{Header: s.cfg.AuthHeader, Trusted: s.cfg.TrustedProxies, Hosts: s.cfg.TrustedHosts, Insecure: s.cfg.InsecureNoAuth,
		Group: s.cfg.RequireGroup, GroupsHeader: s.cfg.GroupsHeader}
	open := func(r *http.Request) bool {
		p := r.URL.Path
		// /api/status checks its own key instead of the auth proxy's header.
		return p == "/healthz" || p == "/manifest.webmanifest" || p == "/sw.js" || strings.HasPrefix(p, "/static/") ||
			(p == "/api/status" && r.Method == http.MethodGet)
	}
	return securityHeaders(a.Middleware(mux, open))
}

const cspRest = "object-src 'none'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// csp allows <style> elements carrying the request's nonce: CodeMirror
// injects its styles that way. Scripts stay 'self' only.
func csp(nonce string) string {
	return "default-src 'self'; img-src 'self' data:; style-src 'self' 'nonce-" + nonce + "'; " + cspRest
}

// cspTerminal: xterm.js injects <style> elements and cannot carry a nonce,
// so the terminal page alone allows inline styles (never inline scripts).
const cspTerminal = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " + cspRest

type nonceKey struct{}

func nonce(r *http.Request) string {
	n, _ := r.Context().Value(nonceKey{}).(string)
	return n
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		n := base64.RawURLEncoding.EncodeToString(b)
		h.Set("Content-Security-Policy", csp(n))
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, n)))
	})
}

// staticHandler serves embedded assets, gzipped when the client accepts it.
// URLs carry ?v=<version>, so they can be cached for long.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServerFS(s.static)
	var mu sync.Mutex
	gz := map[string][]byte{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		compressible := strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".svg")
		if !compressible || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			files.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		body, ok := gz[p]
		mu.Unlock()
		if !ok {
			raw, err := fs.ReadFile(s.static, p)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			var b bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
			_, _ = zw.Write(raw)
			_ = zw.Close()
			body = b.Bytes()
			mu.Lock()
			gz[p] = body
			mu.Unlock()
		}
		ct := "text/css; charset=utf-8"
		switch {
		case strings.HasSuffix(p, ".js"):
			ct = "text/javascript; charset=utf-8"
		case strings.HasSuffix(p, ".svg"):
			ct = "image/svg+xml"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		_, _ = w.Write(body)
	})
}

func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(bytes.ReplaceAll(b, []byte("__VERSION__"), []byte(Version)))
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.static, "manifest.webmanifest")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// page is the data every full page gets.
type page struct {
	Title    string
	Nav      string
	User     string
	Advanced bool
	Version  string
	Caps     docker.Caps
	Nonce    string // CSP nonce for injected <style> elements
	Data     any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title, nav string, data any) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "page inconnue", http.StatusInternalServerError)
		return
	}
	p := page{Title: title, Nav: nav, User: auth.User(r.Context()), Advanced: s.advanced(r), Version: Version, Caps: s.docker.Caps, Nonce: nonce(r), Data: data}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "layout", p); err != nil {
		slog.Error("render", "page", name, "err", err)
		http.Error(w, "erreur d'affichage", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = b.WriteTo(w)
}

// partial renders a named template without the layout.
func (s *Server) partial(w http.ResponseWriter, r *http.Request, name string, data any) {
	var b bytes.Buffer
	t := s.pages["home"] // every page set holds the partials
	if err := t.ExecuteTemplate(&b, name, struct {
		page
		Data any
	}{page{Advanced: s.advanced(r), Caps: s.docker.Caps, Version: Version}, data}); err != nil {
		slog.Error("render", "partial", name, "err", err)
		http.Error(w, "erreur d'affichage", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = b.WriteTo(w)
}

func (s *Server) advanced(r *http.Request) bool {
	return s.store.Pref(r.Context(), auth.User(r.Context()), "advanced") == "1"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sse prepares a server-sent events response.
func sse(w http.ResponseWriter) (func(event string, data any) error, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return func(event string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}, true
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	send, ok := sse(w)
	if !ok {
		return
	}
	ch, cancel := s.events.Subscribe()
	defer cancel()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if send("change", nil) != nil {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}
}

// Background starts the loops the server depends on.
func (s *Server) Background(ctx context.Context) {
	go s.stats.Loop(ctx)
	if s.docker.Caps.Events {
		go s.events.Loop(ctx)
	} else {
		// Without the events endpoint, poll: pages still refresh.
		go func() {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.events.Notify()
				}
			}
		}()
	}
	go s.updates.Loop(ctx, s.cfg.UpdateInterval, s.imageRefs)
}

func (s *Server) imageRefs(ctx context.Context) []string {
	all, err := s.stacks.Containers(ctx)
	if err != nil {
		return nil
	}
	var refs []string
	for _, c := range all {
		refs = append(refs, c.Image)
	}
	return refs
}
