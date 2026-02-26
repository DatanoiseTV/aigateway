# RunPod configuration for AI Gateway

## Deploying on RunPod

### Option 1: Deploy from Template

1. Go to [RunPod](https://runpod.com/)
2. Search for "AI Gateway" template or deploy a new container
3. Use the following settings:
   - Container Image: Your custom image or build from Dockerfile
   - Port: 8090
   - Environment Variables: None required (config via config.yaml)

### Option 2: Custom Dockerfile

Create a `Dockerfile`:

```dockerfile
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /ai-gateway ./cmd/server

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /ai-gateway .
COPY config.example.yaml config.yaml
RUN mkdir -p data logs

EXPOSE 8090

CMD ["./ai-gateway"]
```

### Option 3: Deploy with Docker Compose

Create `docker-compose.yml`:

```yaml
version: '3.8'

services:
  ai-gateway:
    build: .
    ports:
      - "8090:8090"
    volumes:
      - ./data:/app/data
      - ./logs:/app/logs
      - ./config.yaml:/app/config.yaml
    restart: unless-stopped
    environment:
      - GIN_MODE=release
```

### Required Configuration

In your `config.yaml`, make sure to set:

```yaml
server:
    host: 0.0.0.0
    port: 8090

admin:
    username: admin
    # Password hash - generate with: echo -n "yourpassword" | bcrypt
    password_hash: ""

database:
    path: ./data/gateway.db
```

### Health Checks

For RunPod health checks, use:
- Endpoint: `http://localhost:8090/health`
- Type: HTTP
- Interval: 30s
- Timeout: 10s

### GPU Support

AI Gateway doesn't require GPU directly, but if you're connecting to GPU backends (like Ollama or LM Studio), ensure:
1. The container has access to the GPU (RunPod handles this automatically)
2. Backend URLs point to internal GPU services

### Network Configuration

If using Ollama or LM Studio on the same pod:
- Ollama default: `http://localhost:11434`
- LM Studio default: `http://localhost:1234`

Add to client's backend configuration in the admin UI.
