# AI Gateway

[![Go Version](https://img.shields.io/github/go-mod/go-version/DatanoiseTV/aigateway)](https://github.com/DatanoiseTV/aigateway)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight, self-hosted API gateway that sits between your applications and LLM providers. Each client gets their own API key with independent configuration: backend provider, upstream API key, default model, model whitelist, rate limits, and token quotas.

- **OpenAI-compatible** - Works with any OpenAI SDK or tool
- **Per-client config** - Each API key routes to different providers
- **Real-time dashboard** - Live stats via WebSocket
- **Local models** - Built-in support for Ollama and LM Studio

---

# Screenshots

<img width="2994" height="1542" alt="image" src="https://github.com/user-attachments/assets/420c007e-2443-4e3d-822f-1743daa9142f" />
<img width="2994" height="1544" alt="image" src="https://github.com/user-attachments/assets/afa7b44a-b2f6-4d1b-b47e-96aa33ea6292" />
<img width="2994" height="1544" alt="image" src="https://github.com/user-attachments/assets/9fecf964-67b2-4163-af83-3c747c812dec" />


## Supported Providers

| Provider | Protocol | Default Endpoint | Auth |
|---|---|---|---|
| Google Gemini | Gemini native | `generativelanguage.googleapis.com` | API key |
| OpenAI | Chat Completions | `api.openai.com` | Bearer token |
| Anthropic | Messages API | `api.anthropic.com` | `x-api-key` |
| Mistral | Chat Completions | `api.mistral.ai` | Bearer token |
| Perplexity AI | Chat Completions | `api.perplexity.ai` | Bearer token |
| xAI / Grok | Chat Completions | `api.x.ai` | Bearer token |
| Cohere | Chat Completions | `api.cohere.com` | Bearer token |
| Azure OpenAI | Chat Completions | Custom resource URL | `api-key` |
| Ollama | Chat Completions | `localhost:11434` | None |
| LM Studio | Chat Completions | `localhost:1234` | None |

All providers support streaming via Server-Sent Events. Any OpenAI-compatible endpoint not listed above can be added as a generic provider.

---

## Getting Started

### Download

Grab a binary from the [releases page](https://github.com/DatanoiseTV/aigateway/releases) or build from source:

```bash
go build -o ai-gateway ./cmd/server
```

### Run

```bash
./ai-gateway
```

On first launch the server creates a `config.yaml`, generates admin credentials (printed once to stdout), and initializes the database. Default port is `8090`.

### Quick Setup

1. Open `http://localhost:8090/admin`, log in with credentials from stdout
2. Go to **Clients** → **New Client**
3. Configure:
   - **Backend**: Provider (gemini, openai, anthropic, ollama, lmstudio, etc.)
   - **Backend API Key**: Your API key for that provider
   - **Default Model**: Model name to use by default
   - **Base URL**: Override (e.g., `http://localhost:11434` for local Ollama)
4. Use the client's API key with your OpenAI-compatible app

### Example: Using with Ollama

```bash
# Create client with:
#   Backend: ollama
#   Base URL: http://localhost:11434
#   Default Model: llama3.2

curl http://localhost:8090/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama3.2",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

---

## Usage

### Chat Completions

Works with any OpenAI-compatible client library or tool.

```bash
curl http://localhost:8090/v1/chat/completions \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "stream": true,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

The gateway resolves the client's assigned backend from the API key, translates the request into the provider's native format, and streams the response back as OpenAI-format SSE.

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    api_key="<CLIENT_API_KEY>",
    base_url="http://localhost:8090/v1",
)

stream = client.chat.completions.create(
    model="gemini-2.5-flash",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True,
)

for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

### Gemini Native API

Direct passthrough for applications that use the Gemini protocol:

```bash
curl http://localhost:8090/v1beta/models/gemini-2.5-flash:generateContent \
  -H "Authorization: Bearer <CLIENT_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "Hello"}]}]}'
```

### List Models

```bash
curl http://localhost:8090/v1/models \
  -H "Authorization: Bearer <CLIENT_API_KEY>"
```

Returns models available to the client (from cached model list or auto-fetched from backend). If no models are configured, they are automatically fetched from the client's backend on first request.

---

## Per-Client Features

Each client (API key) has independent configuration:

| Feature | Description |
|---|---|
| **Backend Provider** | Route requests to any supported provider (Gemini, OpenAI, Anthropic, Ollama, LM Studio, etc.) |
| **Backend API Key** | Per-client upstream API key (uses provider's credentials) |
| **Default Model** | Model to use when none specified |
| **Model Whitelist** | Restrict which models this client can access |
| **System Prompt** | Injected as a system message on every request |
| **Tool Mode** | Pass-through (forward tool_calls to client) or Gateway (execute internally) |
| **Base URL Override** | Point at a specific Ollama/LM Studio instance |
| **Rate Limits** | Per-minute, per-hour, per-day request caps |
| **Token Quotas** | Daily input/output token budgets |
| **Max Tokens** | Per-request input/output token limits |
| **API Key Prefix** | `gm_`, `sk-`, or `sk-ant-` style keys |
| **Active/Inactive** | Disable a key without deleting it |

---

## Admin Dashboard

The web UI at `/admin` provides:

- **Real-time stats** -- requests, tokens, and model usage updating live via WebSocket
- **Client management** -- create, edit, disable, and delete clients
- **Test Connection** -- verify connectivity to client backend
- **Fetch Models** -- auto-discover available models from backend (Ollama, LM Studio, etc.)
- **Model Whitelist UI** -- select which models each client can use
- **Request history** -- per-client and global request logs with status, latency, and token counts

---

## Prometheus Metrics

The gateway exposes Prometheus metrics at `/metrics` with HTTP Basic authentication:

```yaml
# config.yaml
prometheus:
  enabled: true
  username: prometheus
  password: your-secure-password
```

**Available metrics:**
- `ai_gateway_requests_total` - Total requests by client/model/status
- `ai_gateway_requests_in_progress` - Current in-flight requests
- `ai_gateway_input_tokens_total` - Input tokens by client/model
- `ai_gateway_output_tokens_total` - Output tokens by client/model
- `ai_gateway_request_duration_seconds` - Request duration histogram
- `ai_gateway_active_clients` - Number of active clients
- `ai_gateway_upstream_errors_total` - Upstream errors by client/provider

**Grafana Dashboard:** Import `contrib/grafana-dashboard.json` for a pre-built dashboard.

---

## OpenCode Integration

The gateway supports tool calling with [opencode](https://opencode.ai). Configure your `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "myprovider": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://127.0.0.1:8099",
        "apiKey": "gm_..."
      },
      "models": {
        "my-model": {}
      }
    }
  }
}
```

**Tool Mode:** The gateway defaults to "pass-through" mode, which forwards tool_calls to opencode for execution. This allows opencode to execute tools (bash, read, write, edit, glob, grep, etc.) locally. Set to "Gateway" in the admin UI if you want the gateway to handle tools (requires implementing tool functions).

---

## Configuration

### config.yaml

```yaml
server:
  host: 0.0.0.0
  port: 8090
  https:
    enabled: false

defaults:
  rate_limit:
    requests_per_minute: 60
    requests_per_hour: 1000
    requests_per_day: 10000
  quota:
    max_input_tokens_per_day: 1000000
    max_output_tokens_per_day: 500000
    max_requests_per_day: 1000
    max_input_tokens: 1000000
    max_output_tokens: 8192

database:
  path: ./data/gateway.db
```

All provider configuration (API keys, endpoints, models) is done per-client in the admin UI. Each client can have its own backend provider, upstream API key, base URL, and model settings.

### CLI Flags

| Flag | Description | Default |
|---|---|---|
| `-config` | Config file path | `config.yaml` |
| `-port` | Port override | from config |

### HTTPS

```yaml
server:
  https:
    enabled: true
    cert_file: /path/to/cert.pem
    key_file: /path/to/key.pem
```

---

## Building

### From Source

```
make build
```

Version, commit hash, and build time are embedded automatically via ldflags.

### Cross-Compile

```
make release
```

Produces binaries for Linux (amd64, arm64), macOS (amd64, arm64), and Windows (amd64) in `dist/`.

### Docker (planned)

Not yet available. Contributions welcome.

---

## Architecture

```
                        +------------------+
                        |   Admin Web UI   |
                        |  (WebSocket live|
                        |    dashboard)   |
                        +--------+---------+
                                 |
Clients -----> AI Gateway (:8090)
  |              |
  | OpenAI API   +---> Per-Client Provider
  | (any SDK)    |      (configured in admin UI)
  |              |
  | Gemini API   +---> SQLite (clients, usage, logs)
  | (native)    |
  +---> Any LLM provider
```

Each client request is routed to its configured backend provider based on the API key used.

---

## Project Structure

```
cmd/server/              Entry point
internal/
  config/                Config loading, migration, defaults
  handlers/              HTTP handlers (chat completions, proxy, admin)
  middleware/            Auth, rate limiting, security, logging
  models/                Database models
  providers/             Backend provider interface + implementations
    provider.go          Interface, registry, factory
    gemini.go            Google Gemini
    openai_compat.go     OpenAI, Mistral, Perplexity, xAI, Cohere, Ollama, LM Studio
    anthropic.go         Anthropic
    azure_openai.go      Azure OpenAI
  services/              Request logging, stats, WebSocket hub
  templates/             Embedded static assets
```

---

## Security

- Client API keys are stored as SHA-256 hashes
- Upstream provider API keys are stored per-client (encrypted at rest)
- Admin sessions use signed, HTTP-only cookies
- Security headers on every response (HSTS, X-Frame-Options, X-Content-Type-Options)
- Per-client rate limiting and quota enforcement
- Request body size capped at 10 MB
- System prompt injection allows enforcing guardrails per client

---

## License

MIT
