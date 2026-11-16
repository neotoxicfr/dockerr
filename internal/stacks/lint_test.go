package stacks

import (
	"context"
	"strings"
	"testing"

	"github.com/neotoxicfr/dockerr/internal/composeedit"
)

func TestLint(t *testing.T) {
	cases := []struct {
		name, compose, env string
		want               string // substring of a warning ("" = no lint warning)
		fixed              string // substring of the compose after its fix ("" = no fix)
		envAppend          string
	}{
		{"version", "version: \"3.8\"\nservices:\n  a:\n    image: nginx:1.27\n", "", "clé version est obsolète", "services:", ""},
		{"privileged", "services:\n  a:\n    image: nginx:1.27\n    privileged: true\n", "", "accès complet à l'hôte", "", ""},
		{"socket", "services:\n  a:\n    image: nginx:1.27\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n", "", "socket Docker monté", "", ""},
		{"socket proxy allowed", "services:\n  a:\n    image: tecnativa/docker-socket-proxy:0.3\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock:ro\n", "", "", "", ""},
		{"latest", "services:\n  a:\n    image: nginx:latest\n", "", "pas de version fixée", "", ""},
		{"no tag", "services:\n  a:\n    image: ghcr.io/org/app\n", "", "pas de version fixée", "", ""},
		{"registry port is not a tag", "services:\n  a:\n    image: registry.local:5000/app:2.1\n", "", "", "", ""},
		{"digest", "services:\n  a:\n    image: nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n", "", "", "", ""},
		{"db port", "services:\n  db:\n    image: postgres:17\n    ports:\n      - \"5432:5432\"\n      - \"8080:80\"\n", "", "ouvert sur toutes les interfaces", `"127.0.0.1:5432:5432"`, ""},
		{"db port bound", "services:\n  db:\n    image: postgres:17\n    ports:\n      - \"127.0.0.1:5432:5432\"\n", "", "", "", ""},
		{"secret", "services:\n  a:\n    image: nginx:1.27\n    environment:\n      DB_PASSWORD: hunter22\n      MODE: prod\n", "", "DB_PASSWORD est écrit en clair", "DB_PASSWORD: ${DB_PASSWORD}", "DB_PASSWORD=hunter22\n"},
		{"secret quoted", "services:\n  a:\n    image: nginx:1.27\n    environment:\n      - API_TOKEN=a b#c\n", "", "API_TOKEN est écrit en clair", "API_TOKEN=${API_TOKEN}", "API_TOKEN='a b#c'\n"},
		{"secret already in env", "services:\n  a:\n    image: nginx:1.27\n    environment:\n      DB_PASSWORD: hunter22\n", "DB_PASSWORD=other\n", "DB_PASSWORD est écrit en clair", "", ""},
		{"secret from variable", "services:\n  a:\n    image: nginx:1.27\n    environment:\n      DB_PASSWORD: ${DB_PASSWORD}\n", "DB_PASSWORD=x\n", "", "", ""},
		{"boolean is not a secret", "services:\n  a:\n    image: nginx:1.27\n    environment:\n      OAUTH_ENABLED: \"true\"\n", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Check(context.Background(), "demo", t.TempDir(), c.compose, c.env, Host{})
			if len(r.Issues) > 0 {
				t.Fatalf("issues: %+v", r.Issues)
			}
			var hit *Issue
			for i, w := range r.Warnings {
				if c.want != "" && strings.Contains(w.Message, c.want) {
					hit = &r.Warnings[i]
				}
			}
			if c.want == "" {
				for _, w := range r.Warnings {
					if !strings.Contains(w.Message, "n'existe pas") { // bind sources absent on the test host
						t.Errorf("unexpected warning: %s", w.Message)
					}
				}
				return
			}
			if hit == nil {
				t.Fatalf("warning %q missing: %+v", c.want, r.Warnings)
			}
			if hit.Line == 0 {
				t.Errorf("warning without line")
			}
			if c.fixed == "" {
				if hit.Fix != nil {
					t.Fatalf("unexpected fix: %+v", hit.Fix)
				}
				return
			}
			if hit.Fix == nil {
				t.Fatal("fix missing")
			}
			doc, err := composeedit.Parse(c.compose)
			if err != nil {
				t.Fatal(err)
			}
			if err := doc.Apply(hit.Fix.Edits); err != nil {
				t.Fatal(err)
			}
			out := doc.String()
			if !strings.Contains(out, c.fixed) {
				t.Errorf("fixed compose lacks %q:\n%s", c.fixed, out)
			}
			if hit.Fix.EnvAppend != c.envAppend {
				t.Errorf("env append = %q, want %q", hit.Fix.EnvAppend, c.envAppend)
			}
			// The fix resolves the warning.
			again := Check(context.Background(), "demo", t.TempDir(), out, c.env+hit.Fix.EnvAppend, Host{})
			for _, w := range again.Warnings {
				if strings.Contains(w.Message, c.want) {
					t.Errorf("still warned after fix: %s\n%s", w.Message, out)
				}
			}
		})
	}
}
