## What and why

<!-- What changes, and what problem it solves. Link an issue if there is one. -->

## Checks

- [ ] `make check` passes (build, race tests, lint, vuln)
- [ ] **UI touched?** checked at 1440×900 **and** on a phone, light and dark
- [ ] **Docker operations touched?** tried on a throwaway stack
- [ ] `actionlint` clean *(only if `.github/workflows/` changed)*
- [ ] No secret, token, domain or personal data in the diff

## Impact

- [ ] **New Go dependency?** justified below
- [ ] **New config option?** added to `config.go`, `README.md` **and** `deploy/docker-compose.yml`
- [ ] **New Docker API call?** the socket-proxy section it needs is probed and documented

## Notes

<!-- Trade-offs, follow-ups, things left out. -->
