package updates

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTags(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tk", "expires_in": 300})
		case r.Header.Get("Authorization") != "Bearer tk":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/team/app/tags/list?last=1.1&n=1000>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.0", "1.1"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.2"}})
		}
	}))
	defer srv.Close()
	reg := newRegistry(nil)
	reg.http = srv.Client()
	host := strings.TrimPrefix(srv.URL, "https://")
	tags, err := reg.Tags(t.Context(), host+"/team/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "1.0,1.1,1.2" {
		t.Fatalf("tags = %v", tags)
	}
}

func TestNextPage(t *testing.T) {
	cases := map[string]string{
		`</v2/a/tags/list?last=x&n=10>; rel="next"`: "https://r/v2/a/tags/list?last=x&n=10",
		``: "",
		`<https://evil.example/v2/a/tags/list>; rel="next"`: "",
		`</v2/a/tags/list?last=x>; rel="prev"`:              "",
	}
	for link, want := range cases {
		if got := nextPage(link, "https://r"); got != want {
			t.Errorf("nextPage(%q) = %q, want %q", link, got, want)
		}
	}
}
