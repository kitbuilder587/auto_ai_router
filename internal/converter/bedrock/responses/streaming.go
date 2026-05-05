package bedrockresponses

import (
	"io"

	converter "github.com/mixaill76/auto_ai_router/internal/converter"
	anthropicresponses "github.com/mixaill76/auto_ai_router/internal/converter/anthropic/responses"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
)

// TransformBedrockStreamToResponses converts a Bedrock EventStream binary response to
// Responses API SSE events. The pipeline is:
//
//	Bedrock EventStream (binary) → Anthropic SSE events → Responses API SSE events
//
// This avoids the Chat Completions intermediate layer used by the converted path.
func TransformBedrockStreamToResponses(
	reader io.Reader,
	writer io.Writer,
	model string,
	meta *responses.ResponsesMetadata,
	onComplete func(*responses.Response),
) error {
	pr, pw := io.Pipe()

	go func() {
		err := converter.DecodeEventStreamToSSE(reader, pw)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
	}()

	return anthropicresponses.TransformAnthropicStreamToResponses(pr, writer, model, "", meta, onComplete)
}
