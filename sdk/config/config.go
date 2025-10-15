// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

    // Access holds request authentication provider configuration.
    Access AccessConfig `yaml:"auth,omitempty" json:"auth,omitempty"`

    // Responses configures behavior specific to the OpenAI /v1/responses endpoint.
    Responses ResponsesConfig `yaml:"responses,omitempty" json:"responses,omitempty"`
}

// AccessConfig groups request authentication providers.
type AccessConfig struct {
	// Providers lists configured authentication providers.
	Providers []AccessProvider `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// AccessProvider describes a request authentication provider entry.
type AccessProvider struct {
	// Name is the instance identifier for the provider.
	Name string `yaml:"name" json:"name"`

	// Type selects the provider implementation registered via the SDK.
	Type string `yaml:"type" json:"type"`

	// SDK optionally names a third-party SDK module providing this provider.
	SDK string `yaml:"sdk,omitempty" json:"sdk,omitempty"`

	// APIKeys lists inline keys for providers that require them.
	APIKeys []string `yaml:"api-keys,omitempty" json:"api-keys,omitempty"`

	// Config passes provider-specific options to the implementation.
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

const (
	// AccessProviderTypeConfigAPIKey is the built-in provider validating inline API keys.
	AccessProviderTypeConfigAPIKey = "config-api-key"

	// DefaultAccessProviderName is applied when no provider name is supplied.
	DefaultAccessProviderName = "config-inline"
)

// ConfigAPIKeyProvider returns the first inline API key provider if present.
func (c *SDKConfig) ConfigAPIKeyProvider() *AccessProvider {
	if c == nil {
		return nil
	}
	for i := range c.Access.Providers {
		if c.Access.Providers[i].Type == AccessProviderTypeConfigAPIKey {
			if c.Access.Providers[i].Name == "" {
				c.Access.Providers[i].Name = DefaultAccessProviderName
			}
			return &c.Access.Providers[i]
		}
	}
	return nil
}

// MakeInlineAPIKeyProvider constructs an inline API key provider configuration.
// It returns nil when no keys are supplied.
func MakeInlineAPIKeyProvider(keys []string) *AccessProvider {
    if len(keys) == 0 {
        return nil
    }
    provider := &AccessProvider{
        Name:    DefaultAccessProviderName,
        Type:    AccessProviderTypeConfigAPIKey,
        APIKeys: append([]string(nil), keys...),
    }
    return provider
}

// ResponsesConfig groups defaults and feature flags for the /v1/responses endpoint.
type ResponsesConfig struct {
    // Defaults configures default values injected into requests when not provided by clients.
    Defaults ResponsesDefaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
    // InferEffortFromModelSuffix enables mapping model suffixes (e.g., gpt-5-high)
    // to reasoning.effort and stripping the suffix from the model.
    // When true, and the model family is supported, the suffix is converted into
    // reasoning.effort unless the client already set reasoning.effort explicitly.
    InferEffortFromModelSuffix bool `yaml:"infer-effort-from-model-suffix,omitempty" json:"infer-effort-from-model-suffix,omitempty"`
}

// ResponsesDefaults defines injectable defaults for /v1/responses.
type ResponsesDefaults struct {
    // Verbosity controls output verbosity for text responses: low|medium|high
    Verbosity string `yaml:"verbosity,omitempty" json:"verbosity,omitempty"`
    // ReasoningSummary selects level of reasoning summary returned: auto|detailed
    ReasoningSummary string `yaml:"reasoning-summary,omitempty" json:"reasoning-summary,omitempty"`
}
