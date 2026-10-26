# Security Policy

## Supported versions

Only the latest release is supported. Fixes land on `dev` and ship in the next
tag; there are no backports.

| Version | Supported |
| ------- | --------- |
| latest release | ✅ |
| `dev` | ⚠️ pre-release, no guarantees |
| anything older | ❌ |

## Reporting a vulnerability

Open a [private security advisory](https://github.com/neotoxicfr/dockerr/security/advisories/new)
rather than a public issue. Please include the version, the configuration
involved (proxy, auth, socket access), and a way to reproduce.

## Security model

dockerr drives Docker. Whoever controls it controls the host, so it is built to
never be the only thing standing in front of the Docker API.

- **Authentication is delegated** to a forward-auth proxy (Traefik + Authelia or
  similar). dockerr reads the user from a header (`Remote-User` by default) and
  trusts it only from the addresses in `DOCKERR_TRUSTED_PROXIES`, which has no
  default: dockerr refuses to start without it. Host names are accepted and
  re-resolved every 30 s, so a recreated proxy container keeps working.
- **The proxy must strip identity headers sent by clients**, and the auth
  provider must never apply a `bypass` policy to dockerr's domain. Otherwise a
  client could send its own `Remote-User`. The example compose adds the
  stripping middleware.
- **Optional group check**: `DOCKERR_REQUIRE_GROUP` must appear in
  `Remote-Groups`.
- **Cross-site requests are refused**: every state-changing request must be
  same-origin (`Sec-Fetch-Site`, or `Origin` matching `Host`); the terminal
  WebSocket checks its origin too.
- **Least privilege towards Docker**: use a socket proxy
  (`tecnativa/docker-socket-proxy`) with only the sections dockerr needs. At
  start, dockerr probes each section with requests that have no side effect and
  disables what the proxy refuses.
- **Terminal off by default**: it needs `DOCKERR_EXEC=1` *and* `EXEC=1` on the
  proxy. Every session is logged (user, container, start, end).
- **Self-protection**: dockerr refuses to operate the stack it runs in.
- **Secrets in the UI**: environment variables are blurred until clicked; the
  history keeps every saved compose and `.env`, so treat the data volume as
  sensitive.
- **Headers**: a strict Content-Security-Policy: scripts from `'self'` only,
  no third-party origin, `object-src 'none'`, and inline `<style>` only with a
  per-response nonce (the editor uses it). The terminal page is the one
  exception: xterm.js cannot carry a nonce, so it allows inline styles there,
  never inline scripts. Plus `Permissions-Policy` (no camera, microphone or
  geolocation), `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`.

## Container hardening

The image is `distroless/static` (CA bundle, no shell, no package manager) and
runs as non-root by default. Editing compose files owned by root needs
`user: "0:0"`; that grants nothing beyond what access to Docker already gives.

## Supply chain

- Every GitHub Action is pinned to a full commit SHA.
- CI runs the race tests, golangci-lint, `govulncheck` and a Trivy filesystem
  scan on every pull request, push to `dev` and release tag. No image is
  published unless it passes.
- The editor and terminal bundles (CodeMirror, xterm.js) are vendored, built
  from a pinned `package-lock.json`; nothing loads from a CDN.
- Dependabot watches the Go modules, the Dockerfile,
  the workflows and the vendored npm bundles, with a week of cooldown.
