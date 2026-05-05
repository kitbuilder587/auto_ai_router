package bedrockresponses

import (
	anthropicresponses "github.com/mixaill76/auto_ai_router/internal/converter/anthropic/responses"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
)

// BedrockToResponsesResponse converts a Bedrock Anthropic response body to a responses.Response.
// Bedrock returns the same Anthropic Messages API JSON format, so we reuse the Anthropic converter.
func BedrockToResponsesResponse(body []byte, displayModelID string) (*responses.Response, error) {
	return anthropicresponses.AnthropicToResponsesResponse(body, displayModelID, "", 0)
}
