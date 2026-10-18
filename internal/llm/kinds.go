// Package llm is the model catalog: providers (where models are served, with
// their API keys), the models they offer and which teams may use them, the
// model proxy agents call when a provider is in proxy mode, and model usage.
//
// Platform admins manage providers and models; teams pick from the models
// published to them, in templates and agents. A provider's API key never
// leaves the platform in proxy mode: agents call the platform's gateway with
// their own credential, and the platform adds the key, checks the agent may
// use the model, and counts the tokens. In direct mode the agent gets the
// provider's URL and key and calls it itself (for gateways that issue scoped
// keys, or that agents may reach directly).
package llm

// Kind is a type of provider: which API it speaks and how it authenticates.
type Kind struct {
	ID   string
	Name string
	// API is the Pi API the agent runtime uses (MODEL_API).
	API string
	// DefaultBaseURL is used when a provider of this kind is created without one.
	DefaultBaseURL string
	// AuthHeader is how the key is sent: "authorization" (Bearer) or "x-api-key".
	AuthHeader string
	// ProxyPaths are the request paths, after the base URL, the proxy relays.
	ProxyPaths []string
	// ModelsPath lists the provider's models (discovery).
	ModelsPath string
}

// Kinds are the provider types, in the order the console offers them.
var Kinds = []Kind{
	{ID: "openai", Name: "OpenAI", API: "openai-responses", DefaultBaseURL: "https://api.openai.com/v1",
		AuthHeader: "authorization", ProxyPaths: []string{"/responses"}, ModelsPath: "/models"},
	{ID: "anthropic", Name: "Anthropic", API: "anthropic-messages", DefaultBaseURL: "https://api.anthropic.com",
		AuthHeader: "x-api-key", ProxyPaths: []string{"/v1/messages"}, ModelsPath: "/v1/models"},
	{ID: "openai-compatible", Name: "OpenAI-compatible (chat completions)", API: "openai-completions",
		AuthHeader: "authorization", ProxyPaths: []string{"/chat/completions"}, ModelsPath: "/models"},
	{ID: "openai-responses", Name: "OpenAI Responses-compatible", API: "openai-responses",
		AuthHeader: "authorization", ProxyPaths: []string{"/responses"}, ModelsPath: "/models"},
	{ID: "anthropic-compatible", Name: "Anthropic-compatible", API: "anthropic-messages",
		AuthHeader: "x-api-key", ProxyPaths: []string{"/v1/messages"}, ModelsPath: "/v1/models"},
}

// KindByID returns a provider kind.
func KindByID(id string) (Kind, bool) {
	for _, k := range Kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// Access modes.
const (
	ModeProxy  = "proxy"
	ModeDirect = "direct"
)
