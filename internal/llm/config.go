package llm

// BackendConfig holds configuration for a single LLM backend instance.
type BackendConfig struct {
	// Backend is the provider type. Supported values:
	//
	// Native APIs:
	//   anthropic, gemini, bedrock, ollama
	//
	// OpenAI-compatible (Bearer token auth, /v1/models discovery):
	//   openai, openrouter, together, groq, fireworks, mistral, ai21,
	//   huggingface, deepseek, cerebras, xai,
	//   litellm, lmstudio, jan, localai, vllm, anythingllm
	Backend string

	// APIKey is the authentication key or token for cloud backends.
	APIKey string

	// BaseURL overrides the default base URL for OpenAI-compatible backends.
	// For named backends (e.g. "openai"), this defaults from KnownBackends.
	// Required for custom/self-hosted OpenAI-compatible endpoints.
	BaseURL string

	// Model is the model ID to use. If empty, the first discovered model
	// that passes the allow/block filter is used.
	Model string

	// Region is the AWS region (e.g. "us-east-1"). Bedrock only.
	Region string

	// AWSKeyID is the AWS access key ID. Bedrock only.
	AWSKeyID string

	// AWSSecretKey is the AWS secret access key. Bedrock only.
	AWSSecretKey string

	// Allow is a list of regex patterns. If non-empty, only model IDs matching
	// at least one pattern are returned by DiscoverModels.
	Allow []string

	// Block is a list of regex patterns. Model IDs matching any pattern are
	// excluded from DiscoverModels results.
	Block []string
}
