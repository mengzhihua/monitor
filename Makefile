VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
BIN      = core/bin

.PHONY: all web core test lint run cross clean

all: web core

## web: build the Vue dashboard into core/internal/api/ui/dist (embedded by Go)
web:
	cd web && npm ci && npm run build

## core: build monitord for the current platform
core:
	cd core && go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord ./cmd/monitord

## run: build web + core and start the agent on :19999
run: all
	$(BIN)/monitord -listen :19999 -data-dir ./data

test:
	cd core && go vet ./... && go test -race ./...
	cd web && npm run typecheck

lint:
	cd core && test -z "$$(gofmt -l .)" && go vet ./...

## cross: build server binaries for all supported server platforms
cross:
	cd core && \
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-linux-amd64   ./cmd/monitord && \
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-linux-arm64   ./cmd/monitord && \
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-darwin-amd64  ./cmd/monitord && \
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-darwin-arm64  ./cmd/monitord && \
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-windows-amd64.exe ./cmd/monitord && \
	GOOS=freebsd GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-freebsd-amd64 ./cmd/monitord && \
	GOOS=freebsd GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-freebsd-arm64 ./cmd/monitord && \
	GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN)/monitord-android-arm64 ./cmd/monitord

clean:
	rm -rf $(BIN) core/internal/api/ui/dist/assets core/internal/api/ui/dist/index.html core/internal/api/ui/dist/favicon.svg
