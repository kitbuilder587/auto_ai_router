package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	litellmdbmodels "github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingHandlersDetectFragmentedTerminalErrorsAcrossReads(t *testing.T) {
	terminalChunks := []string{
		"event: error\n",
		`data: {"ty`,
		`pe":"error","error":{"type":"overloaded_error","message":"fragmented terminal failure"}}` + "\n\n",
	}
	normalOutput := `data: {"id":"chatcmpl-normalized","choices":[{"delta":{"content":"normalized"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	outputErrorChunks := []string{
		`data: {"type":"response.`,
		`failed","response":{"id":"resp-output-failed","status":"failed"}}` + "\n\n",
	}

	tests := []struct {
		name       string
		run        func(*Proxy, http.ResponseWriter, *http.Response, *RequestLogContext) error
		input      []string
		wantBody   string
		wantMarker string
	}{
		{
			name: "direct passthrough observes raw provider frames",
			run: func(p *Proxy, w http.ResponseWriter, resp *http.Response, logCtx *RequestLogContext) error {
				return p.handleStreamingWithTokens(w, resp, "direct-openai", "gpt-4o-mini", logCtx)
			},
			input:      terminalChunks,
			wantBody:   strings.Join(terminalChunks, ""),
			wantMarker: "fragmented terminal failure",
		},
		{
			name: "transformed stream retains raw provider failure after normalization",
			run: func(p *Proxy, w http.ResponseWriter, resp *http.Response, logCtx *RequestLogContext) error {
				transformer := func(r io.Reader, _ string, output io.Writer) error {
					if _, err := io.Copy(io.Discard, r); err != nil {
						return err
					}
					_, err := io.WriteString(output, normalOutput)
					return err
				}
				return p.handleTransformedStreaming(w, resp, "direct-anthropic", "claude", "Anthropic", transformer, logCtx)
			},
			input:      terminalChunks,
			wantBody:   normalOutput,
			wantMarker: "fragmented terminal failure",
		},
		{
			name: "transformed stream observes fragmented emitted failure",
			run: func(p *Proxy, w http.ResponseWriter, resp *http.Response, logCtx *RequestLogContext) error {
				transformer := func(r io.Reader, _ string, output io.Writer) error {
					if _, err := io.Copy(io.Discard, r); err != nil {
						return err
					}
					for _, chunk := range outputErrorChunks {
						if _, err := io.WriteString(output, chunk); err != nil {
							return err
						}
					}
					return nil
				}
				return p.handleTransformedStreaming(w, resp, "direct-converted", "gpt-4o-mini", "converted", transformer, logCtx)
			},
			input:      []string{"data: [DONE]\n\n"},
			wantBody:   strings.Join(outputErrorChunks, ""),
			wantMarker: "response.failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prx := NewTestProxyBuilder().Build()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			credential := &config.CredentialConfig{Name: "terminal-provider", Type: config.ProviderTypeOpenAI, BaseURL: "https://provider.invalid"}
			logCtx := &RequestLogContext{
				RequestID:   "terminal-handler",
				StartTime:   time.Now().UTC(),
				Request:     request,
				Credential:  credential,
				ModelID:     "gpt-4o-mini",
				RealModelID: "gpt-4o-mini",
				TargetURL:   credential.BaseURL,
			}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body:       &fragmentedStreamReadCloser{chunks: stringChunks(tt.input)},
				Request:    request,
			}
			w := httptest.NewRecorder()

			err := tt.run(prx, w, resp, logCtx)

			var terminalErr proxyProviderStreamError
			require.ErrorAs(t, err, &terminalErr)
			assert.Equal(t, tt.wantBody, w.Body.String())
			assert.Equal(t, "failure", logCtx.Status)
			assert.Equal(t, http.StatusOK, logCtx.HTTPStatus)
			assert.Equal(t, "stream_error", logCtx.StreamOutcome)
			assert.Contains(t, logCtx.ErrorMsg, tt.wantMarker)
		})
	}
}

func TestProxyRequestPassthroughResponsesTerminalSemanticsForDirectAndProxyCredentials(t *testing.T) {
	failedChunks := []string{
		"event: response.failed\n",
		`data: {"type":"response.`,
		`failed","response":{"id":"resp-failed","status":"failed","error":{"message":"responses terminal failure"}}}` + "\n\n",
	}
	completedChunks := []string{
		"event: response.completed\n",
		`data: {"type":"response.com`,
		`pleted","response":{"id":"resp-completed","object":"response","created_at":1,"status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{},"output_tokens_details":{}}}}` + "\n\n",
	}

	for _, providerType := range []config.ProviderType{config.ProviderTypeOpenAI, config.ProviderTypeProxy} {
		for _, streamCase := range []struct {
			name        string
			chunks      []string
			wantStatus  string
			wantOutcome string
			wantSession bool
		}{
			{name: "fragmented response.failed", chunks: failedChunks, wantStatus: "failure", wantOutcome: "stream_error"},
			{name: "normal response.completed", chunks: completedChunks, wantStatus: "success", wantOutcome: "completed", wantSession: true},
		} {
			t.Run(string(providerType)+"/"+streamCase.name, func(t *testing.T) {
				events := []string{}
				sink := &keySpendTestSink{events: &events}
				prx := NewTestProxyBuilder().
					WithSingleCredential("responses-upstream", providerType, "https://responses.invalid", "upstream-key").
					WithSessionSticky(time.Minute).
					Build()
				prx.spendLogger = sink
				prx.client = &http.Client{Transport: fragmentedStreamingRoundTripper{
					statusCode: http.StatusOK,
					header:     http.Header{"Content-Type": {"text/event-stream"}},
					chunks:     stringChunks(streamCase.chunks),
				}}

				const sessionID = "responses-terminal-session"
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
					`{"model":"gpt-5","input":"hello","stream":true,"session_id":"`+sessionID+`"}`,
				))
				request.Header.Set("Authorization", "Bearer master-key")
				request.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				prx.ProxyRequest(w, request)

				require.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, strings.Join(streamCase.chunks, ""), w.Body.String(), "native Responses SSE must remain byte-for-byte passthrough")
				require.Len(t, sink.replayed, 1)
				assert.Equal(t, streamCase.wantStatus, sink.replayed[0].Status)
				metadata := decodeSpendMetadata(t, sink.replayed[0])
				extension := metadata["spend_logs_metadata"].(map[string]any)
				assert.Equal(t, streamCase.wantOutcome, extension["outcome"])
				_, hasSession := prx.sessionStore.Get(sessionID, "gpt-5")
				assert.Equal(t, streamCase.wantSession, hasSession)
				if streamCase.wantStatus == "failure" {
					errorInfo := metadata["error_information"].(map[string]any)
					assert.Equal(t, float64(http.StatusOK), errorInfo["error_code"])
					assert.Contains(t, errorInfo["error_message"], "responses terminal failure")
				}
			})
		}
	}
}

type fragmentedStreamingRoundTripper struct {
	statusCode int
	header     http.Header
	chunks     [][]byte
}

func (rt fragmentedStreamingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	chunks := make([][]byte, len(rt.chunks))
	for i, chunk := range rt.chunks {
		chunks[i] = append([]byte(nil), chunk...)
	}
	return &http.Response{
		StatusCode:    rt.statusCode,
		Header:        rt.header.Clone(),
		Body:          &fragmentedStreamReadCloser{chunks: chunks},
		ContentLength: -1,
		Request:       request,
	}, nil
}

func decodeSpendMetadata(t *testing.T, entry *litellmdbmodels.SpendLogEntry) map[string]any {
	t.Helper()
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	return metadata
}
