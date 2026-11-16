package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neotoxicfr/dockerr/internal/config"
	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/store"
)

func TestPromEscape(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:     `plain`,
		`a"b`:       `a\"b`,
		`a\b`:       `a\\b`,
		"a\nb":      `a\nb`,
		`\"` + "\n": `\\\"\n`,
	} {
		if got := promEscape(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestRenderMetrics(t *testing.T) {
	d := metricsData{
		all: []*stacks.Container{
			{Name: "web-1", Project: "web", Service: "web", State: "running", CPU: 1.5, Mem: 1024},
			{Name: `we"ird`, Project: "w", Service: "s", State: "running", Mem: 2},
			{Name: "old", Project: "web", Service: "job", State: "exited", NetLost: true},
		},
		list: []*stacks.Stack{{Name: "web", State: stacks.Running, Updates: 2}, {Name: "w", State: stacks.Problem}},
		last: time.Unix(1700000000, 0),
		ops:  []store.OpCount{{Kind: "update", N: 3}, {Kind: "update", Failed: true, N: 1}},
	}
	out := renderMetrics(d)
	if again := renderMetrics(d); again != out {
		t.Error("output is not stable")
	}
	for _, want := range []string{
		"# HELP dockerr_up ", "# TYPE dockerr_up gauge\ndockerr_up 1\n",
		`dockerr_stacks{state="running"} 1`, `dockerr_stacks{state="problem"} 1`, `dockerr_stacks{state="stopped"} 0`,
		`dockerr_containers{state="running"} 2`, `dockerr_containers{state="exited"} 1`,
		`dockerr_container_cpu_percent{stack="web",service="web",container="web-1"} 1.50`,
		`dockerr_container_memory_bytes{stack="w",service="s",container="we\"ird"} 2`,
		"dockerr_updates_available 2\n", "dockerr_update_last_check_timestamp_seconds 1700000000\n", "dockerr_netdeps_lost 1\n",
		`dockerr_operations_total{kind="update",result="error"} 1`, `dockerr_operations_total{kind="update",result="ok"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `container="old"`) {
		t.Error("stopped container has resource samples")
	}
	if strings.Count(out, "# TYPE dockerr_stacks ") != 1 {
		t.Error("TYPE repeated")
	}
	// Stable order: samples of a family are sorted.
	if strings.Index(out, `state="problem"`) > strings.Index(out, `state="running"`) {
		t.Error("samples not sorted")
	}
}

func TestMetricsAuth(t *testing.T) {
	hash := func(k string) string { s := sha256.Sum256([]byte(k)); return hex.EncodeToString(s[:]) }
	mk := "met-0123456789abcdef0123456789abcdef" // gitleaks:allow (fake test key)
	sk := "sta-0123456789abcdef0123456789abcdef" // gitleaks:allow (fake test key)
	f := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(f, []byte(hash(mk)+" scope=metrics\n"+hash(sk)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, file, header, value string
	}{
		{"no key", f, "", ""},
		{"status key", f, "Authorization", "Bearer " + sk},
		{"wrong key", f, "X-Api-Key", mk + "x"},
		{"no keys file", "", "X-Api-Key", mk},
	}
	for _, c := range cases {
		s := &Server{cfg: &config.Config{APIKeysFile: c.file}}
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if c.header != "" {
			r.Header.Set(c.header, c.value)
		}
		w := httptest.NewRecorder()
		s.metrics(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", c.name, w.Code)
		}
	}
	if !apiKeyOK(func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		r.Header.Set("Authorization", "Bearer "+mk)
		return r
	}(), f, "metrics") {
		t.Error("metrics key refused")
	}
}
