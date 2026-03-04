# ---- Build stage ----
FROM golang:1.24-alpine AS builder

# gcc and musl-dev required for CGO (go-sqlite3)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown

# CGO_ENABLED=1 is required — go-sqlite3 uses CGO bindings
RUN CGO_ENABLED=1 go build \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o /ai-gateway ./cmd/server

# Build password-hash utility (pure Go, no CGO needed)
RUN mkdir -p cmd/hashpw && cat > cmd/hashpw/main.go <<'GOEOF'
package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: hashpw <password>")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(os.Args[1]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(string(hash))
}
GOEOF
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /hashpw ./cmd/hashpw

# ---- Runtime stage ----
FROM alpine:3.21

# ca-certificates needed for HTTPS calls to upstream LLM providers
RUN apk add --no-cache ca-certificates

WORKDIR /app
RUN mkdir -p data logs

COPY --from=builder /ai-gateway .
COPY --from=builder /hashpw /usr/local/bin/hashpw
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8090/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
