# Docker Deployment

## Building the Image

From the repository root:

```bash
docker build -f docker/Dockerfile -t ai-gateway .
```

With version info:

```bash
docker build -f docker/Dockerfile \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse --short HEAD) \
  -t ai-gateway .
```

## Configuration

AI Gateway reads all settings from a single `config.yaml` file. Copy `docker/config.example.yaml` as a starting point and customise it with your provider API keys and preferences.

Mount the config file into the container at `/app/config.yaml`.

### Admin Password

Three options, from most to least manual:

**1. Pre-compute the bcrypt hash (recommended for PaaS)**

Generate a hash using the image itself:

```bash
docker run --rm ai-gateway hashpw 'your-secure-password'
```

Paste the output into your config:

```yaml
admin:
  username: admin
  password_hash: "$2a$10$..."
  session_secret: "see below"
```

**2. Use the `ADMIN_PASSWORD` environment variable**

Set `ADMIN_PASSWORD` in your PaaS environment. The entrypoint generates the bcrypt hash and injects it into the config on every container start. The variable is cleared from the process environment before the application launches.

This works even if the config mount is read-only.

**3. Use the setup wizard**

Set `password_hash` to `__SETUP_REQUIRED__` (or leave it empty). The app serves a setup page at `/setup` on first boot where you set the password through the browser.

Note: the setup wizard writes the new hash back to `config.yaml`. This requires the config file to be writable inside the container.

### Session Secret

The `session_secret` field signs admin session cookies. Generate one with:

```bash
openssl rand -hex 16
```

If left empty, the app auto-generates a secret and writes it back to the config file (requires writable config). For read-only mounts, always provide a pre-generated value.

## Running

```bash
docker run -d \
  --name ai-gateway \
  -p 8090:8090 \
  -v /path/to/your/config.yaml:/app/config.yaml \
  -v ai-gateway-data:/app/data \
  ai-gateway
```

### Persistent Storage

| Container Path | Purpose | Notes |
|---|---|---|
| `/app/config.yaml` | Configuration file | Mount from host or PaaS config |
| `/app/data/` | SQLite database | **Must be persistent** — contains clients, API keys, request logs, usage stats |
| `/app/logs/` | Application log files | Optional — mount if you need log persistence beyond container lifetime |

The SQLite database is the only stateful component. If the volume backing `/app/data` is lost, all client configurations and API keys are gone.

### Environment Variables

| Variable | Description |
|---|---|
| `ADMIN_PASSWORD` | If set, the entrypoint generates a bcrypt hash and updates `password_hash` in the config before starting. Cleared from the environment after use. |

All other configuration is via `config.yaml`. The application does not read environment variables directly.

### Ports

The default listen port is `8090` (configurable via `server.port` in `config.yaml`).

| Endpoint | Purpose |
|---|---|
| `/health` | Health check (no auth) |
| `/v1/chat/completions` | OpenAI-compatible API (Bearer token) |
| `/admin` | Admin dashboard (session auth) |
| `/metrics` | Prometheus metrics (basic auth, if enabled) |

## Build Notes

- **CGO is required.** The SQLite driver (`go-sqlite3`) uses CGO bindings. The Dockerfile uses `golang:1.24-alpine` with `gcc` and `musl-dev` for compilation.
- **Static assets are embedded** in the Go binary via `embed`. No additional files need to be copied to the runtime image.
- The runtime image is `alpine:3.21` with only `ca-certificates` added (needed for HTTPS calls to upstream LLM providers).
