package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/modelupdate"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveServerWriteTimeoutPreservesResponseGrace(t *testing.T) {
	tests := []struct {
		name           string
		requestTimeout time.Duration
		writeTimeout   time.Duration
		want           time.Duration
	}{
		{
			name:           "equal timeout gets response grace",
			requestTimeout: 30 * time.Second,
			writeTimeout:   30 * time.Second,
			want:           35 * time.Second,
		},
		{
			name:           "shorter write timeout gets response grace",
			requestTimeout: 30 * time.Second,
			writeTimeout:   20 * time.Second,
			want:           35 * time.Second,
		},
		{
			name:           "longer write timeout remains configured",
			requestTimeout: 30 * time.Second,
			writeTimeout:   60 * time.Second,
			want:           60 * time.Second,
		},
		{
			name:           "disabled write timeout remains disabled",
			requestTimeout: 30 * time.Second,
			writeTimeout:   0,
			want:           0,
		},
		{
			name:           "unlimited request timeout does not invent deadline",
			requestTimeout: -1,
			writeTimeout:   60 * time.Second,
			want:           60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, effectiveServerWriteTimeout(tt.requestTimeout, tt.writeTimeout))
		})
	}
}

func TestSplitCredentialModel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "standard format",
			input:    "openai_main:gpt-4o",
			expected: []string{"openai_main", "gpt-4o"},
		},
		{
			name:     "with multiple colons in model name",
			input:    "openai_main:gpt-4o:turbo",
			expected: []string{"openai_main", "gpt-4o:turbo"},
		},
		{
			name:     "simple names",
			input:    "cred1:model1",
			expected: []string{"cred1", "model1"},
		},
		{
			name:     "with dashes and underscores",
			input:    "openai_backup:gpt-3.5-turbo",
			expected: []string{"openai_backup", "gpt-3.5-turbo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := modelupdate.SplitCredentialModel(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInitializeShadowSpendSinkDisabledDoesNotCreateWriter(t *testing.T) {
	cfg := &config.Config{SpendLog: config.SpendLogConfig{}}
	sink := initializeShadowSpendSink(cfg, slog.New(slog.DiscardHandler), monitoring.New(false))

	assert.False(t, sink.IsEnabled())
	assert.NoError(t, sink.LogSpend(nil))
}

func TestInitializeModelManagerKeepsBackendRateLimitsBehindClientSurface(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{DefaultModelsRPM: -1},
		Fail2Ban: config.Fail2BanConfig{
			MaxAttempts: 3,
			BanDuration: time.Minute,
			ErrorCodes:  []int{429},
		},
		Credentials: []config.CredentialConfig{{
			Name: "provider", Type: config.ProviderTypeOpenAI, RPM: -1, TPM: -1,
		}},
		Models: []config.ModelRPMConfig{{
			Name: "backend-chat", Credential: "provider", RPM: 7, TPM: 70,
		}},
		ModelAlias:     map[string]string{"public/chat": "backend-chat"},
		ClientModelIDs: []string{"public/chat"},
	}
	logger := slog.New(slog.DiscardHandler)
	_, limiter, bal := initializeBalancer(cfg, logger, nil)
	manager := initializeModelManager(logger, cfg, limiter, bal)

	assert.Equal(t, 7, limiter.GetModelLimitRPM("provider", "backend-chat"))
	assert.Equal(t, 70, limiter.GetModelLimitTPM("provider", "backend-chat"))
	assert.Equal(t, -1, limiter.GetModelLimitRPM("provider", "public/chat"))
	models := manager.GetAllModels()
	if assert.Len(t, models.Data, 1) {
		assert.Equal(t, "public/chat", models.Data[0].ID)
	}
}

func TestInitializeShadowSpendSinkConnectionFailureIsFailOpen(t *testing.T) {
	cfg := &config.Config{SpendLog: config.SpendLogConfig{
		DatabaseURL:          "postgres://%zz",
		ExpectedDatabaseName: "test-db",
	}}
	sink := initializeShadowSpendSink(cfg, slog.New(slog.DiscardHandler), monitoring.New(false))

	assert.False(t, sink.IsEnabled())
	assert.NoError(t, sink.LogSpend(nil))
}

// ru01 runs litellm_db with is_required=true. When the database is unreachable
// the process must NOT start on a NoopManager (which would fail-open budgets and
// silently drop billing) — resolve must return an error so startup aborts.
func TestResolveLiteLLMDBManagerRequiredWithoutDBFailsClosed(t *testing.T) {
	cfg := &config.Config{LiteLLMDB: config.LiteLLMDBConfig{
		Enabled:        true,
		IsRequired:     true,
		DatabaseURL:    "postgres://%zz",
		ConnectTimeout: 100 * time.Millisecond,
	}}

	manager, err := resolveLiteLLMDBManager(cfg, slog.New(slog.DiscardHandler))

	require.Error(t, err)
	assert.Nil(t, manager)
}

// With is_required=false the degrade path is allowed, but it must be observable:
// the process comes up on a NoopManager and LiteLLMDBDegraded is raised to 1 so
// the silent billing/budget loss is not invisible.
func TestResolveLiteLLMDBManagerOptionalDegradesLoudly(t *testing.T) {
	monitoring.LiteLLMDBDegraded.Set(0)
	cfg := &config.Config{LiteLLMDB: config.LiteLLMDBConfig{
		Enabled:        true,
		IsRequired:     false,
		DatabaseURL:    "postgres://%zz",
		ConnectTimeout: 100 * time.Millisecond,
	}}

	manager, err := resolveLiteLLMDBManager(cfg, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.NotNil(t, manager)
	assert.False(t, manager.IsEnabled(), "degraded manager must be the NoopManager")
	assert.Equal(t, 1.0, testutil.ToFloat64(monitoring.LiteLLMDBDegraded))
}

func TestResolveLiteLLMDBManagerDisabledUsesNoop(t *testing.T) {
	cfg := &config.Config{LiteLLMDB: config.LiteLLMDBConfig{Enabled: false}}

	manager, err := resolveLiteLLMDBManager(cfg, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.NotNil(t, manager)
	assert.False(t, manager.IsEnabled())
}
