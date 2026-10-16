// Package docker wraps the Docker API client shared by every feature, and
// probes which parts of the API the endpoint (socket or proxy) lets through.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/moby/moby/client"
)

type Docker struct {
	API  client.APIClient
	CLI  command.Cli
	Caps Caps
	Host string
}

func New(ctx context.Context) (*Docker, error) {
	cli, err := command.NewDockerCli(command.WithCombinedStreams(io.Discard), command.WithInputStream(io.NopCloser(strings.NewReader(""))))
	if err != nil {
		return nil, err
	}
	if err := cli.Initialize(flags.NewClientOptions()); err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	d := &Docker{API: cli.Client(), CLI: cli, Host: cli.Client().DaemonHost()}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := d.API.Ping(pctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return nil, fmt.Errorf("docker unreachable at %s: %w", d.Host, err)
	}
	d.Caps = d.probe(pctx)
	slog.Info("docker", "host", d.Host, "caps", d.Caps.String())
	return d, nil
}

// Caps tells which API sections the endpoint allows. With a raw socket all
// are true; behind tecnativa/docker-socket-proxy, a section turned off
// answers 403 before reaching the daemon.
type Caps struct {
	Containers   bool // list and inspect containers
	Images       bool
	Networks     bool
	Volumes      bool
	Write        bool // create, remove, pull: POST=1 on the proxy
	Lifecycle    bool // start, stop, restart
	Exec         bool
	Distribution bool // remote digest lookup, for update checks
	Events       bool
	Info         bool
}

// Cap describes one capability for the settings page.
type Cap struct {
	Name, Env string
	OK        bool
}

func (c Caps) List() []Cap {
	return []Cap{
		{"Conteneurs", "CONTAINERS=1", c.Containers},
		{"Images", "IMAGES=1", c.Images},
		{"Réseaux", "NETWORKS=1", c.Networks},
		{"Volumes", "VOLUMES=1", c.Volumes},
		{"Écriture", "POST=1", c.Write},
		{"Démarrer, arrêter", "ALLOW_START=1 ALLOW_STOP=1 ALLOW_RESTARTS=1", c.Lifecycle},
		{"Terminal", "EXEC=1", c.Exec},
		{"Mises à jour", "DISTRIBUTION=1", c.Distribution},
		{"Événements", "EVENTS=1", c.Events},
		{"Infos système", "INFO=1", c.Info},
	}
}

// Compose reports whether up/down/pull can run: they create containers,
// networks and volumes, and pull images.
func (c Caps) Compose() bool {
	return c.Containers && c.Images && c.Networks && c.Volumes && c.Write
}

func (c Caps) String() string {
	var on []string
	for _, x := range c.List() {
		if x.OK {
			on = append(on, x.Env)
		}
	}
	return strings.Join(on, " ")
}

const probeName = "dockerr-probe-missing"

// probe sends requests that have no side effect whatever the answer: missing
// objects (404) or malformed bodies (400) when the daemon sees them, 403 when
// the proxy stops them.
func (d *Docker) probe(ctx context.Context) Caps {
	dial := d.API.(*client.Client).Dialer()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
	}}
	base := "http://docker"
	ver := "/v" + d.API.ClientVersion()
	allowed := func(method, path, body string) bool {
		req, err := http.NewRequestWithContext(ctx, method, base+ver+path, strings.NewReader(body))
		if err != nil {
			return false
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			slog.Warn("probe", "path", path, "err", err)
			return false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		return resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized
	}
	now := fmt.Sprint(time.Now().Unix())
	return Caps{
		Containers:   allowed("GET", "/containers/"+probeName+"/json", ""),
		Images:       allowed("GET", "/images/"+probeName+"/json", ""),
		Networks:     allowed("GET", "/networks/"+probeName, ""),
		Volumes:      allowed("GET", "/volumes/"+probeName, ""),
		Write:        allowed("POST", "/containers/create", "{") && allowed("POST", "/volumes/create", "{") && allowed("POST", "/networks/create", "{"),
		Lifecycle:    allowed("POST", "/containers/"+probeName+"/start", "") && allowed("POST", "/containers/"+probeName+"/stop", "") && allowed("POST", "/containers/"+probeName+"/restart", ""),
		Exec:         allowed("POST", "/containers/"+probeName+"/exec", "{"),
		Distribution: allowed("GET", "/distribution/INVALID/json", ""),
		Events:       allowed("GET", "/events?since="+now+"&until="+now, ""),
		Info:         allowed("GET", "/info", ""),
	}
}
