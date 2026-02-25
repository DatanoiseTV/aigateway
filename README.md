# AI Gateway

A lightweight, self-hosted API gateway that sits between your applications and LLM providers. It exposes an OpenAI-compatible API and routes requests to the configured upstream backend -- Google Gemini, OpenAI, Anthropic, Mistral, Perplexity, xAI, Cohere, Azure OpenAI, Ollama, or LM Studio.

Each client gets its own API key, backend assignment, rate limits, token quotas, and optional system prompt. Everything is managed through a built-in admin dashboard with real-time usage monitoring.

---

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

```
go build -o ai-gateway ./cmd/server
```

### Run

```
./ai-gateway
```

On first launch the server creates a `config.yaml`, generates admin credentials (printed once to stdout), and initializes the database. Default port is `8090`.

### Configure

Open `http://localhost:8090/admin`, log in, and:

1. Go to **Settings** -- add your provider API keys
2. Go to **Clients** -- create API keys for your applications
3. Point your OpenAI SDK at `http://localhost:8090/v1` with the client key

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

Returns models from all configured providers.

---

## Per-Client Features

Each client (API key) has independent configuration:

| Feature | Description |
|---|---|
| **Backend Provider** | Route requests to any configured provider |
| **System Prompt** | Injected as a system message on every request |
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
- **Provider settings** -- add and configure backend providers
- **Request history** -- per-client and global request logs with status, latency, and token counts

---

## Configuration

### config.yaml

```yaml
server:
  host: 0.0.0.0
  port: 8090
  https:
    enabled: false

providers:
  gemini:
    type: gemini
    api_key: ""
    default_model: gemini-2.5-flash
    timeout_seconds: 120
  openai:
    type: openai
    api_key: ""
    default_model: gpt-4o
  anthropic:
    type: anthropic
    api_key: ""
    default_model: claude-sonnet-4-20250514
  mistral:
    type: mistral
    api_key: ""
    default_model: mistral-large-latest
  perplexity:
    type: perplexity
    api_key: ""
    default_model: sonar-pro
  xai:
    type: xai
    api_key: ""
    default_model: grok-3
  cohere:
    type: cohere
    api_key: ""
    default_model: command-r-plus
  azure:
    type: azure-openai
    api_key: ""
    base_url: https://YOUR_RESOURCE.openai.azure.com
    default_model: gpt-4o
  ollama:
    type: ollama
    base_url: http://localhost:11434/v1
    default_model: llama3.2
  lmstudio:
    type: lmstudio
    base_url: http://localhost:1234/v1

defaults:
  rate_limit:
    requests_per_minute: 60
    requests_per_hour: 1000
    requests_per_day: 10000
  quota:
    max_input_tokens_per_day: 1000000
    max_output_tokens_per_day: 500000
    max_requests_per_day: 1000

database:
  path: ./data/gateway.db
```

Providers can also be added and configured through the admin UI. Only add the providers you need.

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
                        |  (WebSocket live |
                        |    dashboard)    |
                        +--------+---------+
                                 |
Clients -----> AI Gateway (:8090)
  |              |
  | OpenAI API   +---> Provider Registry
  | (any SDK)    |       |-- gemini     --> Google Gemini API
  |              |       |-- openai     --> OpenAI
  | Gemini API   |       |-- anthropic  --> Anthropic
  | (native)     |       |-- mistral    --> Mistral
                 |       |-- perplexity --> Perplexity AI
                 |       |-- xai        --> xAI / Grok
                 |       |-- cohere     --> Cohere
                 |       |-- azure      --> Azure OpenAI
                 |       |-- ollama     --> Ollama (local)
                 |       +-- lmstudio   --> LM Studio (local)
                 |
                 +---> SQLite (clients, usage, logs)
```

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
- Admin sessions use signed, HTTP-only cookies
- Security headers on every response (HSTS, X-Frame-Options, X-Content-Type-Options)
- Per-client rate limiting and quota enforcement
- Request body size capped at 10 MB
- Provider API keys stored in `config.yaml` with `0600` file permissions
- System prompt injection allows enforcing guardrails per client

---

## License

MIT
