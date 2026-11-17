package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// apiKeyOK accepts "Authorization: Bearer <key>" or "X-Api-Key: <key>" when
// the key's SHA-256 is listed in the keys file with that scope. A line is
// "<sha256 hex> [scope=status|check|metrics] [comment]"; without scope= it is a
// status key. Each key has one scope: the check key cannot read the status
// and the status key cannot start a check. The file is read on each request,
// so a key is added or revoked without a restart.
func apiKeyOK(r *http.Request, keysFile, scope string) bool {
	if keysFile == "" {
		return false
	}
	key := r.Header.Get("X-Api-Key")
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		key = b
	}
	key = strings.TrimSpace(key)
	if len(key) < 32 {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	f, err := os.Open(keysFile)
	if err != nil {
		return false
	}
	defer f.Close()
	ok := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		lineScope := "status"
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "scope="); ok {
				lineScope = v
			}
		}
		want, err := hex.DecodeString(fields[0])
		if err == nil && len(want) == len(sum) && subtle.ConstantTimeCompare(want, sum[:]) == 1 && lineScope == scope {
			ok = true // no early return: same time whichever line matches
		}
	}
	return ok
}

// statusJSON is what /api/status returns: counts and stack names, nothing
// from compose files, environments or secrets.
type statusJSON struct {
	Version    string        `json:"version"`
	Host       string        `json:"host,omitempty"`  // with several hosts: the one counted
	Hosts      []hostState   `json:"hosts,omitempty"` // with several hosts: all of them
	Stacks     stackCounts   `json:"stacks"`
	Containers ctrCounts     `json:"containers"`
	Updates    updatesStatus `json:"updates"`
	Problems   []string      `json:"problems"`
	NetLost    int           `json:"network_lost"`
}

type hostState struct {
	Name string `json:"name"`
	Up   bool   `json:"up"`
}

type stackCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Partial int `json:"partial"`
	Stopped int `json:"stopped"`
	Problem int `json:"problem"`
}

type ctrCounts struct {
	Total   int `json:"total"`
	Running int `json:"running"`
}

type updatesStatus struct {
	Stacks       int      `json:"stacks"`
	Services     int      `json:"services"`
	Names        []string `json:"names"`
	LastCheck    string   `json:"last_check,omitempty"`
	LastCheckAge int64    `json:"last_check_age_s"`
	Checking     bool     `json:"checking"`
}

func buildStatus(all []*stacks.Container, list []*stacks.Stack, last time.Time, checking bool) statusJSON {
	st := statusJSON{Version: Version, Problems: []string{}, Updates: updatesStatus{Names: []string{}, Checking: checking, LastCheckAge: -1}}
	st.Stacks.Total = len(list)
	for _, s := range list {
		switch s.State {
		case stacks.Running:
			st.Stacks.Running++
		case stacks.Partial:
			st.Stacks.Partial++
		case stacks.Problem:
			st.Stacks.Problem++
			st.Problems = append(st.Problems, s.Name)
		default:
			st.Stacks.Stopped++
		}
		if s.Updates > 0 {
			st.Updates.Stacks++
			st.Updates.Services += s.Updates
			st.Updates.Names = append(st.Updates.Names, s.Name)
		}
	}
	for _, c := range all {
		st.Containers.Total++
		if c.Running() {
			st.Containers.Running++
		}
		if c.NetLost {
			st.NetLost++
		}
	}
	if !last.IsZero() {
		st.Updates.LastCheck = last.UTC().Format(time.RFC3339)
		st.Updates.LastCheckAge = int64(time.Since(last).Seconds())
	}
	return st
}

// statusCache holds each host's status for 15 s.
var statusCache struct {
	sync.Mutex
	at  map[string]time.Time
	val map[string]statusJSON
}

// apiHost is the host named by ?host=, the local one without it. It writes
// the error itself: unknown name, or a host down or not connected yet.
func (s *Server) apiHost(w http.ResponseWriter, r *http.Request) (*dockerHost, *Server, bool) {
	name := r.URL.Query().Get("host")
	if name == "" {
		name = s.hosts[0].Name
	}
	for _, h := range s.hosts {
		if h.Name != name {
			continue
		}
		srv := h.srv.Load()
		if srv == nil || !h.up.Load() {
			jsonError(w, http.StatusServiceUnavailable, "hôte "+name+" injoignable")
			return nil, nil, false
		}
		return h, srv, true
	}
	jsonError(w, http.StatusBadRequest, "hôte inconnu : "+name)
	return nil, nil, false
}

// apiStatus serves a summary for dashboards, read-only, cached 15 s.
func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	if !apiKeyOK(r, s.cfg.APIKeysFile, "status") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dockerr"`)
		jsonError(w, http.StatusUnauthorized, "clé d'API absente ou invalide")
		return
	}
	h, srv, ok := s.apiHost(w, r)
	if !ok {
		return
	}
	statusCache.Lock()
	defer statusCache.Unlock()
	if statusCache.at == nil {
		statusCache.at, statusCache.val = map[string]time.Time{}, map[string]statusJSON{}
	}
	if time.Since(statusCache.at[h.Name]) > 15*time.Second {
		all, list, err := srv.snapshot(r.Context())
		if err != nil {
			jsonError(w, http.StatusBadGateway, "Docker injoignable")
			return
		}
		last, checking := srv.updates.LastRun()
		statusCache.val[h.Name], statusCache.at[h.Name] = buildStatus(all, list, last, checking), time.Now()
	}
	st := statusCache.val[h.Name]
	if len(s.hosts) > 1 {
		// Up or down is read now, not cached with the counts.
		st.Host = h.Name
		for _, x := range s.hosts {
			st.Hosts = append(st.Hosts, hostState{x.Name, x.srv.Load() != nil && x.up.Load()})
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// apiCheck starts an image update check: HEAD requests to the registries,
// no pull, nothing restarted. Idempotent, and at most once every 5 minutes.
func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	if !apiKeyOK(r, s.cfg.APIKeysFile, "check") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dockerr"`)
		jsonError(w, http.StatusUnauthorized, "clé d'API absente ou invalide")
		return
	}
	// ?host= checks that host; without it, every host that is up.
	var targets []*dockerHost
	if r.URL.Query().Get("host") != "" {
		h, _, ok := s.apiHost(w, r)
		if !ok {
			return
		}
		targets = []*dockerHost{h}
	} else {
		for _, h := range s.hosts {
			if h.srv.Load() != nil && h.up.Load() {
				targets = append(targets, h)
			}
		}
	}
	type result struct {
		Started bool   `json:"started"`
		Images  int    `json:"images,omitempty"`
		Reason  string `json:"reason,omitempty"`
	}
	res := map[string]*result{}
	started, images := false, 0
	var wait time.Duration
	waiting := 0
	for _, h := range targets {
		srv := h.srv.Load()
		rs := &result{}
		res[h.Name] = rs
		last, running := srv.updates.LastRun()
		switch {
		case running:
			rs.Reason = "contrôle déjà en cours"
			continue
		case !last.IsZero() && time.Since(last) < 5*time.Minute:
			rs.Reason = "dernier contrôle il y a moins de 5 minutes"
			if d := 5*time.Minute - time.Since(last); wait == 0 || d < wait {
				wait = d
			}
			waiting++
			continue
		}
		refs := srv.imageRefs(r.Context())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			srv.updates.Check(ctx, refs)
			srv.events.Notify()
		}()
		slog.Info("update check started by API", "host", h.Name, "images", len(refs))
		rs.Started, rs.Images = true, len(refs)
		started, images = true, images+len(refs)
	}
	out := map[string]any{"started": started}
	if started {
		out["images"] = images
	}
	if len(s.hosts) > 1 {
		out["hosts"] = res
	} else if len(targets) == 1 && !started {
		out["reason"] = res[targets[0].Name].Reason
	}
	switch {
	case started:
		writeJSON(w, http.StatusAccepted, out)
	case len(targets) > 0 && waiting == len(targets):
		// Every host was checked less than 5 minutes ago.
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, out)
	case len(targets) == 0:
		jsonError(w, http.StatusServiceUnavailable, "aucun hôte joignable")
	default:
		writeJSON(w, http.StatusAccepted, out) // already running somewhere
	}
}
