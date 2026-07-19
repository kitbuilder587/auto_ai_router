package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSuccessfulResponseModelForEveryModelBearingRoute(t *testing.T) {
	for _, endpoint := range []string{
		"/v1/completions",
		"/v1/embeddings",
		"/v1/responses",
	} {
		t.Run(endpoint, func(t *testing.T) {
			normalized := normalizeSuccessfulResponseModel(
				[]byte(`{"object":"fixture","model":"provider/backend","data":[]}`),
				endpoint,
				"client/requested-model",
			)
			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(normalized, &body))
			assert.Equal(t, "client/requested-model", body["model"])
		})
	}
}

func TestNormalizeSuccessfulResponseModelDoesNotInventImageModel(t *testing.T) {
	body := []byte(`{"created":1,"data":[{"url":"https://images.invalid/1"}]}`)
	assert.Equal(
		t,
		body,
		normalizeSuccessfulResponseModel(body, "/v1/images/generations", "client/image-model"),
	)
}

func TestClientVisibleResponseModelPrefersTrustedChainIdentity(t *testing.T) {
	logCtx := &RequestLogContext{
		PublicModelID: "inner/body-model",
		Billing: NewBillingContext(
			"event",
			"call",
			"/v1/embeddings",
			shadowcontext.Identity{PublicModel: "outer/requested-model"},
		),
	}
	assert.Equal(
		t,
		"outer/requested-model",
		clientVisibleResponseModel(logCtx, "provider/backend"),
	)
}

func TestNormalizeSuccessfulResponseModelStreamHandlesFragmentedSSE(t *testing.T) {
	const publicModel = "client/requested-model"
	input := strings.Join([]string{
		"event: message\n",
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"provider/backend","choices":[]}` + "\n",
		"\n",
		`data: {"error":{"message":"unchanged"},"model":"provider/error-model"}` + "\n",
		"\n",
		"data: [DONE]\n\n",
	}, "")
	logCtx := &RequestLogContext{
		Billing: NewBillingContext("event", "call", "/v1/chat/completions", shadowcontext.Identity{}).
			WithPublicModel(publicModel),
	}

	normalized, err := io.ReadAll(normalizeSuccessfulResponseModelStream(
		iotest.OneByteReader(strings.NewReader(input)),
		http.StatusOK,
		logCtx,
		"provider/backend",
	))
	require.NoError(t, err)
	assert.Contains(t, string(normalized), `"model":"client/requested-model"`)
	assert.Contains(t, string(normalized), `{"error":{"message":"unchanged"},"model":"provider/error-model"}`)
	assert.True(t, strings.HasSuffix(string(normalized), "data: [DONE]\n\n"))
}

func TestNormalizeSuccessfulResponseModelStreamRewritesNestedResponsesModel(t *testing.T) {
	logCtx := &RequestLogContext{
		Billing: NewBillingContext("event", "call", "/v1/responses", shadowcontext.Identity{}).
			WithPublicModel("client/responses-model"),
	}
	input := `data: {"type":"response.completed","response":{"id":"resp_1","model":"provider/backend","status":"completed"}}` + "\n\n"

	normalized, err := io.ReadAll(normalizeSuccessfulResponseModelStream(
		strings.NewReader(input),
		http.StatusOK,
		logCtx,
		"provider/backend",
	))
	require.NoError(t, err)
	var event struct {
		Response struct {
			Model string `json:"model"`
		} `json:"response"`
	}
	dataLine := strings.Split(string(normalized), "\n")[0]
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &event))
	assert.Equal(t, "client/responses-model", event.Response.Model)
}

func TestCompletionStreamingResponseUsesRequestedModelForDirectAndChain(t *testing.T) {
	for _, tc := range []struct {
		name        string
		publicModel string
		identity    shadowcontext.Identity
	}{
		{name: "direct_air", publicModel: "direct/requested-model"},
		{
			name:        "litellm_chain",
			publicModel: "outer-litellm/model-alias",
			identity: shadowcontext.Identity{
				PublicModel: "outer-litellm/model-alias",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prx := NewTestProxyBuilder().Build()
			billing := NewBillingContext(
				"event", "call", "/v1/completions", tc.identity,
			)
			if tc.identity.PublicModel == "" {
				billing = billing.WithPublicModel(tc.publicModel)
			}
			logCtx := &RequestLogContext{
				Request: httptest.NewRequest(http.MethodPost, "/v1/completions", nil),
				Billing: billing,
			}
			providerStream := `data: {"id":"cmpl-1","object":"text_completion","model":"provider/backend","choices":[]}` + "\n\n" +
				"data: [DONE]\n\n"
			proxyResp := &ProxyResponse{
				StatusCode:  http.StatusOK,
				Headers:     http.Header{"Content-Type": {"text/event-stream"}},
				StreamBody:  io.NopCloser(iotest.OneByteReader(strings.NewReader(providerStream))),
				IsStreaming: true,
			}
			w := httptest.NewRecorder()

			_, err := prx.writeProxyStreamingResponseWithTokens(
				w,
				proxyResp,
				logCtx.Request,
				"proxy-credential",
				"canonical-backend",
				"provider/backend",
				logCtx,
			)
			require.NoError(t, err)
			assert.Contains(t, w.Body.String(), `"model":"`+tc.publicModel+`"`)
			assert.NotContains(t, w.Body.String(), `"model":"provider/backend"`)
		})
	}
}
