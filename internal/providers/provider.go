package providers

import (
	"fmt"
	"net/http"

	"ai-gateway/internal/config"
)

// Provider is the interface all upstream AI backends implement.
// Both streaming and non-streaming requests go through this interface.
type Provider interface {
	// Name returns the provider identifier (e.g. "gemini", "openai")
	Name() string

	// ChatCompletion sends a non-streaming request and returns the raw response body,
	// HTTP status code, and any error. The messages follow a simplified internal format.
	ChatCompletion(req *ChatRequest) (responseBody []byte, statusCode int, err error)

	// ChatCompletionStream sends a streaming request and returns the raw HTTP response
	// for SSE reading. The caller is responsible for closing the response body.
	ChatCompletionStream(req *ChatRequest) (resp *http.Response, err error)

	// ParseResponse extracts the generated text from a non-streaming response body.
	ParseResponse(body []byte) (text string, inputTokens int, outputTokens int, err error)

	// ParseStreamChunk extracts text and token counts from a single SSE chunk.
	// Returns the text delta and updated token counts.
	ParseStreamChunk(data []byte) (text string, inputTokens int, outputTokens int)

	// StreamDataPrefix returns the SSE line prefix this provider uses (e.g. "data: ")
	StreamDataPrefix() string

	// Models returns the list of allowed/available models for this provider.
	Models() []string

	// DefaultModel returns the default model for this provider.
	DefaultModel() string

	// TestConnection verifies the provider's API is reachable.
	TestConnection() (message string, ok bool, err error)

	// FetchModels fetches available models from the backend API.
	// Returns nil if not supported or fetch fails.
	FetchModels() ([]string, error)

	// ParseToolCalls extracts tool calls from a non-streaming response body.
	// Returns nil if no tool calls are present.
	ParseToolCalls(body []byte) ([]ToolCall, error)

	// ParseStreamToolCall extracts tool call and finish_reason from a streaming chunk.
	// Returns (toolCall, finishReason). If no tool call in chunk, returns (nil, finishReason).
	// The toolCall is provider-specific (e.g., *StreamToolCall for OpenAICompatProvider)
	ParseStreamToolCall(data []byte) (interface{}, string)
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ChatRequest is the internal representation of a chat completion request
// that gets translated into each provider's native format.
type ChatRequest struct {
	Model          string         `json:"model"`
	Messages       []ChatMessage  `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Temperature    float64        `json:"temperature,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	Tools          []Tool         `json:"tools,omitempty"`
	ResponseFormat any            `json:"response_format,omitempty"`
	StreamOptions  *StreamOptions `json:"stream_options,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Tool represents a function tool that the model can call.
type Tool struct {
	Type     string        `json:"type"`
	Function *ToolFunction `json:"function,omitempty"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall represents a tool call requested by the model
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Registry holds all configured provider instances, keyed by their config name.
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider instance under the given name.
func (r *Registry) Register(name string, p Provider) {
	r.providers[name] = p
}

// Get returns the provider registered under the given name.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", name)
	}
	return p, nil
}

// GetWithOverride returns a provider, optionally creating a derived instance
// with a custom base URL. Used for per-client URL overrides.
func (r *Registry) GetWithOverride(name, baseURLOverride string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not configured", name)
	}
	if baseURLOverride == "" {
		return p, nil
	}
	// For providers that support URL overrides, create a clone with the new URL
	if cloneable, ok := p.(URLOverridable); ok {
		return cloneable.WithBaseURL(baseURLOverride), nil
	}
	return p, nil
}

// Names returns a list of all registered provider names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// URLOverridable is implemented by providers that support per-client base URL overrides.
type URLOverridable interface {
	WithBaseURL(url string) Provider
}

// BuildRegistry creates a provider registry from the config's providers section.
func BuildRegistry(cfg *config.Config) *Registry {
	reg := NewRegistry()

	for name, pcfg := range cfg.Providers {
		p := buildProvider(name, pcfg)
		reg.Register(name, p)
	}

	return reg
}

// BuildSingleProvider creates a provider instance from a single ProviderConfig.
// Used for per-client provider instances when the client has their own API key.
func BuildSingleProvider(name string, pcfg config.ProviderConfig) (Provider, error) {
	p := buildProvider(name, pcfg)
	if p == nil {
		return nil, fmt.Errorf("unknown provider type: %s", pcfg.Type)
	}
	return p, nil
}

func buildProvider(name string, pcfg config.ProviderConfig) Provider {
	switch pcfg.Type {
	case "gemini":
		return NewGeminiProvider(pcfg)
	case "openai":
		return NewOpenAIProvider(name, pcfg)
	case "anthropic":
		return NewAnthropicProvider(pcfg)
	case "mistral":
		return NewMistralProvider(pcfg)
	case "ollama":
		return NewOllamaProvider(name, pcfg)
	case "lmstudio":
		return NewLMStudioProvider(name, pcfg)
	case "perplexity":
		return NewPerplexityProvider(pcfg)
	case "xai":
		return NewXAIProvider(pcfg)
	case "cohere":
		return NewCohereProvider(pcfg)
	case "azure-openai":
		return NewAzureOpenAIProvider(pcfg)
	case "vllm":
		return NewVLLMProvider(pcfg)
	case "openrouter":
		return NewOpenRouterProvider(pcfg)
	default:
		// Treat unknown types as OpenAI-compatible
		return NewOpenAIProvider(name, pcfg)
	}
}
