package updates

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTags(t *testing.T) {
	cases := []struct {
		name  string
		token string // what the token service hands out
		next  func(base string) string
		want  string // tags joined, or "error"
	}{
		{"relative next link", "tk", func(string) string { return `</v2/team/app/tags/list?last=1.1&n=1000>; rel="next"` }, "1.0,1.1,1.2"},
		{"absolute link, same registry", "tk", func(b string) string { return `<` + b + `/v2/team/app/tags/list?last=1.1>; rel="next"` }, "1.0,1.1,1.2"},
		{"link to another host not followed", "tk", func(string) string { return `<https://other.example/v2/team/app/tags/list?last=1.1>; rel="next"` }, "1.0,1.1"},
		{"empty token is an error", "", func(string) string { return "" }, "error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/token":
					_ = json.NewEncoder(w).Encode(map[string]any{"token": c.token, "expires_in": 300})
				case r.Header.Get("Authorization") != "Bearer tk":
					w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg"`)
					w.WriteHeader(http.StatusUnauthorized)
				case r.URL.Query().Get("last") == "":
					w.Header().Set("Link", c.next(srv.URL))
					_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.0", "1.1"}})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.2"}})
				}
			}))
			defer srv.Close()
			reg := newRegistry(nil)
			reg.http.Transport = srv.Client().Transport
			tags, err := reg.Tags(t.Context(), strings.TrimPrefix(srv.URL, "https://")+"/team/app:1.0")
			got := strings.Join(tags, ",")
			if err != nil {
				got = "error"
			}
			if got != c.want {
				t.Fatalf("tags = %q (err %v), want %q", got, err, c.want)
			}
		})
	}
}

func TestRedirectToHTTPRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/v2/team/app/tags/list", http.StatusFound)
	}))
	defer srv.Close()
	reg := newRegistry(func(string) (string, string) { return "u", "secret" })
	reg.http.Transport = srv.Client().Transport
	if _, err := reg.Tags(t.Context(), strings.TrimPrefix(srv.URL, "https://")+"/team/app:1.0"); err == nil || !strings.Contains(err.Error(), "non HTTPS") {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
}

func TestNextPage(t *testing.T) {
	cases := map[string]string{
		`</v2/a/tags/list?last=x&n=10>; rel="next"`:     "https://r/v2/a/tags/list?last=x&n=10",
		`<https://r/v2/a/tags/list?last=y>; rel="next"`: "https://r/v2/a/tags/list?last=y",
		``: "",
		`<https://evil.example/v2/a/tags/list>; rel="next"`: "",
		`<http://r/v2/a/tags/list>; rel="next"`:             "",
		`</v2/a/tags/list?last=x>; rel="prev"`:              "",
	}
	for link, want := range cases {
		if got := nextPage(link, "https://r"); got != want {
			t.Errorf("nextPage(%q) = %q, want %q", link, got, want)
		}
	}
}
