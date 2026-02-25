# TODO - AI Gateway

## 2026-02-25

### Completed
- [x] Add key type selection (gm_, sk-, sk-ant-) when creating client
- [x] Add key type dropdown to create client modal
- [x] Add key type selection to regenerate key function
- [x] Update regenerate key form to include key type selector
- [x] Fix OpenAI streaming 500 error - statusResponseWriter missing http.Flusher interface (72f001b)
- [x] Fix SSE streaming format to follow OpenAI spec (role chunk -> content chunk -> done chunk)
- [x] Fix model mapping - gemini-2.0-flash-lite-001 was incorrectly mapped to gemini-2.0-flash
- [x] Fix Gemini request format - proper multi-turn conversation with roles and system instruction
- [x] Fix error responses to return proper HTTP status codes (429, 502, etc.) instead of 200
- [x] Use crypto/rand for request ID generation
- [x] Implement true streaming via Gemini streamGenerateContent endpoint with alt=sse
- [x] Real-time chunk translation from Gemini SSE to OpenAI SSE format
- [x] Split handler into streaming/non-streaming paths
- [x] Token counting and request logging for streamed responses
- [x] Real-time dashboard via WebSocket (stats cards, recent requests table, model chart)
- [x] WebSocket hub with debounced broadcasting on every logged request
- [x] Auto-reconnect on disconnect (3s backoff)
- [x] Allow All / Clear All buttons for model selection in settings page
- [x] Fix WebSocket upgrade - add http.Hijacker to statusResponseWriter (1a5ae20)
- [x] Rename project from gemini-proxy to ai-gateway (8e3195e)
- [x] Multi-backend provider architecture (3688530)
  - Provider interface with implementations for Gemini, OpenAI, Anthropic, Mistral, Ollama, LM Studio
  - Config redesigned: providers map replaces single gemini section
  - Old config format auto-migrated on load
- [x] Per-client backend routing via provider registry (cfcae4e)
- [x] Per-client system prompt injection (cfcae4e)
- [x] Per-client base URL override for local backends (cfcae4e)
- [x] Admin UI for backend selection, system prompt, and multi-provider settings (4201da0)
- [x] Revamp README with full feature documentation
- [x] Add Perplexity AI, xAI/Grok, Cohere, Azure OpenAI providers (f6fb2f4)
- [x] GitHub Actions CI/CD: build on push, release binaries on tag
- [x] Makefile for local build and cross-compilation
- [x] config.example.yaml for safe distribution

### Next Steps
- Test streaming with all provider backends
- Add provider health check to dashboard
- Add ability to remove providers from settings UI
- Add model list fetching for non-Gemini providers
- Add request log filtering by backend provider
- Dockerfile for container deployments
