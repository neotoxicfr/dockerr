<div align="center">

<img src="web/static/icons/icon.svg" width="88" height="88" alt="">

# dockerr

**A small, fast web manager for Docker compose stacks.**

</div>

dockerr does the daily work in one tap — see every stack, start, stop, update,
edit its compose file — and keeps the rest (containers, images, networks,
volumes, a terminal) behind an advanced mode. One Go binary, installable as a PWA.

## What it does

- **Stacks at a glance**: state, services up, CPU and memory, images with a newer
  version on their registry. Problems sort first.
- **One-tap operations**: start (`up -d`), stop, restart, update (pull + up),
  recreate, per stack or per service, with live progress.
- **Compose editor**: YAML editor, validation as you type with the faulty line
  marked, a diff before saving, "save and apply".
- **Shared network namespaces done right** (`network_mode: service:x` /
  `container:x`, e.g. apps behind a VPN container like gluetun):
  - restarts follow the dependency order, so dependents never try to join a
    namespace that is down;
  - dockerr notices when a container lost its parent's network (parent restarted
    or recreated) and repairs it after each operation, or on demand.
- **Update checks** with `HEAD` requests to registries: no Docker Hub pull quota
  used, private registries through the usual `config.json`.
- **Advanced mode**: containers outside compose, images, networks, volumes and
  cleanup; container details; network map; compose history with diff and
  restore; container terminal (off by default, every session logged).

Logs are left to [Dozzle](https://dozzle.dev): set `DOCKERR_DOZZLE_URL` for a
logs link on each container.

## Security model

Access to Docker is root on the host. dockerr is built to sit behind a forward-auth
proxy (Traefik + Authelia) and to talk to Docker through a socket proxy:

- it trusts the user header (`Remote-User`) only from `DOCKERR_TRUSTED_PROXIES`,
  which is required; optional group check with `DOCKERR_REQUIRE_GROUP`;
- state-changing requests must come from the same origin;
- at start it probes which API sections the socket proxy allows and greys out the
  features it cannot perform, naming the missing proxy setting;
- the terminal needs both `DOCKERR_EXEC=1` and `EXEC=1` on the proxy;
- it refuses to operate the stack it runs in.

## Install

See [`deploy/docker-compose.yml`](deploy/docker-compose.yml): dockerr, its own
`tecnativa/docker-socket-proxy` on an internal network, Traefik labels.

Mount the stacks folder **at the same path as on the host** (`/mnt/docker:/mnt/docker`)
so relative bind mounts in compose files resolve to the same place.

## Configuration

| Variable | Default | |
|---|---|---|
| `DOCKER_HOST` | local socket | `tcp://dockerr-socket:2375` behind a socket proxy |
| `DOCKERR_STACKS_DIR` | `/mnt/docker` | one folder per stack, holding `compose.yaml` or `docker-compose.yml` |
| `DOCKERR_TRUSTED_PROXIES` | — (required) | IPs, CIDRs or host names allowed to set the user header |
| `DOCKERR_AUTH_HEADER` | `Remote-User` | |
| `DOCKERR_REQUIRE_GROUP` | — | group required in `DOCKERR_GROUPS_HEADER` (`Remote-Groups`) |
| `DOCKERR_DOZZLE_URL` | — | adds a logs link per container |
| `DOCKERR_PORT_HOST` | request host | host used in links to published ports |
| `DOCKERR_EXEC` | `false` | container terminal |
| `DOCKERR_UPDATE_INTERVAL` | `6h` | image update checks |
| `DOCKERR_REPAIR_NETDEPS` | `true` | repair lost network namespaces after operations |
| `DOCKERR_DATA_DIR` | `/data` | SQLite database (history, preferences, terminal log) |
| `DOCKERR_LISTEN` | `:8080` | |
| `DOCKERR_INSECURE_NOAUTH` | `false` | no authentication: local development only |

## Development

```sh
make dev      # local socket, no auth, http://127.0.0.1:8080
make test
make vendor   # rebuild the CodeMirror and xterm bundles (Node)
```

Go 1.27, the Docker compose SDK (`github.com/docker/compose/v5`), server-rendered
HTML with a little vanilla JavaScript.
