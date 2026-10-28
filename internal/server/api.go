package server

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// apiKeyOK accepts "Authorization: Bearer <key>" or "X-Api-Key: <key>" when
// the key's SHA-256 is listed in the keys file. The file is read on each
// request, so a key is added or revoked without a restart.
func apiKeyOK(r *http.Request, keysFile string) bool {
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
		want, err := hex.DecodeString(strings.Fields(line)[0])
		if err == nil && len(want) == len(sum) && subtle.ConstantTimeCompare(want, sum[:]) == 1 {
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
	if !apiKeyOK(r, s.cfg.APIKeysFile) {
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
