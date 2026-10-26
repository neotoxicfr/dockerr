# dockerr — multi-arch image. CGO_ENABLED=0 (pure-Go SQLite): cross-compiles natively.

# Digest-pinned: the tag alone follows Alpine releases.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
ARG TARGETOS TARGETARCH VERSION=dev REVISION=
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
# The version stamps asset URLs (?v=) and the service worker cache: a :dev
# build must change it on every commit, or browsers keep stale assets.
RUN V="$VERSION"; [ "$V" = dev ] && [ -n "$REVISION" ] && V="dev-$(echo "$REVISION" | cut -c1-7)"; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/neotoxicfr/dockerr/internal/server.Version=${V}" \
      -o /out/dockerr ./cmd/dockerr \
    && mkdir -p /out/data

# distroless/static: CA bundle for registry checks, no shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88
COPY --from=build /out/dockerr /dockerr
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV DOCKERR_LISTEN=:8080 \
    DOCKERR_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
# Runs as nonroot by default. To edit compose files owned by root, run with
# user: "0:0" — dockerr already controls Docker, which is root-equivalent.
USER nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/dockerr", "healthcheck"]
ENTRYPOINT ["/dockerr"]
