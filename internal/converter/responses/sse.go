package responses

import (
	"encoding/json"
	"fmt"
	"io"
)

// BuildInProgressResponse returns the map[string]interface{} payload used in
// response.created and response.in_progress SSE events.
//
// The caller is responsible for adding any provider-specific fields (e.g.
// "store", "previous_response_id") to the returned map after this call.
func BuildInProgressResponse(responseID, model string, createdAt int64) map[string]interface{} {
	return map[string]interface{}{
		"id":         responseID,
		"object":     "response",
		"created_at": createdAt,
		"model":      model,
		"status":     "in_progress",
		"output":     []interface{}{},
		"metadata":   map[string]interface{}{},
	}
}

// CompletedResponseParams holds the pre-computed, provider-independent fields
// needed to assemble a final *Response at stream completion.  The caller is
// responsible for all provider-specific mapping (stop-reason → Status/
// IncompleteDetails, raw chunks → Output, raw usage → *Usage) before filling
// this struct.
type CompletedResponseParams struct {
	ID        string
	Model     string
	CreatedAt int64

	// Pre-computed status fields.
	Status            string
	IncompleteDetails *IncompleteDetails

	// Pre-built output items.
	Output []OutputItem

	// Pre-computed token usage (may be nil).
	Usage *Usage

	// Metadata and conversation linkage.
	Metadata           map[string]string
	PreviousResponseID interface{} // nil or string

	// CompletedAt is set by providers that track a completion timestamp.
	// Leave nil to omit (e.g. Chat Completions path).
	CompletedAt *int64

	// Store and ToolChoice are only populated by the Chat Completions path.
	Store      bool
	ToolChoice interface{}
}

// BuildCompletedResponse constructs the typed *Response for a stream-completed
// event from pre-computed, provider-neutral fields.  All provider-specific
// conversions (stop-reasons, usage shapes, output item types) must be done by
// the caller before invoking this function.
func BuildCompletedResponse(p CompletedResponseParams) *Response {
	metadata := p.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	output := p.Output
	if output == nil {
		output = []OutputItem{}
	}
	return &Response{
		ID:                 p.ID,
		Object:             "response",
		CreatedAt:          p.CreatedAt,
		CompletedAt:        p.CompletedAt,
		Model:              p.Model,
		Status:             p.Status,
		IncompleteDetails:  p.IncompleteDetails,
		Output:             output,
		Usage:              p.Usage,
		Error:              nil,
		Metadata:           metadata,
		Tools:              []Tool{},
		ParallelToolCalls:  true,
		PreviousResponseID: p.PreviousResponseID,
		Instructions:       nil,
		Store:              p.Store,
		ToolChoice:         p.ToolChoice,
	}
}

// WriteSSEEvent JSON-marshals data, increments *seqNum, and writes a single
// SSE frame in the format:
//
//	event: <eventType>\ndata: <json>\n\n
//
// seqNum must not be nil. The caller owns the sequenceNumber field inside its
// own accumulator and passes a pointer so the counter stays in sync.
func WriteSSEEvent(w io.Writer, eventType string, data interface{}, seqNum *int) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("WriteSSEEvent marshal %s: %w", eventType, err)
	}
	*seqNum++
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
	return err
}
