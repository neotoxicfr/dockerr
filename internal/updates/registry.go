package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
)

// manifestAccept lists the manifest types a registry may answer with; the
// digest it returns for a multi-arch tag is the index digest, which is what
// the daemon stores in RepoDigests after a pull.
var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Credentials returns basic auth for a registry host, if any.
type Credentials func(host string) (user, pass string)

// registry fetches remote digests with HEAD requests: they do not count as
// pulls against Docker Hub's rate limit.
type registry struct {
	http  *http.Client
	creds Credentials

	mu     sync.Mutex
	tokens map[string]token // realm+scope → token
}

type token struct {
	value   string
	expires time.Time
}

func newRegistry(creds Credentials) *registry {
	return &registry{http: &http.Client{Timeout: 20 * time.Second}, creds: creds, tokens: map[string]token{}}
}

// Digest returns the digest the registry serves for ref (e.g. "nginx:1.27").
func (r *registry) Digest(ctx context.Context, ref string) (repo, digest string, err error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", err
	}
	named = reference.TagNameOnly(named)
	tagged, ok := named.(reference.Tagged)
	if !ok {
		return "", "", errors.New("référence sans tag")
	}
	host := reference.Domain(named)
	path := reference.Path(named)
	apiHost := host
	if host == "docker.io" {
		apiHost = "registry-1.docker.io"
	}
	u := "https://" + apiHost + "/v2/" + path + "/manifests/" + tagged.Tag()
	scope := "repository:" + path + ":pull"

	status, hdr, err := r.head(ctx, u, host, "")
	if err != nil {
		return "", "", err
	}
	if status == http.StatusUnauthorized {
		tok, terr := r.token(ctx, hdr.Get("WWW-Authenticate"), host, scope)
		if terr != nil {
			return "", "", terr
		}
		if status, hdr, err = r.head(ctx, u, host, tok); err != nil {
			return "", "", err
		}
	}
	if status != http.StatusOK {
		return "", "", fmt.Errorf("%s : HTTP %d", host, status)
	}
	d := hdr.Get("Docker-Content-Digest")
	if d == "" {
		return "", "", fmt.Errorf("%s : pas de digest dans la réponse", host)
	}
	return reference.FamiliarName(named), d, nil
}

func (r *registry) head(ctx context.Context, u, host, tok string) (int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	switch {
	case tok != "":
		req.Header.Set("Authorization", "Bearer "+tok)
	case r.creds != nil:
		if user, pass := r.creds(host); user != "" {
			req.SetBasicAuth(user, pass)
		}
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header, nil
}

// token runs the Bearer challenge of the distribution spec.
func (r *registry) token(ctx context.Context, challenge, host, scope string) (string, error) {
	scheme, params := parseChallenge(challenge)
	if !strings.EqualFold(scheme, "bearer") || params["realm"] == "" {
		return "", fmt.Errorf("%s : authentification %q non gérée", host, scheme)
	}
	if params["scope"] != "" {
		scope = params["scope"]
	}
	key := params["realm"] + "|" + params["service"] + "|" + scope
	r.mu.Lock()
	if t, ok := r.tokens[key]; ok && time.Now().Before(t.expires) {
		r.mu.Unlock()
		return t.value, nil
	}
	r.mu.Unlock()

	q := url.Values{}
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if r.creds != nil {
		if user, pass := r.creds(host); user != "" {
			req.SetBasicAuth(user, pass)
		}
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s : jeton refusé (HTTP %d)", host, resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	t := body.Token
	if t == "" {
		t = body.AccessToken
	}
	ttl := time.Duration(body.ExpiresIn) * time.Second
	if ttl < time.Minute {
		ttl = time.Minute
	}
	r.mu.Lock()
	r.tokens[key] = token{value: t, expires: time.Now().Add(ttl - 10*time.Second)}
	r.mu.Unlock()
	return t, nil
}

// parseChallenge splits `Bearer realm="…",service="…"`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				params[k] = v[1:]
				break
			}
			params[k] = v[1 : end+1]
			rest = v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			params[k] = strings.TrimSpace(val)
			rest = after
		}
	}
	return scheme, params
}

// maxTagPages bounds the tag listing: an image with tens of thousands of
// tags (nightlies) is not worth more requests.
const maxTagPages = 20

// Tags lists a repository's tags, following the registry's pagination.
func (r *registry) Tags(ctx context.Context, ref string) ([]string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, err
	}
	host, path := reference.Domain(named), reference.Path(named)
	apiHost := host
	if host == "docker.io" {
		apiHost = "registry-1.docker.io"
	}
	base := "https://" + apiHost
	next := base + "/v2/" + path + "/tags/list?n=1000"
	scope := "repository:" + path + ":pull"
	var tok string
	var tags []string
	for page := 0; next != "" && page < maxTagPages; page++ {
		resp, err := r.get(ctx, next, host, tok)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && tok == "" {
			resp.Body.Close()
			if tok, err = r.token(ctx, resp.Header.Get("WWW-Authenticate"), host, scope); err != nil {
				return nil, err
			}
			page--
			continue
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s : liste des tags, HTTP %d", host, resp.StatusCode)
		}
		if err != nil {
			return nil, fmt.Errorf("%s : liste des tags illisible", host)
		}
		tags = append(tags, body.Tags...)
		next = nextPage(resp.Header.Get("Link"), base)
	}
	return tags, nil
}

func (r *registry) get(ctx context.Context, u, host, tok string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case tok != "":
		req.Header.Set("Authorization", "Bearer "+tok)
	case r.creds != nil:
		if user, pass := r.creds(host); user != "" {
			req.SetBasicAuth(user, pass)
		}
	}
	return r.http.Do(req)
}

// nextPage reads `</v2/…/tags/list?last=x&n=1000>; rel="next"`; a link to
// another host is not followed.
func nextPage(link, base string) string {
	l, rel, ok := strings.Cut(link, ";")
	if !ok || !strings.Contains(rel, `rel="next"`) {
		return ""
	}
	l = strings.Trim(strings.TrimSpace(l), "<>")
	if !strings.HasPrefix(l, "/v2/") {
		return ""
	}
	return base + l
}
