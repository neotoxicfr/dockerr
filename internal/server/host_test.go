package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/store"
)

// hostsServer returns the local server of three hosts: local and pi are up,
// nas is down; each host's routes answer with its name.
func hostsServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pages, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	base := &Server{store: st, pages: pages}
	for _, n := range []string{"local", "pi", "nas"} {
		base.hosts = append(base.hosts, &dockerHost{HostDeps: HostDeps{Name: n, Endpoint: "tcp://" + n + ".example:2376"}})
	}
	for _, h := range base.hosts {
		c := *base
		c.cur, c.docker = h, &docker.Docker{}
		name := h.Name
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(name)) })
		c.mux = mux
		h.srv.Store(&c)
		h.set(nil)
	}
	base.hosts[2].set(errors.New("connection refused"))
	return base.hosts[0].srv.Load()
}

func TestDispatch(t *testing.T) {
	s := hostsServer(t)
	tests := []struct {
		name, method, path, cookie, accept string
		header                             string // X-Dockerr-Host
		code                               int
		body                               string
	}{
		{name: "default is local", path: "/", code: 200, body: "local"},
		{name: "cookie picks pi", path: "/s/web", cookie: "pi", code: 200, body: "pi"},
		{name: "unknown host falls back to local", path: "/", cookie: "nope", code: 200, body: "local"},
		{name: "global path stays local", path: "/operations", cookie: "pi", code: 200, body: "local"},
		{name: "static stays local", path: "/static/css/app.css", cookie: "pi", code: 200, body: "local"},
		{name: "down host: page", path: "/", cookie: "nas", accept: "text/html", code: 503, body: "Hôte injoignable"},
		{name: "down host: fragment", path: "/stacks/rows", cookie: "nas", code: 503, body: "connection refused"},
		{name: "down host: operation", method: "POST", path: "/s/web/op", cookie: "nas", accept: "text/html", code: 503, body: "injoignable"},
		{name: "down host: settings from local", path: "/settings", cookie: "nas", accept: "text/html", code: 200, body: "local"},
		{name: "a change goes to the page's host, not the cookie's", method: "POST", path: "/s/web/op", cookie: "local", header: "pi", code: 200, body: "pi"},
		{name: "unknown page host refused", method: "POST", path: "/s/web/op", cookie: "pi", header: "nope", code: 400, body: "hôte inconnu"},
		{name: "a read follows the cookie", path: "/s/web", cookie: "local", header: "pi", code: 200, body: "local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.method
			if m == "" {
				m = http.MethodGet
			}
			r := httptest.NewRequest(m, tt.path, nil)
			if tt.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "host", Value: tt.cookie})
			}
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if tt.header != "" {
				r.Header.Set("X-Dockerr-Host", tt.header)
			}
			w := httptest.NewRecorder()
			s.dispatch(w, r)
			if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.body) {
				t.Fatalf("got %d %q, want %d containing %q", w.Code, w.Body.String(), tt.code, tt.body)
			}
		})
	}
}

func TestSelectHost(t *testing.T) {
	s := hostsServer(t)
	tests := []struct {
		host, referer string
		code          int
		location      string
	}{
		{host: "pi", code: http.StatusSeeOther, location: "/"},
		{host: "pi", referer: "https://dockerr.example/s/web", code: http.StatusSeeOther, location: "/"},
		{host: "local", referer: "https://dockerr.example/settings", code: http.StatusSeeOther, location: "/settings"},
		{host: "evil", code: http.StatusBadRequest},
		{host: "", code: http.StatusBadRequest},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/host", strings.NewReader(url.Values{"host": {tt.host}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tt.referer != "" {
			r.Header.Set("Referer", tt.referer)
		}
		w := httptest.NewRecorder()
		s.selectHost(w, r)
		if w.Code != tt.code {
			t.Fatalf("%s: code %d, want %d", tt.host, w.Code, tt.code)
		}
		if tt.code != http.StatusSeeOther {
			if len(w.Result().Cookies()) != 0 {
				t.Fatalf("%s: cookie set for a refused host", tt.host)
			}
			continue
		}
		c := w.Result().Cookies()
		if len(c) != 1 || c[0].Name != "host" || c[0].Value != tt.host || !c[0].HttpOnly || w.Header().Get("Location") != tt.location {
			t.Fatalf("%s: cookies %v, location %q", tt.host, c, w.Header().Get("Location"))
		}
	}
}

func TestHostKeys(t *testing.T) {
	s := hostsServer(t)
	pi := s.hosts[1].srv.Load()
	if s.storeHost() != "" || s.histKey("web") != "web" {
		t.Fatalf("local: %q %q", s.storeHost(), s.histKey("web"))
	}
	if pi.storeHost() != "pi" || pi.histKey("web") != "pi/web" {
		t.Fatalf("pi: %q %q", pi.storeHost(), pi.histKey("web"))
	}
	if !pi.cur.plain() || (&dockerHost{HostDeps: HostDeps{Endpoint: "tcp://x:2376", TLS: true}}).plain() ||
		(&dockerHost{}).plain() {
		t.Fatal("plain TCP misreported")
	}
}
