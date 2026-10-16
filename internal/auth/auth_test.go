package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestMiddleware(t *testing.T) {
	a := &Auth{Header: "Remote-User", Trusted: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}
	var got string
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = User(r.Context()) }),
		func(r *http.Request) bool { return r.URL.Path == "/healthz" })

	cases := []struct {
		name, method, remote, user, site, origin string
		want                                     int
	}{
		{"trusted proxy", "GET", "172.20.0.5:1234", "neo", "", "", 200},
		{"untrusted source", "GET", "8.8.8.8:1234", "neo", "", "", 401},
		{"no header", "GET", "172.20.0.5:1234", "", "", "", 401},
		{"post same-origin", "POST", "172.20.0.5:1234", "neo", "same-origin", "", 200},
		{"post cross-site", "POST", "172.20.0.5:1234", "neo", "cross-site", "", 403},
		{"post origin ok", "POST", "172.20.0.5:1234", "neo", "", "https://docker.example.com", 200},
		{"post origin bad", "POST", "172.20.0.5:1234", "neo", "", "https://evil.example", 403},
		{"post no origin", "POST", "172.20.0.5:1234", "neo", "", "", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got = ""
			r := httptest.NewRequest(c.method, "https://docker.example.com/", nil)
			r.RemoteAddr = c.remote
			if c.user != "" {
				r.Header.Set("Remote-User", c.user)
			}
			if c.site != "" {
				r.Header.Set("Sec-Fetch-Site", c.site)
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d", w.Code, c.want)
			}
			if c.want == 200 && got != c.user {
				t.Fatalf("user %q, want %q", got, c.user)
			}
		})
	}
	a.Group, a.GroupsHeader = "admins", "Remote-Groups"
	for groups, want := range map[string]int{"users, admins": 200, "users": 403, "": 403} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "172.20.0.5:1"
		r.Header.Set("Remote-User", "neo")
		r.Header.Set("Remote-Groups", groups)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("groups %q: %d, want %d", groups, w.Code, want)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.RemoteAddr = "8.8.8.8:1"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("open path: %d", w.Code)
	}
}

func TestTrustedHost(t *testing.T) {
	a := &Auth{Header: "Remote-User", Hosts: []string{"localhost"}}
	if !a.trusted("127.0.0.1:5555") {
		t.Fatal("localhost not resolved")
	}
	if a.trusted("10.1.2.3:5555") {
		t.Fatal("other address trusted")
	}
}
