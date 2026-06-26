BINARY := docker-in-kubernetes
PKG    := ./...
SOCKET ?= /tmp/docker-in-kubernetes.sock

VERSION    ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo 0.0.0-dev)
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME)

.PHONY: all build test lint fmt vet run clean tidy

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test -race -count=1 $(PKG)

lint:
	golangci-lint run

fmt:
	gofmt -w .

vet:
	go vet $(PKG)

tidy:
	go mod tidy

run: build
	./bin/$(BINARY) --socket $(SOCKET)

clean:
	rm -rf bin/
	rm -f $(SOCKET)
