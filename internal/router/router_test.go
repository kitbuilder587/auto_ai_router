package router

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/auth"
	"github.com/mixaill76/auto_ai_router/internal/balancer"
	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/fail2ban"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
	"github.com/mixaill76/auto_ai_router/internal/proxy"
	"github.com/mixaill76/auto_ai_router/internal/ratelimit"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
)

func newIPv4Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp4 listener unavailable in test environment: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

// createTestProxy creates a test proxy instance
func createTestProxy() *proxy.Proxy {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	f2b := fail2ban.New(3, 0, []int{401, 403, 500})
	rl := ratelimit.New()

	credentials := []config.CredentialConfig{
		{Name: "test1", APIKey: "key1", BaseURL: "http://test1.com", RPM: 100},
		{Name: "test2", APIKey: "key2", BaseURL: "http://test2.com", RPM: 100},
	}

	for _, cred := range credentials {
		rl.AddCredential(cred.Name, cred.RPM)
	}

	bal := balancer.New(credentials, f2b, rl)
	metrics := monitoring.New(false)
	tokenManager := auth.NewVertexTokenManager(logger)

	return proxy.New(&proxy.Config{
		Balancer:            bal,
		Logger:              logger,
		MaxBodySizeMB:       10,
		RequestTimeout:      30 * time.Second,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
		Metrics:             metrics,
		MasterKey:           "test-master-key",
		RateLimiter:         rl,
		TokenManager:        tokenManager,
		ModelManager:        createTestModelManager(),
		Version:             "test-version",
		Commit:              "test-commit",
	})
}

// createTestModelManager creates a test model manager instance (disabled - no static models)
func createTestModelManager() *models.Manager {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	return models.New(logger, 100, []config.ModelRPMConfig{})
}

// createEnabledTestModelManager creates an enabled model manager with static models
func createEnabledTestModelManager() *models.Manager {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	staticModels := []config.ModelRPMConfig{{Name: "test-model", RPM: 100, TPM: 100000}}
	return models.New(logger, 100, staticModels)
}

// createProxyWithConfig creates a test proxy with custom credentials
func createProxyWithConfig(credentials []config.CredentialConfig, bannedCreds []string) *proxy.Proxy {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	f2b := fail2ban.New(1, 0, []int{500})
	rl := ratelimit.New()

	for _, cred := range credentials {
		rl.AddCredential(cred.Name, cred.RPM)
	}

	bal := balancer.New(credentials, f2b, rl)

	// Ban specified credentials
	for _, credName := range bannedCreds {
		f2b.RecordResponse(credName, "", 500)
	}

	metrics := monitoring.New(false)
	tm := auth.NewVertexTokenManager(logger)
	return proxy.New(&proxy.Config{
		Balancer:            bal,
		Logger:              logger,
		MaxBodySizeMB:       10,
		RequestTimeout:      30 * time.Second,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
		Metrics:             metrics,
		MasterKey:           "test-key",
		RateLimiter:         rl,
		TokenManager:        tm,
		ModelManager:        createTestModelManager(),
		Version:             "test-version",
		Commit:              "test-commit",
	})
}

// createProxyWithMockServer creates a proxy configured with a mock server URL
func createProxyWithMockServer(mockServerURL string) *proxy.Proxy {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	f2b := fail2ban.New(3, 0, []int{500})
	rl := ratelimit.New()

	credentials := []config.CredentialConfig{
		{Name: "test1", APIKey: "key1", BaseURL: mockServerURL, RPM: 100},
	}

	for _, cred := range credentials {
		rl.AddCredential(cred.Name, cred.RPM)
	}

	bal := balancer.New(credentials, f2b, rl)
	metrics := monitoring.New(false)
	tm := auth.NewVertexTokenManager(logger)
	return proxy.New(&proxy.Config{
		Balancer:            bal,
		Logger:              logger,
		MaxBodySizeMB:       10,
		RequestTimeout:      30 * time.Second,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     120 * time.Second,
		Metrics:             metrics,
		MasterKey:           "test-key",
		RateLimiter:         rl,
		TokenManager:        tm,
		ModelManager:        createTestModelManager(),
		Version:             "test-version",
		Commit:              "test-commit",
	})
}

func TestNew(t *testing.T) {
	prx := createTestProxy()
	modelManager := createTestModelManager()
	monConfig := testhelpers.NewTestMonitoringConfig("/health", false, "")
	logger := testhelpers.NewTestLogger()

	r := New(nil, modelManager, monConfig, logger, nil)

	assert.NotNil(t, r)
	assert.Equal(t, "/health", r.monitoringConfig.HealthCheckPath)
	assert.Equal(t, modelManager, r.modelManager)

	monConfig2 := testhelpers.NewTestMonitoringConfig("/status", false, "")
	r2 := New(prx, nil, monConfig2, logger, nil)
	assert.NotNil(t, r2)
	assert.Equal(t, "/status", r2.monitoringConfig.HealthCheckPath)
}

func TestServeHTTP_HealthCheck(t *testing.T) {
	prx := createTestProxy()
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "healthy", response["status"])
}

func TestServeHTTP_HealthCheck_Unhealthy(t *testing.T) {
	credentials := []config.CredentialConfig{
		{Name: "test1", APIKey: "key1", BaseURL: "http://test1.com", RPM: 100},
	}
	prx := createProxyWithConfig(credentials, []string{"test1"})
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "unhealthy", response["status"])
}

func TestServeHTTP_HealthCheck_ScopedViewDoesNotDriveStatusCode(t *testing.T) {
	credentials := []config.CredentialConfig{
		{Name: "team-a", APIKey: "key1", BaseURL: "http://team-a.example", RPM: 100, Scopes: []string{"team-a"}},
	}
	prx := createProxyWithConfig(credentials, nil)
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "unhealthy", response["status"])
	assert.Equal(t, float64(0), response["total_credentials"])
}

func TestServeHTTP_V1Models_Enabled(t *testing.T) {
	modelManager := createEnabledTestModelManager()

	prx := createTestProxy()
	router := New(prx, modelManager, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response models.ModelsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "list", response.Object)
	// Empty models is OK for this test, just verifying the endpoint works
}

func TestServeHTTP_V1Models_Disabled(t *testing.T) {
	mockServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": "proxied"})
	}))
	defer mockServer.Close()

	prx := createProxyWithMockServer(mockServer.URL)
	modelManager := createTestModelManager() // disabled (no static models)
	router := New(prx, modelManager, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should proxy the request instead of handling locally
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServeHTTP_V1Models_NilManager(t *testing.T) {
	mockServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": "proxied"})
	}))
	defer mockServer.Close()

	prx := createProxyWithMockServer(mockServer.URL)
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Should proxy the request when model manager is nil
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServeHTTP_ProxyRequest(t *testing.T) {
	mockServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
	}))
	defer mockServer.Close()

	prx := createProxyWithMockServer(mockServer.URL)
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	tests := []struct {
		name string
		path string
	}{
		{"chat completions", "/v1/chat/completions"},
		{"completions", "/v1/completions"},
		{"embeddings", "/v1/embeddings"},
		{"images", "/v1/images/generations"},
		{"image edits", "/v1/images/edits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model": "test-model"}`)
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestServeHTTP_NotFound(t *testing.T) {
	prx := createTestProxy()
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	tests := []struct {
		name string
		path string
	}{
		{"root path", "/"},
		{"api path", "/api/test"},
		{"random path", "/random"},
		{"v2 path", "/v2/chat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

func TestHandleHealth(t *testing.T) {
	tests := []struct {
		name           string
		bannedCreds    []string
		expectedStatus int
	}{
		{
			name:           "healthy - all available",
			bannedCreds:    []string{},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "unhealthy - all banned",
			bannedCreds:    []string{"test1", "test2"},
			expectedStatus: http.StatusServiceUnavailable,
		},
		{
			name:           "healthy - partially available",
			bannedCreds:    []string{"test1"},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := []config.CredentialConfig{
				{Name: "test1", APIKey: "key1", BaseURL: "http://test1.com", RPM: 100},
				{Name: "test2", APIKey: "key2", BaseURL: "http://test2.com", RPM: 100},
			}
			prx := createProxyWithConfig(credentials, tt.bannedCreds)
			router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

			req := httptest.NewRequest("GET", "/health", nil)
			w := httptest.NewRecorder()

			router.handleHealth(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)

			if tt.expectedStatus == http.StatusOK {
				assert.Equal(t, "healthy", response["status"])
			} else {
				assert.Equal(t, "unhealthy", response["status"])
			}
		})
	}
}

func TestHandleModels(t *testing.T) {
	modelManager := createEnabledTestModelManager()
	prx := createTestProxy()

	router := New(prx, modelManager, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	router.handleModels(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response models.ModelsResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "list", response.Object)
	// Models list might be empty if not fetched, which is OK
}

func TestHandleVisualHealth(t *testing.T) {
	prx := createTestProxy()
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/vhealth", nil)
	w := httptest.NewRecorder()

	router.handleVisualHealth(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	assert.NotEmpty(t, w.Body.String())
	// Should return HTML content
	assert.Contains(t, w.Body.String(), "html")
}

func TestServeHTTP_VisualHealth(t *testing.T) {
	prx := createTestProxy()
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", false, ""), testhelpers.NewTestLogger(), nil)

	req := httptest.NewRequest("GET", "/vhealth", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestServeHTTP_StreamingRequestNotLogged(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock proxy that returns a 500 error
	mockServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer mockServer.Close()

	prx := createProxyWithMockServer(mockServer.URL)
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", true, tmpDir+"/errors.log"), testhelpers.NewTestLogger(), nil)

	// Test: Streaming request should NOT be logged even if status is 500
	streamingBody := []byte(`{"stream": true, "model": "test-model"}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(streamingBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Streaming request should still be processed (500 from mock)
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	// But log file should be empty (streaming requests are not logged)
	logPath := tmpDir + "/errors.log"
	content, err := os.ReadFile(logPath)
	if err == nil {
		// File exists but should be empty
		assert.Empty(t, content, "Streaming requests should not be logged")
	}
	// If file doesn't exist, that's also expected (no logging)
}

func TestServeHTTP_NonStreamingErrorIsLogged(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := tmpDir + "/errors.log"

	// Create a mock proxy that returns a 400 error
	mockServer := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer mockServer.Close()

	prx := createProxyWithMockServer(mockServer.URL)
	router := New(prx, nil, testhelpers.NewTestMonitoringConfig("/health", true, logPath), testhelpers.NewTestLogger(), nil)

	// Test: Non-streaming request SHOULD be logged when status is error
	nonStreamingBody := []byte(`{"stream": false, "model": "test-model"}`)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(nonStreamingBody)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Non-streaming request should be processed (400 from mock)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Log file should contain the error
	content, err := os.ReadFile(logPath)
	assert.NoError(t, err, "Log file should exist")
	assert.NotEmpty(t, content, "Non-streaming error should be logged")

	// Verify log format
	var entry ErrorLogEntry
	err = json.Unmarshal(content, &entry)
	assert.NoError(t, err, "Log file should contain valid JSON")
	assert.Equal(t, http.StatusBadRequest, entry.Status)
}
