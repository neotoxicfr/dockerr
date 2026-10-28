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
// "<sha256 hex> [scope=status|check] [comment]"; without scope= it is a
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
	Stacks     stackCounts   `json:"stacks"`
	Containers ctrCounts     `json:"containers"`
	Updates    updatesStatus `json:"updates"`
	Problems   []string      `json:"problems"`
	NetLost    int           `json:"network_lost"`
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

var statusCache struct {
	sync.Mutex
	at  time.Time
	val statusJSON
}

// apiStatus serves a summary for dashboards, read-only, cached 15 s.
func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	if !apiKeyOK(r, s.cfg.APIKeysFile, "status") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dockerr"`)
		jsonError(w, http.StatusUnauthorized, "clé d'API absente ou invalide")
		return
	}
	statusCache.Lock()
	defer statusCache.Unlock()
	if time.Since(statusCache.at) > 15*time.Second {
		all, list, err := s.snapshot(r.Context())
		if err != nil {
			jsonError(w, http.StatusBadGateway, "Docker injoignable")
			return
		}
		last, checking := s.updates.LastRun()
		statusCache.val, statusCache.at = buildStatus(all, list, last, checking), time.Now()
	}
	writeJSON(w, http.StatusOK, statusCache.val)
}

// apiCheck starts an image update check: HEAD requests to the registries,
// no pull, nothing restarted. Idempotent, and at most once every 5 minutes.
func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	if !apiKeyOK(r, s.cfg.APIKeysFile, "check") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dockerr"`)
		jsonError(w, http.StatusUnauthorized, "clé d'API absente ou invalide")
		return
	}
	last, running := s.updates.LastRun()
	switch {
	case running:
		writeJSON(w, http.StatusAccepted, map[string]any{"started": false, "reason": "contrôle déjà en cours"})
		return
	case !last.IsZero() && time.Since(last) < 5*time.Minute:
		w.Header().Set("Retry-After", strconv.Itoa(int((5*time.Minute-time.Since(last)).Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"started": false, "reason": "dernier contrôle il y a moins de 5 minutes"})
		return
	}
	refs := s.imageRefs(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.updates.Check(ctx, refs)
		s.events.Notify()
	}()
	slog.Info("update check started by API", "images", len(refs))
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "images": len(refs)})
}
