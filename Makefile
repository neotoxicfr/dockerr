.PHONY: dev build test test-race vet lint vuln check vendor docker

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/neotoxicfr/dockerr/internal/server.Version=$(VERSION)

# Local run against the Docker socket, without the auth proxy.
dev:
	DOCKERR_INSECURE_NOAUTH=1 DOCKERR_DATA_DIR=./data DOCKERR_LISTEN=127.0.0.1:8080 go run ./cmd/dockerr

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/dockerr ./cmd/dockerr

test:
	go test -timeout 120s ./...

test-race:
	go test -race -timeout 120s ./...

vet:
	go vet ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

# What CI runs, before pushing.
check: build test-race lint vuln

# Rebuild the CodeMirror and xterm bundles in web/static/vendor.
vendor:
	tools/vendor/build.sh

docker:
	docker buildx build --build-arg VERSION=$(VERSION) -t dockerr:dev .
