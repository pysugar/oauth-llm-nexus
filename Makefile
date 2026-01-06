VERSION := $(shell git describe --tags --always --dirty || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD || echo "none")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

LDFLAGS := -X github.com/pysugar/oauth-llm-nexus/internal/version.Version=$(VERSION) \
           -X github.com/pysugar/oauth-llm-nexus/internal/version.Commit=$(COMMIT) \
           -X github.com/pysugar/oauth-llm-nexus/internal/version.BuildTime=$(BUILD_TIME)

.PHONY: build clean test

build:
	go build -ldflags "$(LDFLAGS)" -o nexus ./cmd/nexus

release-mac:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS) -s -w" -o nexus-darwin-arm64 ./cmd/nexus

release-linux:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS) -s -w" -o nexus-linux-amd64 ./cmd/nexus

release-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS) -s -w" -o nexus-linux-arm64 ./cmd/nexus

test:
	go test ./...

clean:
	rm -f nexus nexus-*
