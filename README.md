# AI Gateway

A multi-backend AI API gateway written in Go. Routes OpenAI-compatible requests to any upstream LLM provider with per-client authentication, rate limiting, usage quotas, and a real-time admin dashboard.

Each client gets its own API key, backend provider, rate limits, token quotas, and optional system prompt injection -- all managed through the web UI.

## Supported Backends

| Provider | Type | Auth | Streaming |
|----------|------|------|-----------|
| **Google Gemini** | Native Gemini API | API key | SSE via `streamGenerateContent` |
| **OpenAI** | Chat Completions | Bearer token | SSE |
| **Anthropic** | Messages API | `x-api-key` header | SSE |
| **Mistral** | OpenAI-compatible | Bearer token | SSE |
| **Ollama** | OpenAI-compatible (local) | None | SSE |
| **LM Studio** | OpenAI-compatible (local) | None | SSE |

Any OpenAI-compatible backend not listed above can be added through the settings UI by choosing the appropriate base type.

## Features

- **Multi-Backend Routing** -- each client can be assigned a different upstream provider
- **Per-Client System Prompt** -- inject a system message on every request (guardrails, personas, instructions)
- **Per-Client Base URL Override** -- point individual clients at different Ollama/LM Studio instances
- **OpenAI-Compatible API** -- clients connect using any OpenAI SDK or tool (`/v1/chat/completions`, `/chat/completions`)
- **Real-Time Streaming** -- true token-by-token streaming from upstream to client via SSE
- **API Key Authentication** -- SHA-256 hashed keys with configurable prefixes (`gm_`, `sk-`, `sk-ant-`)
- **Rate Limiting** -- configurable per-minute, per-hour, and per-day limits per client
- **Token Quotas** -- daily input/output token limits and per-request max token caps
- **Real-Time Dashboard** -- WebSocket-powered live stats, request log, and model usage chart
- **Admin Web UI** -- manage clients, providers, settings, and view usage from the browser
- **SQLite Database** -- zero-dependency embedded storage, no external database required
- **Gemini Native Proxy** -- direct passthrough for Gemini API format (`/v1beta/models/:model:generateContent`)
- **HTTPS Support** -- optional TLS with cert/key configuration
- **Security Headers** -- HSTS, X-Frame-Options, CSP, and more on every response

## Quick Start

### Build

```bash
go build -o ai-gateway ./cmd/server
```

### Run

```bash
./ai-gateway
```

On first run the server will:
1. Create `config.yaml` with default settings
2. Generate a random admin password (printed to the console -- save it)
3. Create `data/` and `logs/` directories

### Access the Admin UI

Open `http://localhost:8090/admin` and log in with the generated credentials.

From the dashboard you can:
- Add backend providers (Gemini, OpenAI, Anthropic, Mistral, Ollama, LM Studio)
- Create client API keys with per-client backend, rate limits, and quotas
- Configure per-client system prompts
- Monitor usage in real time

## API Usage

### OpenAI-Compatible Chat Completions

Any OpenAI client library or tool works out of the box. Point it at the gateway and use the client's API key.

```bash
curl -X POST http://localhost:8090/v1/chat/completions \
  -H "Authorization: Bearer YOUR-CLIENT-API-KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.0-flash",
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ],
    "stream": true
  }'
```

The gateway resolves the client's backend provider from the API key, translates the request into the provider's native format, streams the response back as OpenAI-format SSE, and logs the usage.

### Streaming

Set `"stream": true` in the request body. The gateway will stream tokens from the upstream provider to the client in real time using Server-Sent Events.

### Gemini Native API

For clients that speak the Gemini protocol directly:

```bash
curl -X POST http://localhost:8090/v1beta/models/gemini-2.0-flash:generateContent \
  -H "Authorization: Bearer YOUR-CLIENT-API-KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello"}]}]
  }'
```

### List Models

```bash
curl http://localhost:8090/v1/models \
  -H "Authorization: Bearer YOUR-CLIENT-API-KEY"
```

Returns models from all configured providers.

## Configuration

### config.yaml

```yaml
server:
  host: 0.0.0.0
  port: 8090
  https:
    enabled: false
    cert_file: ""
    key_file: ""

admin:
  username: admin
  password_hash: ""      # auto-generated on first run
  session_secret: ""     # auto-generated on first run

providers:
  gemini:
    type: gemini
    api_key: ""
    default_model: gemini-2.0-flash
    allowed_models:
      - gemini-2.0-flash
      - gemini-2.5-flash
      - gemini-2.5-pro
    timeout_seconds: 120

  openai:
    type: openai
    api_key: ""
    default_model: gpt-4o
    timeout_seconds: 120

  anthropic:
    type: anthropic
    api_key: ""
    default_model: claude-sonnet-4-20250514
    timeout_seconds: 120

  mistral:
    type: mistral
    api_key: ""
    default_model: mistral-large-latest
    timeout_seconds: 120

  ollama:
    type: ollama
    base_url: http://localhost:11434/v1
    default_model: llama3.2
    timeout_seconds: 120

  lmstudio:
    type: lmstudio
    base_url: http://localhost:1234/v1
    default_model: default
    timeout_seconds: 120

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

logging:
  level: info
  file: ./logs/gateway.log
```

Providers can also be added and edited through the admin UI at `/admin/settings`.

### Per-Client Settings

Each client has:

| Setting | Description |
|---------|-------------|
| **Backend** | Which provider to route requests through |
| **Base URL Override** | Custom endpoint for local backends (Ollama, LM Studio) |
| **System Prompt** | Prepended as a system message to every request |
| **Rate Limits** | Requests per minute / hour / day |
| **Token Quotas** | Daily input/output token limits |
| **Max Tokens** | Per-request input/output token caps |
| **API Key Type** | Prefix style: `gm_`, `sk-`, or `sk-ant-` |

### CLI Flags

```
-config string    Path to config file (default "config.yaml")
-port int         Port override (overrides config value)
```

## Architecture

```
Client (OpenAI SDK, curl, etc.)
  |
  v
AI Gateway (:8090)
  |-- /v1/chat/completions    --> Per-client provider routing
  |-- /chat/completions       --> Same, without /v1 prefix
  |-- /v1beta/models/:model   --> Gemini native passthrough
  |-- /admin/*                --> Web UI + WebSocket dashboard
  |
  +--> Provider Registry
        |-- gemini    --> Google Gemini API
        |-- openai    --> OpenAI API
        |-- anthropic --> Anthropic Messages API
        |-- mistral   --> Mistral API
        |-- ollama    --> Local Ollama instance
        |-- lmstudio  --> Local LM Studio instance
```

## Project Structure

```
.
├── cmd/server/              Main entry point
├── internal/
│   ├── config/              Configuration loading and migration
│   ├── handlers/            HTTP handlers (OpenAI compat, Gemini proxy, admin UI)
│   ├── middleware/          Auth, rate limiting, security headers, logging
│   ├── models/              Database models (Client, RequestLog, DailyUsage)
│   ├── providers/           Backend provider interface and implementations
│   │   ├── provider.go      Interface, registry, factory
│   │   ├── gemini.go        Google Gemini provider
│   │   ├── openai_compat.go OpenAI/Mistral/Ollama/LM Studio provider
│   │   └── anthropic.go     Anthropic Messages API provider
│   ├── services/            Business logic (logging, stats, WS hub)
│   └── templates/           Embedded static assets
├── config.yaml              Configuration file
├── data/                    SQLite database
└── logs/                    Request logs
```

## Security

- API keys are stored as SHA-256 hashes (never in plaintext)
- Admin sessions use secure, HTTP-only cookies with configurable secret
- All responses include security headers (HSTS, X-Frame-Options, CSP, etc.)
- Per-client rate limiting prevents abuse
- Request body size is capped at 10MB
- Provider API keys are stored in config.yaml (file permissions: 0600)

## License

MIT
