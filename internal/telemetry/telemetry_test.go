package telemetry

import (
	"context"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupDisabled(t *testing.T) {
	tel, err := Setup(context.Background(), &config.OTELConfig{Enabled: false}, "test", "abc", nil)
	require.NoError(t, err)
	assert.Nil(t, tel)
}

func TestSetupNilConfig(t *testing.T) {
	tel, err := Setup(context.Background(), nil, "test", "abc", nil)
	require.NoError(t, err)
	assert.Nil(t, tel)
}

func TestNilTelemetryIsSafe(t *testing.T) {
	var tel *Telemetry
	assert.Nil(t, tel.LogHandler())
	assert.False(t, tel.TracesEnabled())
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestSetupEnabled(t *testing.T) {
	// OTLP exporters are lazy: Setup must succeed even when no collector is
	// listening on the endpoint (export errors happen later, asynchronously).
	cfg := &config.OTELConfig{
		Enabled:          true,
		Endpoint:         "localhost:14317",
		Protocol:         "grpc",
		Insecure:         true,
		ServiceName:      "test-router",
		LogsEnabled:      true,
		TracesEnabled:    true,
		TraceSampleRatio: 1.0,
	}

	tel, err := Setup(context.Background(), cfg, "test", "abc", nil)
	require.NoError(t, err)
	require.NotNil(t, tel)
	assert.True(t, tel.TracesEnabled())
	assert.NotNil(t, tel.LogHandler())
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestSetupHTTPProtocol(t *testing.T) {
	cfg := &config.OTELConfig{
		Enabled:          true,
		Endpoint:         "http://localhost:14318",
		Protocol:         "http",
		Insecure:         true,
		ServiceName:      "test-router",
		LogsEnabled:      false,
		TracesEnabled:    true,
		TraceSampleRatio: 0.5,
	}

	tel, err := Setup(context.Background(), cfg, "test", "abc", nil)
	require.NoError(t, err)
	require.NotNil(t, tel)
	assert.True(t, tel.TracesEnabled())
	assert.Nil(t, tel.LogHandler(), "log handler must be nil when logs_enabled=false")
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestHasURLScheme(t *testing.T) {
	assert.True(t, hasURLScheme("http://localhost:4318"))
	assert.True(t, hasURLScheme("https://collector.example.com"))
	assert.False(t, hasURLScheme("localhost:4317"))
	assert.False(t, hasURLScheme("collector:4317"))
}

func TestWithSignalPath(t *testing.T) {
	// No path: the standard signal path must be appended.
	assert.Equal(t, "http://collector:4318/v1/logs", withSignalPath("http://collector:4318", "/v1/logs"))
	assert.Equal(t, "http://collector:4318/v1/traces", withSignalPath("http://collector:4318/", "/v1/traces"))
	// Explicit path: kept as-is.
	assert.Equal(t, "http://collector:4318/custom/logs", withSignalPath("http://collector:4318/custom/logs", "/v1/logs"))
}
