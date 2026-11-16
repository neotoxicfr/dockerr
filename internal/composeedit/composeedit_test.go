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

func TestMoreSettingsModel(t *testing.T) {
	src := `services:
  app:
    image: nginx:1.27
    container_name: web
    privileged: true
    cpus: 0.5
    cap_add: [NET_ADMIN]
    extra_hosts:
      gw: 192.0.2.1
    logging:
      driver: json-file
      options:
        max-size: 10m
    healthcheck:
      test: ["CMD-SHELL", "curl -f http://localhost || exit 1"]
      retries: 3
  exec:
    image: busybox:1.37
    cap_drop: [&c ALL]
    healthcheck:
      test: ["CMD", "true"]
`
	d, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	svc := map[string]Service{}
	for _, s := range d.Model().Services {
		svc[s.Name] = s
	}
	app, exec := svc["app"], svc["exec"]
	cases := []struct {
		name string
		ok   bool
	}{
		{"scalars", app.Scalars["container_name"] == "web" && app.Scalars["privileged"] == "true" && app.Scalars["cpus"] == "0.5"},
		{"list", len(app.Lists["cap_add"]) == 1 && app.Lists["cap_add"][0].Value == "NET_ADMIN"},
		{"logging", app.Logging.Driver == "json-file" && len(app.Logging.Options) == 1 && app.Logging.Options[0].Value == "10m"},
		{"healthcheck CMD-SHELL", app.Healthcheck.Test == "curl -f http://localhost || exit 1" && app.Healthcheck.Retries == "3"},
		{"extra_hosts mapping locked", slices.Contains(app.Locked, "extra_hosts")},
		{"healthcheck CMD-SHELL editable", !slices.Contains(app.Locked, "healthcheck")},
		{"healthcheck exec form locked", slices.Contains(exec.Locked, "healthcheck")},
		{"anchored list item locked", slices.Contains(exec.Locked, "cap_drop")},
		{"nothing left for text mode", len(app.Other) == 0},
	}
	for _, c := range cases {
		if !c.ok {
			t.Errorf("%s: app=%+v exec=%+v", c.name, app, exec)
		}
	}
}

func TestMoreSettingsEdits(t *testing.T) {
	src := "services:\n  app:\n    image: nginx:1.27\n    privileged: true\n    healthcheck:\n      test: [\"CMD-SHELL\", \"true\"] # local check\n    cap_drop: [&c ALL]\n"
	cases := []struct {
		name    string
		edit    Edit
		want    string // in the output
		gone    string // not in the output
		wantErr bool
	}{
		{"bool stays a bool", Edit{Op: "scalar", Path: []string{"services", "app", "read_only"}, Value: json.RawMessage(`"true"`)}, "read_only: true\n", "", false},
		{"empty removes", Edit{Op: "scalar", Path: []string{"services", "app", "privileged"}, Value: json.RawMessage(`""`)}, "", "privileged", false},
		{"nested set", Edit{Op: "set", Path: []string{"services", "app", "logging", "driver"}, Value: json.RawMessage(`"local"`)}, "driver: local", "", false},
		{"nested map", Edit{Op: "map", Path: []string{"services", "app", "logging", "options"}, Value: json.RawMessage(`[{"key":"max-file","value":"3"}]`)}, `max-file: "3"`, "", false},
		{"list", Edit{Op: "list", Path: []string{"services", "app", "dns"}, Value: json.RawMessage(`["192.0.2.53"]`)}, "- 192.0.2.53", "", false},
		{"replacing a sequence keeps its comment", Edit{Op: "set", Path: []string{"services", "app", "healthcheck", "test"}, Value: json.RawMessage(`"curl -f http://localhost"`)}, "# local check", "", false},
		{"anchored list refused", Edit{Op: "list", Path: []string{"services", "app", "cap_drop"}, Value: json.RawMessage(`["NET_RAW"]`)}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			err = d.Apply([]Edit{c.edit})
			if c.wantErr {
				if !errors.Is(err, ErrLocked) {
					t.Fatalf("err = %v, want ErrLocked", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := d.String()
			if c.want != "" && !strings.Contains(out, c.want) {
				t.Errorf("missing %q in:\n%s", c.want, out)
			}
			if c.gone != "" && strings.Contains(out, c.gone) {
				t.Errorf("%q still in:\n%s", c.gone, out)
			}
		})
	}
}
