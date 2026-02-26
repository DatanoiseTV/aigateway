# Architecture

## Overview

AI Gateway is a self-hosted API gateway that routes LLM requests to multiple backends. Each client (API key) has independent configuration including provider, API key, model, and rate limits.

## Components

### Entry Point
- `cmd/server/main.go` - Server initialization, router setup

### Core
- `internal/config/config.go` - Configuration loading from YAML
- `internal/models/models.go` - Database models (Client, RequestLog)

### Providers
- `internal/providers/provider.go` - Provider interface definition
- `internal/providers/gemini.go` - Google Gemini provider
- `internal/providers/anthropic.go` - Anthropic provider  
- `internal/providers/openai_compat.go` - OpenAI-compatible providers (OpenAI, Mistral, Ollama, LM Studio, Perplexity, xAI, Cohere)
- `internal/providers/azure_openai.go` - Azure OpenAI provider

### Handlers
- `internal/handlers/openai.go` - OpenAI-compatible chat completions, streaming, tool calling
- `internal/handlers/admin.go` - Admin dashboard UI and API
- `internal/handlers/proxy.go` - Legacy proxy handler

### Services
- `internal/services/client.go` - Client CRUD operations, API key management
- `internal/services/gemini.go` - Request logging
- `internal/services/stats.go` - Statistics aggregation
- `internal/services/wshub.go` - WebSocket hub for real-time dashboard updates
- `internal/services/tools.go` - Tool registry (for gateway-mode tool execution)

### Middleware
- `internal/middleware/auth.go` - API key authentication
- `internal/middleware/ratelimit.go` - Rate limiting per client
- `internal/middleware/security.go` - Security headers

## Request Flow

```
Client Request
    ↓
Authentication (API key lookup)
    ↓
Rate Limiting
    ↓
Route Resolution (client → provider)
    ↓
Provider Request Building
    ↓
Upstream API Call
    ↓
Response Parsing
    ↓
Tool Execution Loop (if enabled)
    ↓
Response to Client
    ↓
WebSocket Broadcast (stats update)
```

## Tool Calling Modes

### Pass-through (default)
- Gateway forwards `tool_calls` to client (opencode)
- Client executes tools and sends results back
- Gateway passes results to upstream provider

### Gateway
- Gateway attempts to execute tools internally
- Requires tool implementations in `services/tools.go`
- Limited to built-in tools only

## Database

- SQLite by default (`data/gateway.db`)
- Tables: clients, request_logs

## API Endpoints

| Endpoint | Description |
|---|---|
| `POST /v1/chat/completions` | OpenAI-compatible chat completions |
| `POST /chat/completions` | Alias for above |
| `GET /v1/models` | List available models |
| `GET /admin` | Admin dashboard |
| `GET /admin/ws` | WebSocket for real-time stats |
| `GET /admin/api/*` | Admin API endpoints |
