package telemetry

import (
	"context"
	"testing"
	"time"

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
	assert.False(t, tel.MetricsEnabled())
	assert.NoError(t, tel.Shutdown(context.Background()))
}

func TestSetupEnabled(t *testing.T) {
	// OTLP exporters are lazy: Setup must succeed even when no collector is
	// listening on the endpoint (export errors happen later, asynchronously).
	cfg := &config.OTELConfig{
		Enabled:              true,
		Endpoint:             "localhost:14317",
		Protocol:             "grpc",
		Insecure:             true,
		ServiceName:          "test-router",
		LogsEnabled:          true,
		TracesEnabled:        true,
		MetricExportInterval: 60 * time.Second,
		TraceSampleRatio:     1.0,
	}

	tel, err := Setup(context.Background(), cfg, "test", "abc", nil)
	require.NoError(t, err)
	require.NotNil(t, tel)
	assert.True(t, tel.TracesEnabled())
	assert.True(t, tel.MetricsEnabled())
	assert.NotNil(t, tel.LogHandler())
	// The metric reader flushes the (always-populated) Prometheus registry on
	// shutdown, so with no collector listening the export fails — bound it and
	// don't require success.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = tel.Shutdown(shutdownCtx)
}

func TestSetupHTTPProtocol(t *testing.T) {
	cfg := &config.OTELConfig{
		Enabled:              true,
		Endpoint:             "http://localhost:14318",
		Protocol:             "http",
		Insecure:             true,
		ServiceName:          "test-router",
		LogsEnabled:          false,
		TracesEnabled:        true,
		MetricExportInterval: 30 * time.Second,
		TraceSampleRatio:     0.5,
	}

	tel, err := Setup(context.Background(), cfg, "test", "abc", nil)
	require.NoError(t, err)
	require.NotNil(t, tel)
	assert.True(t, tel.TracesEnabled())
	assert.True(t, tel.MetricsEnabled())
	assert.Nil(t, tel.LogHandler(), "log handler must be nil when logs_enabled=false")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = tel.Shutdown(shutdownCtx)
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
