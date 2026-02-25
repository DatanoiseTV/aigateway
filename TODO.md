# TODO - Gemini Proxy Gateway

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

### Next Steps
- Test the application to verify key type selection works
- Add client-side showing the key type in the clients list
- Test streaming with various OpenAI-compatible clients
