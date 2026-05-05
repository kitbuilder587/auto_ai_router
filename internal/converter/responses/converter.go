package responses

import (
	"io"

	"github.com/mixaill76/auto_ai_router/internal/config"
)

// ProviderResponses converts between the Responses API format and a specific
// provider's native request/response format without going through Chat Completions.
type ProviderResponses interface {
	// RequestFrom converts a Responses API request body to the provider's native
	// request format. Returns the converted body and its content type.
	RequestFrom(body []byte) (converted []byte, contentType string, err error)

	// ResponseTo converts a provider native response body to a responses.Response.
	// displayModelID is used to restore the alias the client originally requested.
	ResponseTo(body []byte, displayModelID string) (*Response, error)

	// StreamTo reads a provider SSE stream and writes Responses API SSE events to
	// writer. onComplete is called once with the final Response when the stream ends.
	StreamTo(reader io.Reader, writer io.Writer, displayModelID string,
		meta *ResponsesMetadata, onComplete func(*Response)) error

	// BuildURL constructs the upstream URL for the provider endpoint.
	BuildURL(cred *config.CredentialConfig) string
}
