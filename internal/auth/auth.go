// Package auth trusts the user name set by a forward-auth proxy (Authelia)
// and rejects cross-site requests that change state.
package auth

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type ctxKey struct{}

// User returns the authenticated user name.
func User(ctx context.Context) string {
	u, _ := ctx.Value(ctxKey{}).(string)
	return u
}

type Auth struct {
	Header  string
	Trusted []netip.Prefix
	// Hosts are resolved every 30 s: a proxy container's IP changes when it
	// is recreated.
	Hosts    []string
	Insecure bool
	// Group, when set, must be listed in GroupsHeader (comma-separated).
	Group        string
	GroupsHeader string
}

func (a *Auth) inGroup(r *http.Request) bool {
	if a.Group == "" {
		return true
	}
	for _, g := range strings.Split(r.Header.Get(a.GroupsHeader), ",") {
		if strings.TrimSpace(g) == a.Group {
			return true
		}
	}
	return false
}

var (
	resolveMu  sync.Mutex
	resolved   []netip.Addr
	resolvedAt time.Time
)

func (a *Auth) hostAddrs() []netip.Addr {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	if time.Since(resolvedAt) < 30*time.Second {
		return resolved
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out []netip.Addr
	for _, h := range a.Hosts {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			out = append(out, ip.Unmap())
		}
	}
	resolved, resolvedAt = out, time.Now()
	return out
}

func (a *Auth) trusted(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, p := range a.Trusted {
		if p.Contains(ip) {
			return true
		}
	}
	if len(a.Hosts) > 0 {
		for _, h := range a.hostAddrs() {
			if h == ip {
				return true
			}
		}
	}
	return false
}

// Middleware authenticates every request except the paths in open.
func (a *Auth) Middleware(next http.Handler, open func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open(r) {
			next.ServeHTTP(w, r)
			return
		}
		user := "local"
		if !a.Insecure {
			user = r.Header.Get(a.Header)
			if user == "" || !a.trusted(r.RemoteAddr) {
				http.Error(w, "Accès refusé : dockerr s'ouvre derrière le proxy d'authentification.", http.StatusUnauthorized)
				return
			}
			if !a.inGroup(r) {
				http.Error(w, "Accès refusé : groupe "+a.Group+" requis.", http.StatusForbidden)
				return
			}
		}
		if !SameOrigin(r) {
			http.Error(w, "Requête d'une autre origine refusée.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	})
}

// SameOrigin accepts safe methods, and state-changing requests that the
// browser marks same-origin (Sec-Fetch-Site) or whose Origin matches Host.
func SameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}
