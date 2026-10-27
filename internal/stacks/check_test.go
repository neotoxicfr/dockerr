package stacks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	host := Host{
		Networks:   map[string]bool{"proxy": true},
		Volumes:    map[string]bool{},
		Containers: map[string]bool{"gluetun": true},
		Ports:      map[string]PortUser{"8080/tcp": {"traefik", "reverse-proxy"}, "9000/tcp": {"demo-web-1", "demo"}},
	}
	compose := `services:
  web:
    image: nginx:${TAG}
    environment:
      A: ${KNOWN}
      B: ${WITH_DEFAULT:-x}
      C: $$ESCAPED
    ports:
      - "8080:80"
      - "9000:90"
    volumes:
      - ./data:/data
      - ./missing:/m
    networks: [proxy, ext]
    secrets: [db, gone]
  vpn:
    image: busybox
    network_mode: container:nothere
networks:
  proxy:
    external: true
  ext:
    external: true
    name: other-net
volumes:
  keep:
    external: true
secrets:
  db:
    file: ./db.txt
  gone:
    file: ./gone.txt
`
	r := Check(context.Background(), "demo", dir, compose, "KNOWN=1\n", host)
	joined := func(is []Issue) string {
		var b strings.Builder
		for _, i := range is {
			b.WriteString(i.Message + "\n")
		}
		return b.String()
	}
	issues, warnings := joined(r.Issues), joined(r.Warnings)
	for _, want := range []string{"réseau externe other-net introuvable", "volume externe keep introuvable", "secret gone"} {
		if !strings.Contains(issues, want) {
			t.Errorf("issue %q missing:\n%s", want, issues)
		}
	}
	for _, want := range []string{"variable TAG non définie", "port 8080/tcp est déjà pris par traefik", "./missing n'existe pas", "conteneur nothere"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warning %q missing:\n%s", want, warnings)
		}
	}
	for _, unwanted := range []string{"KNOWN", "WITH_DEFAULT", "ESCAPED", "9000", "./data", "réseau externe proxy"} {
		if strings.Contains(issues+warnings, unwanted) {
			t.Errorf("unexpected %q in:\n%s%s", unwanted, issues, warnings)
		}
	}
	for _, w := range r.Warnings {
		if w.Line == 0 {
			t.Errorf("warning without line: %q", w.Message)
		}
	}
	// A syntax error stops at the issue.
	r = Check(context.Background(), "demo", dir, "services:\n  a:\n   b: [\n", "", host)
	if len(r.Issues) != 1 || len(r.Warnings) != 0 {
		t.Fatalf("syntax error report: %+v", r)
	}
}

func TestExpandRange(t *testing.T) {
	if got := expandRange("8000-8002"); strings.Join(got, ",") != "8000,8001,8002" {
		t.Fatalf("got %v", got)
	}
	if got := expandRange("80"); len(got) != 1 || got[0] != "80" {
		t.Fatalf("got %v", got)
	}
}
