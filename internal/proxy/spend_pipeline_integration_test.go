//go:build integration

package proxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mixaill76/auto_ai_router/internal/config"
	aimodels "github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/mixaill76/auto_ai_router/internal/spendsink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const proxyIntegrationKeyHash = "cc557cce629a1cb98664b98a3d5f5600a90a91c5955c4fdddfa4d13c94bfdcd6"

type pipelineRow struct {
	RequestID               string
	AirEventID              string
	CallType                string
	Status                  string
	Model                   string
	ModelID                 string
	ModelGroup              string
	Provider                string
	APIBase                 string
	TeamID                  string
	PromptTokens            int
	CompletionTokens        int
	Spend                   float64
	CallID                  string
	ContextState            string
	UsageSource             string
	CostStatus              string
	Outcome                 string
	ActualCredential        string
	Attempts                int
	ComparisonEligible      bool
	CompletionStarted       bool
	MessagesEmptyObject     bool
	ResponseEmptyObject     bool
	ProxyRequestEmptyObject bool
}

func TestShadowPipeline_HTTPToPostgreSQL(t *testing.T) {
	baseDSN := os.Getenv("SPEND_TEST_DATABASE_URL")
	if baseDSN == "" {
		t.Fatal("SPEND_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, baseDSN)
	require.NoError(t, err)
	defer func() { require.NoError(t, admin.Close(context.Background())) }()

	var databaseName string
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName))
	schemaName := "air_proxy_spend_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE") }()
	_, err = admin.Exec(ctx, "SET search_path TO "+quotedSchema)
	require.NoError(t, err)
	installProxyIntegrationSchema(t, ctx, admin)

	sinkDSN := proxyIntegrationSearchPath(t, baseDSN, schemaName)
	sink, err := spendsink.New(ctx, config.SpendLogConfig{
		DatabaseURL:          sinkDSN,
		ExpectedDatabaseName: databaseName,
		MaxConns:             4,
		MinConns:             1,
		HealthCheckInterval:  100 * time.Millisecond,
		ConnectTimeout:       5 * time.Second,
		LogQueueSize:         100,
		LogBatchSize:         20,
		LogFlushInterval:     20 * time.Millisecond,
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier, err := shadowcontext.NewVerifier(config.SignedAuthContextConfig{
		Issuer:          "litellm-it",
		Audience:        "air-it",
		PublicKeys:      map[string]string{"it-key": base64.RawURLEncoding.EncodeToString(publicKey)},
		ClockSkew:       10 * time.Second,
		ReplayCacheSize: 100,
	})
	require.NoError(t, err)
	prices := aimodels.NewModelPriceRegistry()
	prices.Update(map[string]*aimodels.ModelPrice{
		"backend-model": {InputCostPerToken: 0.000001, OutputCostPerToken: 0.000002},
		"image-model":   {OutputCostPerImage: 0.02},
	})
	responseEventIDs := make(map[string]string)

	t.Run("normal json and protected call id", func(t *testing.T) {
		received := make(chan http.Header, 1)
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- r.Header.Clone()
			w.Header().Set(shadowcontext.CallIDHeader, "upstream-must-not-win")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(createMockChatCompletionResponse("chatcmpl-spend-normal", "backend-model", "ok"))
		}))
		defer upstream.Close()

		prx := proxyIntegrationProxy(upstream.URL, sink, verifier, prices)
		request := signedJSONRequest(t, privateKey, "call-normal", "/v1/chat/completions", `{"model":"backend-model","messages":[{"role":"user","content":"redacted-before-db"}]}`)
		response := httptest.NewRecorder()
		prx.ProxyRequest(response, request)

		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, "call-normal", response.Header().Get(shadowcontext.CallIDHeader))
		responseEventID := requireSingleAIREventID(t, response.Header())
		headers := <-received
		assert.Empty(t, headers.Get(shadowcontext.AuthContextHeader))
		assert.Equal(t, "call-normal", headers.Get(shadowcontext.CallIDHeader))
		assert.Equal(t, responseEventID, requireSingleAIREventID(t, headers))
		responseEventIDs["call-normal"] = responseEventID
	})

	t.Run("successful sse", func(t *testing.T) {
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-spend-stream\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-spend-stream\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10,\"total_tokens\":30}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer upstream.Close()

		prx := proxyIntegrationProxy(upstream.URL, sink, verifier, prices)
		request := signedJSONRequest(t, privateKey, "call-stream", "/v1/chat/completions", `{"model":"backend-model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
		response := httptest.NewRecorder()
		prx.ProxyRequest(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Contains(t, response.Body.String(), "chatcmpl-spend-stream")
	})

	t.Run("client abort", func(t *testing.T) {
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-spend-abort\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":15,\"total_tokens\":45}}\n\n")
		}))
		defer upstream.Close()

		prx := proxyIntegrationProxy(upstream.URL, sink, verifier, prices)
		request := signedJSONRequest(t, privateKey, "call-abort", "/v1/chat/completions", `{"model":"backend-model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
		prx.ProxyRequest(newAbortResponseWriter(), request)
	})

	t.Run("provider failure", func(t *testing.T) {
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"synthetic failure"}}`)
		}))
		defer upstream.Close()

		prx := proxyIntegrationProxy(upstream.URL, sink, verifier, prices)
		request := signedJSONRequest(t, privateKey, "call-failure", "/v1/chat/completions", `{"model":"backend-model","messages":[{"role":"user","content":"hello"}]}`)
		response := httptest.NewRecorder()
		prx.ProxyRequest(response, request)
		assert.Equal(t, http.StatusBadRequest, response.Code)
	})

	t.Run("retry and fallback", func(t *testing.T) {
		var primaryCalls, fallbackCalls atomic.Int32
		primary := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			primaryCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate_limit_exceeded","message":"rate limited"}`)
		}))
		defer primary.Close()
		fallback := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fallbackCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(createMockChatCompletionResponse("chatcmpl-spend-fallback", "backend-model", "fallback"))
		}))
		defer fallback.Close()

		prx := NewTestProxyBuilder().WithPrimaryAndFallback(primary.URL, fallback.URL).Build()
		configureProxyIntegration(prx, sink, verifier, prices)
		request := signedJSONRequest(t, privateKey, "call-fallback", "/v1/chat/completions", `{"model":"backend-model","messages":[{"role":"user","content":"hello"}]}`)
		response := httptest.NewRecorder()
		prx.ProxyRequest(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.Equal(t, int32(1), primaryCalls.Load())
		assert.Equal(t, int32(1), fallbackCalls.Load())
	})

	t.Run("multipart image edit", func(t *testing.T) {
		receivedContentType := make(chan string, 1)
		upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedContentType <- r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"created":1800000000,"data":[{"b64_json":"synthetic"},{"b64_json":"synthetic"}]}`)
		}))
		defer upstream.Close()

		prx := proxyIntegrationProxy(upstream.URL, sink, verifier, prices)
		request := signedMultipartRequest(t, privateKey, "call-image")
		response := httptest.NewRecorder()
		prx.ProxyRequest(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
		assert.True(t, strings.HasPrefix(<-receivedContentType, "multipart/form-data; boundary="))
	})

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	require.NoError(t, sink.Shutdown(shutdownCtx))
	shutdownCancel()

	rows := loadProxyPipelineRows(t, ctx, admin)
	require.Len(t, rows, 6)
	assertProxyPipelineRows(t, rows)
	assert.Equal(t, responseEventIDs["call-normal"], rows["call-normal"].AirEventID)
}

func proxyIntegrationProxy(upstreamURL string, sink spendsink.Sink, verifier *shadowcontext.Verifier, prices *aimodels.ModelPriceRegistry) *Proxy {
	prx := NewTestProxyBuilder().
		WithSingleCredential("proxy-it", config.ProviderTypeProxy, upstreamURL, "upstream-key").
		Build()
	configureProxyIntegration(prx, sink, verifier, prices)
	return prx
}

func configureProxyIntegration(prx *Proxy, sink spendsink.Sink, verifier *shadowcontext.Verifier, prices *aimodels.ModelPriceRegistry) {
	prx.spendLogger = sink
	prx.shadowContextVerifier = verifier
	prx.priceRegistry = prices
	prx.spendAPIBase = "http://air-ru01/v1"
}

func signedJSONRequest(t *testing.T, privateKey ed25519.PrivateKey, callID, path, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer master-key")
	request.Header.Set("Content-Type", "application/json")
	setProxyIntegrationContext(t, request, privateKey, callID, "public-model", "deployment-it")
	return request
}

func signedMultipartRequest(t *testing.T, privateKey ed25519.PrivateKey, callID string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "image-model"))
	require.NoError(t, writer.WriteField("prompt", "synthetic edit"))
	require.NoError(t, writer.WriteField("n", "2"))
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="image"; filename="synthetic.png"`},
		"Content-Type":        []string{"image/png"},
	})
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	request.Header.Set("Authorization", "Bearer master-key")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	setProxyIntegrationContext(t, request, privateKey, callID, "public-image", "deployment-image-it")
	return request
}

func setProxyIntegrationContext(t *testing.T, request *http.Request, privateKey ed25519.PrivateKey, callID, publicModel, deploymentID string) {
	t.Helper()
	now := time.Now()
	originalCallType := "acompletion"
	switch request.URL.Path {
	case "/v1/completions":
		originalCallType = "atext_completion"
	case "/v1/embeddings":
		originalCallType = "aembedding"
	case "/v1/responses":
		originalCallType = "aresponses"
	case "/v1/images/generations":
		originalCallType = "aimage_generation"
	case "/v1/images/edits":
		originalCallType = "aimage_edit"
	}
	claims := shadowcontext.Claims{
		Issuer: "litellm-it", Audience: shadowcontext.Audience{"air-it"},
		IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(), ID: uuid.NewString(),
		APIKeyHash: proxyIntegrationKeyHash, UserID: "user-it", TeamID: "team-it", OrganizationID: "org-it",
		ProjectID: "project-it", AgentID: "agent-it", PublicModel: publicModel, DeploymentID: deploymentID,
		EndUser: "end-user-it", Tags: []string{"tag-it"}, OriginalCallType: originalCallType, CallID: callID,
	}
	protected, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "it-key"})
	require.NoError(t, err)
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	encodedProtected := base64.RawURLEncoding.EncodeToString(protected)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedProtected + "." + encodedPayload
	compact := signingInput + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(signingInput)))
	request.Header.Set(shadowcontext.AuthContextHeader, compact)
	request.Header.Set(shadowcontext.CallIDHeader, callID)
}

type abortResponseWriter struct {
	header http.Header
	status int
}

func newAbortResponseWriter() *abortResponseWriter {
	return &abortResponseWriter{header: make(http.Header)}
}
func (w *abortResponseWriter) Header() http.Header { return w.header }
func (w *abortResponseWriter) WriteHeader(status int) {
	w.status = status
}
func (w *abortResponseWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }
func (w *abortResponseWriter) Flush()                    {}

func installProxyIntegrationSchema(t *testing.T, ctx context.Context, connection *pgx.Conn) {
	t.Helper()
	contents, err := os.ReadFile("../spendsink/testdata/litellm_spend_schema.sql")
	require.NoError(t, err)
	for _, rawStatement := range strings.Split(string(contents), ";") {
		lines := strings.Split(rawStatement, "\n")
		filtered := make([]string, 0, len(lines))
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				filtered = append(filtered, line)
			}
		}
		statement := strings.TrimSpace(strings.Join(filtered, "\n"))
		if statement == "" {
			continue
		}
		_, err := connection.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}
}

func proxyIntegrationSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func loadProxyPipelineRows(t *testing.T, ctx context.Context, connection *pgx.Conn) map[string]pipelineRow {
	t.Helper()
	rows, err := connection.Query(ctx, `
		SELECT request_id, COALESCE(metadata#>>'{spend_logs_metadata,air_event_id}',''),
		       call_type, status, model, COALESCE(model_id,''), COALESCE(model_group,''),
		       COALESCE(custom_llm_provider,''), COALESCE(api_base,''), COALESCE(team_id,''),
		       prompt_tokens, completion_tokens, spend,
		       COALESCE(metadata->>'litellm_call_id',''),
		       COALESCE(metadata#>>'{spend_logs_metadata,shadow_context_state}',''),
		       COALESCE(metadata#>>'{spend_logs_metadata,usage_source}',''),
		       COALESCE(metadata#>>'{spend_logs_metadata,cost_status}',''),
		       COALESCE(metadata#>>'{spend_logs_metadata,outcome}',''),
		       COALESCE(metadata#>>'{spend_logs_metadata,actual_credential}',''),
		       jsonb_array_length(COALESCE(metadata#>'{spend_logs_metadata,attempts}', '[]'::jsonb)),
		       COALESCE((metadata#>>'{spend_logs_metadata,comparison_eligible}')::boolean, false),
		       "completionStartTime" IS NOT NULL,
		       COALESCE(messages = '{}'::jsonb, false), COALESCE(response = '{}'::jsonb, false), COALESCE(proxy_server_request = '{}'::jsonb, false)
		FROM "LiteLLM_SpendLogs"
		ORDER BY "startTime", request_id`)
	require.NoError(t, err)
	defer rows.Close()
	result := make(map[string]pipelineRow)
	for rows.Next() {
		var row pipelineRow
		require.NoError(t, rows.Scan(
			&row.RequestID, &row.AirEventID, &row.CallType, &row.Status, &row.Model, &row.ModelID, &row.ModelGroup,
			&row.Provider, &row.APIBase, &row.TeamID, &row.PromptTokens, &row.CompletionTokens, &row.Spend,
			&row.CallID, &row.ContextState, &row.UsageSource, &row.CostStatus,
			&row.Outcome, &row.ActualCredential, &row.Attempts, &row.ComparisonEligible, &row.CompletionStarted,
			&row.MessagesEmptyObject, &row.ResponseEmptyObject, &row.ProxyRequestEmptyObject,
		))
		result[row.CallID] = row
	}
	require.NoError(t, rows.Err())
	return result
}

func assertProxyPipelineRows(t *testing.T, rows map[string]pipelineRow) {
	t.Helper()
	seenRequestIDs := make(map[string]struct{}, len(rows))
	for callID, row := range rows {
		assert.NotEmpty(t, row.RequestID, callID)
		assert.NotEmpty(t, row.AirEventID, callID)
		_, duplicate := seenRequestIDs[row.RequestID]
		assert.False(t, duplicate, callID)
		seenRequestIDs[row.RequestID] = struct{}{}
		assert.Equal(t, callID, row.CallID)
		assert.Equal(t, "valid", row.ContextState, callID)
		assert.Equal(t, "openai", row.Provider, callID)
		assert.Equal(t, "http://air-ru01/v1", row.APIBase, callID)
		assert.Equal(t, "team-it", row.TeamID, callID)
		assert.True(t, row.CompletionStarted, callID)
		assert.True(t, row.MessagesEmptyObject && row.ResponseEmptyObject && row.ProxyRequestEmptyObject, callID)
	}

	assert.Equal(t, "chatcmpl-spend-normal", rows["call-normal"].RequestID)
	assert.Equal(t, "acompletion", rows["call-normal"].CallType)
	assert.Equal(t, 10, rows["call-normal"].PromptTokens)
	assert.Equal(t, 5, rows["call-normal"].CompletionTokens)
	assert.True(t, rows["call-normal"].ComparisonEligible)

	assert.Equal(t, "chatcmpl-spend-stream", rows["call-stream"].RequestID)
	assert.Equal(t, "provider", rows["call-stream"].UsageSource)
	assert.Equal(t, 20, rows["call-stream"].PromptTokens)
	assert.Equal(t, 10, rows["call-stream"].CompletionTokens)

	assert.Equal(t, "failure", rows["call-abort"].Status)
	assert.Equal(t, "chatcmpl-spend-abort", rows["call-abort"].RequestID)
	assert.Equal(t, "client_aborted", rows["call-abort"].Outcome)
	assert.Equal(t, "failure", rows["call-failure"].Status)
	assert.Equal(t, "acompletion", rows["call-failure"].CallType)
	assert.Equal(t, "calculated", rows["call-failure"].CostStatus)
	assert.True(t, rows["call-failure"].ComparisonEligible)

	assert.Equal(t, "chatcmpl-spend-fallback", rows["call-fallback"].RequestID)
	assert.Equal(t, "acompletion", rows["call-fallback"].CallType)
	assert.Equal(t, 2, rows["call-fallback"].Attempts)
	assert.Equal(t, "fallback", rows["call-fallback"].ActualCredential)
	assert.True(t, rows["call-fallback"].ComparisonEligible)

	assert.Equal(t, "aimage_edit", rows["call-image"].CallType)
	assert.Equal(t, "image-model", rows["call-image"].Model)
	assert.Equal(t, "public-image", rows["call-image"].ModelGroup)
	assert.Equal(t, "deployment-image-it", rows["call-image"].ModelID)
	assert.Equal(t, "request_parameters", rows["call-image"].UsageSource)
	assert.InDelta(t, 0.04, rows["call-image"].Spend, 1e-12)
	assert.True(t, rows["call-image"].ComparisonEligible)
}

func TestProxyIntegrationSchemaPath(t *testing.T) {
	if _, err := os.Stat("../spendsink/testdata/litellm_spend_schema.sql"); err != nil {
		t.Fatalf("shared LiteLLM schema fixture is unavailable: %v", err)
	}
}

func Example_spendPipelineIntegration() {
	fmt.Println("SPEND_TEST_DATABASE_URL=postgresql://... go test -race -tags=integration ./internal/proxy")
	// Output: SPEND_TEST_DATABASE_URL=postgresql://... go test -race -tags=integration ./internal/proxy
}
