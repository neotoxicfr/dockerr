<div align="center">

<img src="web/static/icons/icon.svg" width="88" height="88" alt="">

# dockerr

**Your compose stacks, one tap away.**

A small, fast web manager for Docker compose: the daily work in plain sight,
everything else one switch away. One Go binary, installable as an app on your phone.

[![CI](https://github.com/neotoxicfr/dockerr/actions/workflows/docker-publish.yml/badge.svg?branch=dev)](https://github.com/neotoxicfr/dockerr/actions/workflows/docker-publish.yml)
[![Release](https://img.shields.io/github/v/release/neotoxicfr/dockerr?sort=semver)](https://github.com/neotoxicfr/dockerr/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

---

## Why

Most self-hosted servers are a folder of compose files. Managing them should
take a glance and a tap, not a tour of every Docker object. dockerr opens on
your stacks: which run, which struggle, which have a new image waiting. Start,
stop, update or edit them from there. Containers, images, networks, volumes and
a terminal stay one switch away, in the advanced mode.

It also handles what generic managers get wrong: **containers that share
another container's network**, like apps routed through a VPN container.

## Features

### Every day

- **Stacks at a glance**: state, services up, CPU and memory, pending image
  updates. Problems sort first; search with <kbd>/</kbd>.
- **One-tap operations** on a stack or a single service: start, stop, restart,
  update (pull + up), recreate, with live progress.
- **Compose editor**: YAML and `.env`, checked as you type with the faulty line
  marked, a diff before saving, then *save and apply*.
- **Update checks** straight against the registries with `HEAD` requests: no
  Docker Hub pull quota spent, private registries through the usual
  `config.json`. A pulled image that a container does not run yet counts too.
- **New stack** from a pasted compose file.
- **Logs** stay with [Dozzle](https://dozzle.dev): set `DOCKERR_DOZZLE_URL` for
  a link on every container.

### Shared network namespaces, done right

`network_mode: service:vpn` and `container:vpn` put a container inside another
one's network, typically to route it through a VPN container such as gluetun.
Docker resolves that parent once, at start, so:

- when the parent **restarts**, its dependents keep a dead namespace and lose
  the network;
- when the parent is **recreated**, its ID changes and dependents point at a
  container that no longer exists;
- `docker compose restart` restarts parent and dependents **in parallel**, so
  dependents try to join a namespace that is down and fail.

dockerr restarts in dependency order, spots any container left on a dead
namespace and repairs it after every operation, or on demand: a restart when
the reference still resolves, a recreate when it does not. The network map
shows who rides on whom.

### Advanced mode

- Containers outside compose, images, networks, volumes, with cleanup.
- Container details: command, restart policy, IPs, ports, mounts, variables
  (blurred until clicked), labels, raw inspect.
- History of every saved compose and `.env`, with diff and one-click restore.
- Container terminal, off by default, every session logged.

### Everywhere

- Light and dark themes, built for phone and desktop alike.
- Installable as a PWA; live refresh from Docker events.
- No external requests from the browser: fonts, icons and editors are bundled.

## Quick start

dockerr is meant to sit behind a forward-auth proxy and to reach Docker through
a socket proxy. [`deploy/docker-compose.yml`](deploy/docker-compose.yml) sets up
both with Traefik and Authelia:

```yaml
services:
  dockerr-socket:
    image: tecnativa/docker-socket-proxy:latest
    environment:
      CONTAINERS: 1
      IMAGES: 1
      NETWORKS: 1
      VOLUMES: 1
      POST: 1
      ALLOW_START: 1
      ALLOW_STOP: 1
      ALLOW_RESTARTS: 1
      INFO: 1
      EVENTS: 1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks: [dockerr-socket]

  dockerr:
    image: ghcr.io/neotoxicfr/dockerr:latest
    user: "0:0"                       # to write root-owned compose files
    environment:
      DOCKER_HOST: tcp://dockerr-socket:2375
      DOCKERR_STACKS_DIR: /srv/stacks
      DOCKERR_TRUSTED_PROXIES: traefik
    volumes:
      - /srv/stacks:/srv/stacks       # same path as on the host
      - ./data:/data
    networks: [proxy, dockerr-socket]
    # + Traefik labels: strip Remote-* headers, then forward auth

networks:
  dockerr-socket: { internal: true }
  proxy: { external: true }
```

Two rules matter:

1. **Mount the stacks folder at the same path as on the host**, so relative bind
   mounts in your compose files point to the same place.
2. **Never let your auth provider bypass dockerr's domain**, and strip
   `Remote-*` headers from client requests (the example does it). See
   [SECURITY.md](SECURITY.md).

Each stack is a folder under `DOCKERR_STACKS_DIR` holding `compose.yaml`,
`compose.yml`, `docker-compose.yaml` or `docker-compose.yml`, plus an optional
`.env`, as with `docker compose`.

## Configuration

| Variable | Default | |
|---|---|---|
| `DOCKER_HOST` | local socket | e.g. `tcp://dockerr-socket:2375` |
| `DOCKERR_STACKS_DIR` | `/mnt/docker` | one folder per stack |
| `DOCKERR_TRUSTED_PROXIES` | — **required** | IPs, CIDRs or host names allowed to set the user header |
| `DOCKERR_AUTH_HEADER` | `Remote-User` | |
| `DOCKERR_REQUIRE_GROUP` | — | group required in `DOCKERR_GROUPS_HEADER` |
| `DOCKERR_GROUPS_HEADER` | `Remote-Groups` | |
| `DOCKERR_DOZZLE_URL` | — | adds a logs link per container |
| `DOCKERR_PORT_HOST` | request host | host used in links to published ports |
| `DOCKERR_EXEC` | `false` | container terminal (also needs `EXEC=1` on the proxy) |
| `DOCKERR_UPDATE_INTERVAL` | `6h` | image update checks |
| `DOCKERR_REPAIR_NETDEPS` | `true` | repair lost network namespaces after operations |
| `DOCKERR_DATA_DIR` | `/data` | SQLite database: history, preferences, terminal log |
| `DOCKERR_LISTEN` | `:8080` | |
| `DOCKERR_INSECURE_NOAUTH` | `false` | no authentication, local development only |

### Socket proxy rights

dockerr probes the proxy at start and greys out what it cannot do, naming the
setting to add.

| Proxy setting | Used for |
|---|---|
| `CONTAINERS`, `INFO`, `EVENTS` | listing, live refresh |
| `IMAGES`, `NETWORKS`, `VOLUMES`, `POST` | compose up, pull, down, cleanup |
| `ALLOW_START`, `ALLOW_STOP`, `ALLOW_RESTARTS` | start, stop, restart, network repair |
| `EXEC` | terminal |

## Development

```sh
make dev      # local socket, no auth, http://127.0.0.1:8080
make check    # build, race tests, lint, vuln
make vendor   # rebuild the CodeMirror and xterm bundles (Node)
```

Go 1.27 and the Docker compose SDK (`github.com/docker/compose/v5`), embedded:
no `docker` CLI in the image. Server-rendered HTML with a little vanilla
JavaScript. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
