BINARY := docker-in-kubernetes
PKG    := ./...
SOCKET ?= /tmp/docker-in-kubernetes.sock

.PHONY: all build test lint fmt vet run clean tidy

all: lint test build

build:
	go build -o bin/$(BINARY) ./cmd/$(BINARY)

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
