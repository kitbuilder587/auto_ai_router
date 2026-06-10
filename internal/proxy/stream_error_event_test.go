package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractStreamErrorEvent(t *testing.T) {
	tests := []struct {
		name    string
		chunk   string
		wantErr bool
	}{
		{
			name:    "openai mid-stream error",
			chunk:   `data: {"error":{"message":"The server had an error","type":"server_error"}}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "anthropic error event",
			chunk:   "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
			wantErr: true,
		},
		{
			name:    "responses api failed event",
			chunk:   `data: {"type":"response.failed","response":{"status":"failed"}}` + "\n\n",
			wantErr: true,
		},
		{
			name:    "bare json error (non-SSE chunk)",
			chunk:   `{"error":{"message":"quota exceeded","code":429}}`,
			wantErr: true,
		},
		{
			name:    "normal content chunk",
			chunk:   `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hello"}}]}` + "\n\n",
			wantErr: false,
		},
		{
			name:    "usage chunk followed by DONE",
			chunk:   `data: {"id":"chatcmpl-1","choices":[],"usage":{"total_tokens":15}}` + "\ndata: [DONE]\n\n",
			wantErr: false,
		},
		{
			name:    "explicit null error field",
			chunk:   `data: {"id":"chatcmpl-1","error":null,"choices":[]}` + "\n\n",
			wantErr: false,
		},
		{
			name:    "done only",
			chunk:   "data: [DONE]\n\n",
			wantErr: false,
		},
		{
			name:    "empty chunk",
			chunk:   "",
			wantErr: false,
		},
		{
			name:    "content mentioning the word error is not an error",
			chunk:   `data: {"choices":[{"delta":{"content":"an error occurred in your code"}}]}` + "\n\n",
			wantErr: false,
		},
		{
			name:    "error event followed by DONE in same chunk",
			chunk:   `data: {"error":{"message":"rate limited"}}` + "\ndata: [DONE]\n\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStreamErrorEvent([]byte(tt.chunk))
			if tt.wantErr {
				assert.NotEmpty(t, got, "expected error event to be detected")
			} else {
				assert.Empty(t, got, "expected no error event, got: %s", got)
			}
		})
	}
}
