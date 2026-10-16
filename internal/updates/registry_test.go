package updates

import "testing"

func TestParseChallenge(t *testing.T) {
	s, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if s != "Bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/nginx:pull" {
		t.Fatalf("got %q %v", s, p)
	}
	s, p = parseChallenge(`Bearer realm="https://ghcr.io/token",service=ghcr.io`)
	if s != "Bearer" || p["service"] != "ghcr.io" {
		t.Fatalf("got %q %v", s, p)
	}
}

func TestCheckable(t *testing.T) {
	for ref, want := range map[string]bool{
		"nginx":                    true,
		"ghcr.io/a/b:1.2":          true,
		"sha256:abcd":              false,
		"nginx@sha256:abcd":        false,
		"nginx:1.27@sha256:abcdef": false,
		"":                         false,
	} {
		if got := Checkable(ref); got != want {
			t.Errorf("Checkable(%q) = %v, want %v", ref, got, want)
		}
	}
}
