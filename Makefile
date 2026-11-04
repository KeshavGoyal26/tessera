# Developer entry points. CI runs the same steps (.github/workflows/ci.yml).
GO       ?= go
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/KeshavGoyal26/tessera/pkg/version.Version=$(VERSION) \
            -X github.com/KeshavGoyal26/tessera/pkg/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null)

.PHONY: all build test test-race lint proto proto-check js java ci clean

all: build test

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

bench:
	$(GO) test -run '^$$' -bench . -benchmem ./pkg/...

lint:
	$(GO) vet ./...
	golangci-lint run ./...

# Requires buf and protoc-gen-go on PATH (see docs/PROTOCOL.md section 1).
proto:
	buf lint
	buf generate
	cd sdk/js && npm run generate

proto-check: proto
	git diff --exit-code -- gen/go sdk/js/src/gen

js:
	cd sdk/js && npm ci && npm run lint && npm run typecheck && npm run build && npm test
	cd examples/web-demo && npm ci && npm run build

java:
	cd sdk/java && mvn -B verify

ci: lint test-race js java

clean:
	rm -rf bin sdk/js/dist examples/web-demo/dist sdk/java/target
