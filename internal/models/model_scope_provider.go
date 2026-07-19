package models

import "regexp"

// liteLLMBedrockClaudeModelPattern mirrors the "bedrock-claude-ids" routing
// rule in the pinned LiteLLM fallback_generalizations: a case-insensitive,
// unanchored re.search for the dotted "anthropic.claude-" segment, so bare
// (anthropic.claude-...), region-prefixed (us./eu./au./jp./apac.) and
// global.-prefixed Bedrock Claude IDs of every version route to bedrock
// (which also covers LiteLLM's bedrock_converse model list).
var liteLLMBedrockClaudeModelPattern = regexp.MustCompile(`(?i)anthropic\.claude-`)

// liteLLMFutureClaudeModelPattern mirrors the "anthropic-claude-ids" routing
// rule in the pinned LiteLLM fallback_generalizations: a bare Claude
// family-major ID with an optional minor (dash or dot separated) and an
// optional 8-digit date suffix, anchored to the whole name, so
// claude-newfamily-5 routes like claude-newfamily-5-1 does. Other short model
// IDs are accepted only when they are present in the pinned/current model
// registry subset used by this deployment. Unknown IDs deliberately fail
// closed.
var liteLLMFutureClaudeModelPattern = regexp.MustCompile(`(?i)^claude-[a-z]+-\d+(?:[-.]\d+)?(?:-\d{8})?$`)

// inferLiteLLMShortModelProvider returns the provider prefix that pinned
// LiteLLM's get_llm_provider assigns to a provider-less model ID. This is
// intentionally independent of AIR's routing credential: an OpenAI-compatible
// transport can carry Claude or Gemini without making those models OpenAI ACL
// resources.
//
// Keep this list conservative. A false negative denies a provider-wildcard
// shortcut while the exact public model ID still works; a false positive would
// grant model access that LiteLLM denies.
func inferLiteLLMShortModelProvider(modelID string) (string, bool) {
	switch modelID {
	// OpenAI models present in the pinned model registry and current AIR model
	// inventory. Synthetic suffixes (for example "-retry") are not inferred.
	case "gpt-4.1",
		"gpt-4.1-mini",
		"gpt-4.1-nano",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-5",
		"gpt-5-chat",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5.1",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.4-nano",
		"gpt-5.4-pro",
		"gpt-5.5",
		"gpt-image-1",
		"gpt-image-1-mini",
		"text-embedding-3-large",
		"text-embedding-3-small":
		return "openai", true

	// Bare Gemini IDs in the pinned registry are Vertex AI language or
	// embedding models. Explicit "gemini/..." IDs do not reach this short-ID
	// fallback because they already contain a provider prefix.
	case "gemini-2.5-flash",
		"gemini-2.5-pro",
		"gemini-3-flash-preview",
		"gemini-3.1-flash-lite",
		"gemini-3.1-pro-preview",
		"gemini-3.1-pro-preview-customtools",
		"gemini-3.5-flash",
		"gemini-embedding-001":
		return "vertex_ai", true

	// Representative provider-less Bedrock IDs exercised by the current
	// endpoint/model surface. These are exact registry entries; arbitrary
	// "anthropic.*" or "amazon.*" strings must not inherit Bedrock access.
	case "anthropic.claude-3-5-sonnet-20240620-v1:0",
		"anthropic.claude-3-5-sonnet-20241022-v2:0",
		"anthropic.claude-3-7-sonnet-20250219-v1:0",
		"amazon.titan-text-lite-v1":
		return "bedrock", true
	}

	// The Bedrock rule is consulted before the bare-id Anthropic fallback,
	// matching the rule order in LiteLLM's fallback_generalizations.
	if liteLLMBedrockClaudeModelPattern.MatchString(modelID) {
		return "bedrock", true
	}

	if liteLLMFutureClaudeModelPattern.MatchString(modelID) {
		return "anthropic", true
	}
	return "", false
}
