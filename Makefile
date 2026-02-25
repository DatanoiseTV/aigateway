VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)
BINARY  := ai-gateway

.PHONY: build run clean test vet release

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/server

run: build
	./$(BINARY)

test:
	go test -v ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

release:
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		GOOS=$${target%/*}; \
		GOARCH=$${target#*/}; \
		output="$(BINARY)-$${GOOS}-$${GOARCH}"; \
		if [ "$$GOOS" = "windows" ]; then output="$${output}.exe"; fi; \
		echo "Building $${output}..."; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -ldflags "$(LDFLAGS)" -o "dist/$${output}" ./cmd/server; \
	done
	@cd dist && sha256sum * > checksums.txt
	@echo "Binaries in dist/"
