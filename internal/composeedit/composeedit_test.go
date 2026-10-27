package composeedit

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

const sample = `# Ma pile
services:
  web: # le site
    image: nginx:1.27 # version figée
    restart: unless-stopped
    ports:
      - "8080:80"
      - target: 443
        published: 8443
    environment:
      TZ: Europe/Paris
      DB_PASSWORD: secret
  worker:
    image: busybox
    environment:
      - A=1
      - B=2
    depends_on:
      db:
        condition: service_healthy
  x:
    <<: *defaults
`

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func apply(t *testing.T, src string, edits ...Edit) (string, error) {
	t.Helper()
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(edits); err != nil {
		return "", err
	}
	return d.String(), nil
}

const anchored = `x-defaults: &defaults
  restart: always
services:
  a:
    <<: *defaults
    image: alpine
  b:
    image: alpine
    environment: &env
      K: v
  c:
    image: alpine
    environment: *env
`

func TestModel(t *testing.T) {
	d, err := Parse(anchored)
	if err != nil {
		t.Fatal(err)
	}
	m := d.Model()
	if len(m.Services) != 3 || !slices.Equal(m.Other, []string{"x-defaults"}) {
		t.Fatalf("model: %+v", m)
	}
	if len(m.Services[0].Locked) != len(serviceFields) {
		t.Errorf("merge-key service should be fully locked: %v", m.Services[0].Locked)
	}
	if !slices.Contains(m.Services[1].Locked, "environment") || !slices.Contains(m.Services[2].Locked, "environment") {
		t.Errorf("anchor and alias must lock environment: %v / %v", m.Services[1].Locked, m.Services[2].Locked)
	}
	if m.Services[2].Environment[0] != (KV{"K", "v"}) {
		t.Errorf("alias not resolved for display: %+v", m.Services[2].Environment)
	}
}

func TestModelLongSyntax(t *testing.T) {
	d, err := Parse(strings.Replace(sample, "    <<: *defaults\n", "    image: x\n", 1))
	if err != nil {
		t.Fatal(err)
	}
	web := d.Model().Services[0]
	if len(web.Ports) != 2 || web.Ports[0].Long || !web.Ports[1].Long {
		t.Fatalf("ports: %+v", web.Ports)
	}
	if web.Environment[1] != (KV{"DB_PASSWORD", "secret"}) {
		t.Errorf("env: %+v", web.Environment)
	}
	worker := d.Model().Services[1]
	if !slices.Equal(worker.DependsOn, []string{"db"}) || !slices.Contains(worker.Locked, "depends_on") {
		t.Errorf("depends_on with conditions must be shown and locked: %+v", worker)
	}
	// Replacing a list holding long-syntax entries would lose them.
	_, err = apply(t, strings.Replace(sample, "    <<: *defaults\n", "    image: x\n", 1),
		Edit{Op: "list", Path: []string{"services", "web", "ports"}, Value: raw([]string{"80:80"})})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

func TestEditKeepsComments(t *testing.T) {
	src := strings.Replace(sample, "    <<: *defaults\n", "    image: x\n", 1)
	out, err := apply(t, src,
		Edit{Op: "set", Path: []string{"services", "web", "image"}, Value: raw("nginx:1.29")},
		Edit{Op: "map", Path: []string{"services", "worker", "environment"}, Value: raw([]KV{{"A", "1"}, {"C", "3"}})},
		Edit{Op: "list", Path: []string{"services", "worker", "ports"}, Value: raw([]string{"22:22"})},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Ma pile", "web: # le site", "image: nginx:1.29 # version figée", "- A=1", "- C=3", `- "22:22"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "B=2") {
		t.Errorf("B not removed:\n%s", out)
	}
}

func TestEditRefusesAlias(t *testing.T) {
	for _, e := range []Edit{
		{Op: "set", Path: []string{"services", "a", "image"}, Value: raw("x")},          // merge key
		{Op: "map", Path: []string{"services", "b", "environment"}, Value: raw([]KV{})}, // anchor
		{Op: "map", Path: []string{"services", "c", "environment"}, Value: raw([]KV{})}, // alias
	} {
		if _, err := apply(t, anchored, e); !errors.Is(err, ErrLocked) {
			t.Errorf("%v: err = %v, want ErrLocked", e.Path, err)
		}
	}
}

func TestAddRemoveService(t *testing.T) {
	out, err := apply(t, "services:\n  a:\n    image: x\n",
		Edit{Op: "set", Path: []string{"services", "b", "image"}, Value: raw("redis:7")},
		Edit{Op: "set", Path: []string{"secrets", "db", "file"}, Value: raw("./secrets/db")},
		Edit{Op: "list", Path: []string{"services", "b", "secrets"}, Value: raw([]string{"db"})},
		Edit{Op: "remove", Path: []string{"services", "a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "services:\n  b:\n    image: redis:7\n    secrets:\n      - db\nsecrets:\n  db:\n    file: ./secrets/db\n"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	// An empty value removes the key.
	out, _ = apply(t, out, Edit{Op: "list", Path: []string{"services", "b", "secrets"}, Value: raw([]string{})})
	if strings.Contains(out, "- db") {
		t.Errorf("secrets list not removed:\n%s", out)
	}
}

func TestEmptyAndInvalid(t *testing.T) {
	out, err := apply(t, "", Edit{Op: "set", Path: []string{"services", "a", "image"}, Value: raw("x")})
	if err != nil || out != "services:\n  a:\n    image: x\n" {
		t.Fatalf("from empty: %q %v", out, err)
	}
	if _, err := Parse("- a\n- b\n"); err == nil {
		t.Error("top-level list accepted")
	}
	if _, err := apply(t, "services: {}\n", Edit{Op: "set", Path: []string{"services", "<<"}, Value: raw("x")}); err == nil {
		t.Error("merge key path accepted")
	}
	if _, err := apply(t, "services: {}\n", Edit{Op: "nope", Path: []string{"x"}}); err == nil {
		t.Error("unknown op accepted")
	}
}

func TestModelNoNull(t *testing.T) {
	d, _ := Parse("services:\n  a:\n    image: x\n")
	b, _ := json.Marshal(d.Model())
	if strings.Contains(string(b), "null") {
		t.Fatalf("null in model: %s", b)
	}
}
