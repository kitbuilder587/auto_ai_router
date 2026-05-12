// Package vertexresponses implements the ProviderResponses interface for Vertex AI (Gemini).
// It converts Responses API requests/responses directly to/from the Vertex AI
// GenerateContent API without going through Chat Completions format.
package vertexresponses

import (
	"io"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
	"github.com/mixaill76/auto_ai_router/internal/converter/vertex"
)

func init() {
	responses.RegisterProviderResponses(config.ProviderTypeVertexAI, func(mode responses.ResponsesRequestMode) responses.ProviderResponses {
		return &VertexResponses{mode: mode}
	})
	responses.RegisterProviderResponses(config.ProviderTypeGemini, func(mode responses.ResponsesRequestMode) responses.ProviderResponses {
		return &VertexResponses{mode: mode, gemini: true}
	})
}

// VertexResponses converts between the Responses API and Vertex AI GenerateContent API.
type VertexResponses struct {
	mode   responses.ResponsesRequestMode
	gemini bool // true → Gemini AI Studio API; false → Vertex AI API
}

// RequestFrom converts a Responses API request body to Vertex AI JSON format.
func (v *VertexResponses) RequestFrom(body []byte) ([]byte, string, error) {
	converted, err := ResponsesRequestToVertex(body, v.mode.ModelID)
	if err != nil {
		return nil, "", err
	}
	return converted, "application/json", nil
}

// ResponseTo converts a Vertex AI GenerateContent response to a responses.Response.
func (v *VertexResponses) ResponseTo(body []byte, displayModelID string) (*responses.Response, error) {
	model := displayModelID
	if model == "" {
		model = v.mode.DisplayModel()
	}
	return VertexToResponsesResponse(body, model, "", 0)
}

// StreamTo reads a Vertex AI SSE stream and writes Responses API SSE events.
func (v *VertexResponses) StreamTo(
	reader io.Reader,
	writer io.Writer,
	displayModelID string,
	meta *responses.ResponsesMetadata,
	onComplete func(*responses.Response),
) error {
	model := displayModelID
	if model == "" {
		model = v.mode.DisplayModel()
	}
	return TransformVertexStreamToResponses(reader, writer, model, "", meta, onComplete)
}

// BuildURL constructs the Vertex AI upstream URL for the given credential.
func (v *VertexResponses) BuildURL(cred *config.CredentialConfig) string {
	if v.gemini {
		return vertex.BuildGeminiURL(cred, v.mode.ModelID, v.mode.IsStreaming)
	}
	return vertex.BuildVertexURL(cred, v.mode.ModelID, v.mode.IsStreaming)
}
