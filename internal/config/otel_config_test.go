package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otelTestConfigBase = `
server:
  port: 8080
  max_body_size_mb: 100
  request_timeout: 30s
  master_key: "sk-test-master-key"

credentials:
  - name: "provider_1"
    type: "openai"
    api_key: "sk-xxxx"
    base_url: "https://api.openai.com"
    rpm: 60
`

func loadConfigFromString(t *testing.T, content string) (*Config, error) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0644))
	return Load(configPath)
}

func TestOTELConfig_DefaultsWhenSectionAbsent(t *testing.T) {
	cfg, err := loadConfigFromString(t, otelTestConfigBase)
	require.NoError(t, err)

	assert.False(t, cfg.OTEL.Enabled)
	assert.Equal(t, "grpc", cfg.OTEL.Protocol)
	assert.Equal(t, "localhost:4317", cfg.OTEL.Endpoint)
	assert.Equal(t, "auto-ai-router", cfg.OTEL.ServiceName)
	assert.True(t, cfg.OTEL.Insecure)
	assert.True(t, cfg.OTEL.LogsEnabled)
	assert.True(t, cfg.OTEL.TracesEnabled)
	assert.Equal(t, 60*time.Second, cfg.OTEL.MetricExportInterval)
	assert.Equal(t, 1.0, cfg.OTEL.TraceSampleRatio)
	assert.True(t, cfg.OTEL.TrustIncomingTraceparent)
}

func TestOTELConfig_FullSection(t *testing.T) {
	cfg, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: true
  endpoint: "collector.example.com:4317"
  protocol: grpc
  insecure: false
  service_name: "my-router"
  logs_enabled: true
  traces_enabled: false
  metric_export_interval: 15s
  trace_sample_ratio: 0.25
  trust_incoming_traceparent: false
  headers:
    Authorization: "Bearer token123"
`)
	require.NoError(t, err)

	assert.True(t, cfg.OTEL.Enabled)
	assert.Equal(t, "collector.example.com:4317", cfg.OTEL.Endpoint)
	assert.Equal(t, "grpc", cfg.OTEL.Protocol)
	assert.False(t, cfg.OTEL.Insecure)
	assert.Equal(t, "my-router", cfg.OTEL.ServiceName)
	assert.True(t, cfg.OTEL.LogsEnabled)
	assert.False(t, cfg.OTEL.TracesEnabled)
	assert.Equal(t, 15*time.Second, cfg.OTEL.MetricExportInterval)
	assert.Equal(t, 0.25, cfg.OTEL.TraceSampleRatio)
	assert.False(t, cfg.OTEL.TrustIncomingTraceparent)
	assert.Equal(t, "Bearer token123", cfg.OTEL.Headers["Authorization"])
}

func TestOTELConfig_HTTPProtocolDefaultEndpoint(t *testing.T) {
	cfg, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: true
  protocol: http
`)
	require.NoError(t, err)

	assert.Equal(t, "http", cfg.OTEL.Protocol)
	assert.Equal(t, "localhost:4318", cfg.OTEL.Endpoint)
}

func TestOTELConfig_EnvResolution(t *testing.T) {
	t.Setenv("TEST_OTEL_ENDPOINT", "otel-collector:4317")
	t.Setenv("TEST_OTEL_TOKEN", "secret-token")

	cfg, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: true
  endpoint: "os.environ/TEST_OTEL_ENDPOINT"
  headers:
    X-Auth: "os.environ/TEST_OTEL_TOKEN"
`)
	require.NoError(t, err)

	assert.Equal(t, "otel-collector:4317", cfg.OTEL.Endpoint)
	assert.Equal(t, "secret-token", cfg.OTEL.Headers["X-Auth"])
}

func TestOTELConfig_InvalidProtocol(t *testing.T) {
	_, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: true
  protocol: udp
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "otel.protocol")
}

func TestOTELConfig_InvalidSampleRatio(t *testing.T) {
	_, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: true
  trace_sample_ratio: 1.5
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trace_sample_ratio")
}

func TestServerConfig_StdoutLogsEnabledDefault(t *testing.T) {
	cfg, err := loadConfigFromString(t, otelTestConfigBase)
	require.NoError(t, err)
	assert.True(t, cfg.Server.StdoutLogsEnabled)
}

func TestServerConfig_StdoutLogsDisabled(t *testing.T) {
	cfg, err := loadConfigFromString(t, `
server:
  port: 8080
  max_body_size_mb: 100
  request_timeout: 30s
  master_key: "sk-test-master-key"
  stdout_logs_enabled: false

credentials:
  - name: "provider_1"
    type: "openai"
    api_key: "sk-xxxx"
    base_url: "https://api.openai.com"
    rpm: 60
`)
	require.NoError(t, err)
	assert.False(t, cfg.Server.StdoutLogsEnabled)
}

func TestOTELConfig_DisabledSkipsValidation(t *testing.T) {
	// Invalid protocol must not fail validation when OTEL is disabled.
	cfg, err := loadConfigFromString(t, otelTestConfigBase+`
otel:
  enabled: false
  protocol: udp
`)
	require.NoError(t, err)
	assert.False(t, cfg.OTEL.Enabled)
}
