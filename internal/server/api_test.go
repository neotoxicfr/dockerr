package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
)

func TestAPIKey(t *testing.T) {
	key := "k3y-0123456789abcdef0123456789abcdef" // gitleaks:allow (fake test key)
	sum := sha256.Sum256([]byte(key))
	f := filepath.Join(t.TempDir(), "keys")
	checkKey := "chk-0123456789abcdef0123456789abcdef" // gitleaks:allow (fake test key)
	csum := sha256.Sum256([]byte(checkKey))
	if err := os.WriteFile(f, []byte("# nehome\n"+hex.EncodeToString(sum[:])+" nehome\n"+hex.EncodeToString(csum[:])+" scope=check nehome\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := func(h, v string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		if h != "" {
			r.Header.Set(h, v)
		}
		return r
	}
	cases := []struct {
		name  string
		r     *http.Request
		file  string
		scope string
		want  bool
	}{
		{"bearer", req("Authorization", "Bearer "+key), f, "status", true},
		{"x-api-key", req("X-Api-Key", key), f, "status", true},
		{"wrong key", req("X-Api-Key", key+"x"), f, "status", false},
		{"short key", req("X-Api-Key", "abc"), f, "status", false},
		{"no key", req("", ""), f, "status", false},
		{"endpoint off", req("X-Api-Key", key), "", "status", false},
		{"missing file", req("X-Api-Key", key), f + ".none", "status", false},
		{"status key cannot check", req("X-Api-Key", key), f, "check", false},
		{"check key checks", req("X-Api-Key", checkKey), f, "check", true},
		{"check key cannot read status", req("X-Api-Key", checkKey), f, "status", false},
	}
	for _, c := range cases {
		if got := apiKeyOK(c.r, c.file, c.scope); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildStatus(t *testing.T) {
	all := []*stacks.Container{
		{Name: "a", State: "running"}, {Name: "b", State: "exited"}, {Name: "c", State: "running", NetLost: true},
	}
	list := []*stacks.Stack{
		{Name: "web", State: stacks.Running, Updates: 2},
		{Name: "db", State: stacks.Problem},
		{Name: "old", State: stacks.Stopped},
	}
	st := buildStatus(all, list, time.Now().Add(-90*time.Second), false)
	if st.Stacks.Total != 3 || st.Stacks.Problem != 1 || st.Containers.Running != 2 || st.NetLost != 1 {
		t.Fatalf("counts: %+v", st)
	}
	if st.Updates.Stacks != 1 || st.Updates.Services != 2 || st.Updates.Names[0] != "web" || st.Updates.LastCheckAge < 89 {
		t.Fatalf("updates: %+v", st.Updates)
	}
	b, _ := json.Marshal(st)
	for _, want := range []string{`"stacks":{"total":3,"running":1,"partial":0,"stopped":1,"problem":1}`, `"problems":["db"]`, `"network_lost":1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	empty, _ := json.Marshal(buildStatus(nil, nil, time.Time{}, false))
	if strings.Contains(string(empty), "null") || !strings.Contains(string(empty), `"last_check_age_s":-1`) {
		t.Errorf("empty status: %s", empty)
	}
}
