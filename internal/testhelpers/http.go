package testhelpers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// APIErrorResponse mirrors proxy.APIErrorResponse for test assertions.
type APIErrorResponse struct {
	Error APIError `json:"error"`
}

// APIError mirrors proxy.APIError for test assertions.
type APIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

// AssertJSONErrorResponse decodes the JSON response from the recorder and
// verifies the HTTP status, error type, and error message.
func AssertJSONErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, expectedStatus int, expectedType, expectedMsg string) {
	t.Helper()

	assert.Equal(t, expectedStatus, recorder.Code)
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))

	var resp APIErrorResponse
	err := json.NewDecoder(recorder.Body).Decode(&resp)
	require.NoError(t, err, "failed to decode JSON error response")

	assert.Equal(t, expectedType, resp.Error.Type)
	assert.Equal(t, expectedMsg, resp.Error.Message)
}

// ResponseBuilder provides a fluent interface for building HTTP responses in tests.
type ResponseBuilder struct {
	statusCode int
	headers    http.Header
	body       interface{}
}

// NewResponseBuilder creates a new response builder with status 200 OK.
func NewResponseBuilder() *ResponseBuilder {
	return &ResponseBuilder{
		statusCode: http.StatusOK,
		headers:    make(http.Header),
	}
}

// WithStatus sets the HTTP status code.
func (rb *ResponseBuilder) WithStatus(code int) *ResponseBuilder {
	rb.statusCode = code
	return rb
}

// WithJSONBody sets the response body to a JSON-encoded value and sets Content-Type to application/json.
func (rb *ResponseBuilder) WithJSONBody(body interface{}) *ResponseBuilder {
	rb.body = body
	rb.headers.Set("Content-Type", "application/json")
	return rb
}

// Write writes the response to the http.ResponseWriter.
func (rb *ResponseBuilder) Write(w http.ResponseWriter) error {
	for key, values := range rb.headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(rb.statusCode)
	if rb.body != nil {
		return json.NewEncoder(w).Encode(rb.body)
	}
	return nil
}
