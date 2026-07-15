package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamToClient_CapturesTTFT verifies that streamToClient sets
// RequestLogContext.CompletionStartTime the moment it reads the first
// non-empty chunk from the upstream reader — this is the TTFT capture point
// shared by every streaming handler.
func TestStreamToClient_CapturesTTFT(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	w := httptest.NewRecorder()

	logCtx := &RequestLogContext{StartTime: time.Now().Add(-50 * time.Millisecond)}
	reader := strings.NewReader("data: hello\n\ndata: world\n\n")

	err := prx.streamToClient(context.Background(), w, reader, "cred1", "gpt-4o", "/v1/chat/completions", nil, nil, logCtx)
	require.NoError(t, err)

	assert.False(t, logCtx.CompletionStartTime.IsZero(), "CompletionStartTime should be set after streaming a non-empty chunk")
	assert.True(t, logCtx.CompletionStartTime.After(logCtx.StartTime), "TTFT timestamp should be after the request start time")
}

// TestStreamToClient_TTFTNotOverwrittenOnSubsequentChunks verifies the
// IsZero() guard: once CompletionStartTime is set, later chunks (or repeat
// calls, as happens on fallback/retry paths) must not overwrite it.
func TestStreamToClient_TTFTNotOverwrittenOnSubsequentChunks(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	w := httptest.NewRecorder()

	preset := time.Now().Add(-time.Hour)
	logCtx := &RequestLogContext{StartTime: preset.Add(-time.Minute), CompletionStartTime: preset}
	reader := strings.NewReader("data: hello\n\ndata: world\n\n")

	err := prx.streamToClient(context.Background(), w, reader, "cred1", "gpt-4o", "/v1/chat/completions", nil, nil, logCtx)
	require.NoError(t, err)

	assert.Equal(t, preset, logCtx.CompletionStartTime, "an already-set CompletionStartTime must not be overwritten")
}

// TestStreamToClient_NilLogCtx verifies streaming still works when no
// RequestLogContext is supplied (e.g. raw proxy passthrough paths that don't
// track TTFT).
func TestStreamToClient_NilLogCtx(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	w := httptest.NewRecorder()
	reader := strings.NewReader("data: hello\n\n")

	assert.NotPanics(t, func() {
		err := prx.streamToClient(context.Background(), w, reader, "cred1", "gpt-4o", "/v1/chat/completions", nil, nil, nil)
		require.NoError(t, err)
	})
}
