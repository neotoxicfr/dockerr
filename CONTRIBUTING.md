# Contributing

## Prerequisites

- **Go 1.27+** (the version in `go.mod` is authoritative; CI installs it from there)
- **golangci-lint v2.14+** and **govulncheck**
- **A C toolchain** (gcc/clang), only for `go test -race`
- **A Docker engine** to try changes against, ideally a throwaway one
- **Node.js**, only to rebuild the vendored editor and terminal bundles

```bash
make dev    # local socket, no auth, http://127.0.0.1:8080
```

`make dev` sets `DOCKERR_INSECURE_NOAUTH=1`: never expose that instance.

## Layout

```
cmd/dockerr          entry point, wiring
internal/config      environment variables
internal/docker      API client, capability probe, stats, events
internal/stacks      stack discovery, state, compose files, validation
internal/ops         compose operations and their live progress
internal/netdeps     network_mode container:/service: graph and repair
internal/updates     registry digest checks
internal/server      HTTP handlers, templates helpers
internal/store       SQLite: history, preferences, terminal log
web/templates        html/template pages and partials
web/static           CSS, JS, icons, service worker, vendored bundles
tools/vendor         sources of the CodeMirror and xterm bundles
```

## Project conventions

- **Server-rendered UI**: `html/template` plus vanilla JS, embedded in the
  binary. npm is only used to build `web/static/vendor`, which is committed.
- **Two modes**: the default mode covers the daily tasks; anything else goes
  behind the advanced mode. Keep the default screens short.
- **No eyebrows, no filler text**: headings and controls carry their meaning.
- **Desktop and mobile both matter**: check every UI change at 1440×900 and at
  a phone width, in light and dark.
- **Errors**: wrap with context, `fmt.Errorf("context: %w", err)`. Messages
  shown to users are in French and name the fix when there is one.
- **Config**: `DOCKERR_*` variables, validated in `config.Load()`; a bad value
  fails at startup. A new variable goes in `config.go`, `README.md` and
  `deploy/docker-compose.yml`.
- **Tests**: table-driven. Anything touching `network_mode` handling needs a
  case in `internal/netdeps`.
- **Docker side effects**: test operations on a throwaway stack, never on
  stacks you care about.

## Workflow

The default branch is `main`; day-to-day work targets **`dev`**.

Commits follow [Conventional Commits](https://www.conventionalcommits.org), in
English, with a body that explains the scenario when the change is not obvious:

```
feat(stacks): show the compose file path
fix(ops): restart dependents after their parent
chore(deps): bump the compose SDK
```

Pull requests target `dev` and are squash-merged. At release time, `dev` is
merged into `main` and the tag is pushed.

## CI

- `ci.yml`: build, race tests, golangci-lint, govulncheck and a Trivy scan, on
  pull requests and pushes to `dev`.
- `docker-publish.yml`: on a push to `dev`, runs `ci.yml` then publishes
  `:dev` (amd64). It publishes nothing else.

## Before pushing

```bash
make check    # build, test-race, lint, vuln: what CI runs
```

If you changed `.github/workflows/`, run `actionlint`; every `uses:` must be
pinned to a full commit SHA.

## Releases

Releases are built on the maintainer's machine, not in Actions:

```bash
make release V=v1.2.3 NOTES=notes.md          # DRY=1 to build without pushing
```

`tools/release` runs `make check`, cross-compiles amd64, arm64 and arm/v7,
layers each binary on the distroless base pinned in the `Dockerfile`, pushes
the multi-arch image as `X.Y.Z` and `latest`, tags, creates the GitHub release
with the notes file and the binaries, then moves `main` to the release.

Semantic versioning. Git tags and release titles carry the `v` (`v1.2.3`);
image tags do not (`ghcr.io/neotoxicfr/dockerr:1.2.3`). Release notes are
written by hand: `## <emoji> <Category>` sections and a `### Docker` block
with the pull command. No generated content.
