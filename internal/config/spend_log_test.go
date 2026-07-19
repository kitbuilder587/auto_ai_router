package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSpendLogConfig(t *testing.T) {
	t.Setenv("SHADOW_DATABASE_URL", "postgresql://shadow:secret@db.example/test-db")
	t.Setenv("SHADOW_PUBLIC_KEY", "cHVibGljLWtleQ==")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  port: 8080
  master_key: test-master-key
credentials:
  - name: upstream
    type: openai
    api_key: test-key
    base_url: https://api.openai.com
    rpm: 10
monitoring:
  prometheus_enabled: false
spend_log:
  database_url: os.environ/SHADOW_DATABASE_URL
  expected_database_name: test-db
  api_base: http://air-ru01/v1
  max_conns: 7
  min_conns: 1
  connect_timeout: 3s
  log_queue_size: 123
  log_batch_size: 17
  log_flush_interval: 2s
  auth_context:
    issuer: litellm
    audience: air-ru01
    public_keys:
      test-key: os.environ/SHADOW_PUBLIC_KEY
    clock_skew: 15s
    replay_cache_size: 321
`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.True(t, cfg.SpendLog.IsEnabled())
	assert.Equal(t, "postgresql://shadow:secret@db.example/test-db", cfg.SpendLog.DatabaseURL)
	assert.Equal(t, "test-db", cfg.SpendLog.ExpectedDatabaseName)
	assert.Equal(t, "http://air-ru01/v1", cfg.SpendLog.APIBase)
	assert.Equal(t, 7, cfg.SpendLog.MaxConns)
	assert.Equal(t, 1, cfg.SpendLog.MinConns)
	assert.Equal(t, 3*time.Second, cfg.SpendLog.ConnectTimeout)
	assert.Equal(t, 123, cfg.SpendLog.LogQueueSize)
	assert.Equal(t, 17, cfg.SpendLog.LogBatchSize)
	assert.Equal(t, 2*time.Second, cfg.SpendLog.LogFlushInterval)
	assert.Equal(t, "litellm", cfg.SpendLog.AuthContext.Issuer)
	assert.Equal(t, "air-ru01", cfg.SpendLog.AuthContext.Audience)
	assert.Equal(t, "cHVibGljLWtleQ==", cfg.SpendLog.AuthContext.PublicKeys["test-key"])
	assert.Equal(t, 15*time.Second, cfg.SpendLog.AuthContext.ClockSkew)
	assert.Equal(t, 321, cfg.SpendLog.AuthContext.ReplayCacheSize)
}

func TestSpendLogConfigDefaultsToDisabled(t *testing.T) {
	cfg := minimalValidConfig()
	require.NoError(t, cfg.Validate())
	assert.False(t, cfg.SpendLog.IsEnabled())
}

func TestLoadSpendLogDirectModeRequiresFailClosedControlPlane(t *testing.T) {
	t.Setenv("DIRECT_DATABASE_URL", "postgresql://direct:secret@db.example/direct-db")
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  port: 8080
  master_key: test-master-key
credentials:
  - name: upstream
    type: openai
    api_key: test-key
    base_url: https://api.openai.com
    rpm: 10
monitoring:
  prometheus_enabled: false
spend_log:
  mode: direct
  database_url: os.environ/DIRECT_DATABASE_URL
  expected_database_name: direct-db
  api_base: http://air-ru01/v1
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "direct mode requires litellm_db.enabled=true")
}

func TestValidateSpendLogConfig(t *testing.T) {
	tests := []struct {
		name        string
		spendLog    SpendLogConfig
		wantErr     string
		wantNoError bool
	}{
		{
			name:        "disabled needs no database",
			spendLog:    SpendLogConfig{},
			wantNoError: true,
		},
		{
			name:     "configured destination requires database url",
			spendLog: SpendLogConfig{ExpectedDatabaseName: "test-db"},
			wantErr:  "spend_log.database_url",
		},
		{
			name: "configured writer requires expected database name",
			spendLog: SpendLogConfig{
				DatabaseURL: "postgres://localhost/test-db",
			},
			wantErr: "spend_log.expected_database_name",
		},
		{
			name: "configured writer rejects non postgres url",
			spendLog: SpendLogConfig{
				DatabaseURL:          "mysql://localhost/test-db",
				ExpectedDatabaseName: "test-db",
			},
			wantErr: "postgres://",
		},
		{
			name:        "configured writer is valid",
			spendLog:    validShadowSpendLogConfig(),
			wantNoError: true,
		},
		{
			name: "configured writer requires canonical api base",
			spendLog: func() SpendLogConfig {
				cfg := validShadowSpendLogConfig()
				cfg.APIBase = "http://another-air/v1"
				return cfg
			}(),
			wantErr: "spend_log.api_base",
		},
		{
			name: "configured writer rejects unsafe queue values",
			spendLog: func() SpendLogConfig {
				cfg := validShadowSpendLogConfig()
				cfg.LogBatchSize = -1
				return cfg
			}(),
			wantErr: "queue size",
		},
		{
			name: "configured writer rejects invalid pool limits",
			spendLog: func() SpendLogConfig {
				cfg := validShadowSpendLogConfig()
				cfg.MinConns = cfg.MaxConns + 1
				return cfg
			}(),
			wantErr: "connection limits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := minimalValidConfig()
			cfg.SpendLog = tt.spendLog
			err := cfg.Validate()
			if tt.wantNoError {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateDirectSpendLogDependencies(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid"},
		{name: "required control plane", mutate: func(cfg *Config) { cfg.LiteLLMDB.IsRequired = false }, wantErr: "litellm_db.is_required"},
		{name: "model table", mutate: func(cfg *Config) { cfg.LiteLLMDB.LoadLitellmDBModels = false }, wantErr: "litellm_db.load_db_models"},
		{name: "budget reservation", mutate: func(cfg *Config) { cfg.LiteLLMDB.EnforceBudgetReservation = false }, wantErr: "enforce_budget_reservation"},
		{name: "key rate limits", mutate: func(cfg *Config) { cfg.LiteLLMDB.EnforceKeyRateLimits = false }, wantErr: "enforce_key_rate_limits"},
		{name: "redis", mutate: func(cfg *Config) { cfg.Redis.Enabled = false }, wantErr: "redis.enabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validDirectConfig()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func validShadowSpendLogConfig() SpendLogConfig {
	return defaultSpendLogConfigWithDestination("postgres://localhost/test-db", "test-db")
}

func defaultSpendLogConfigWithDestination(databaseURL, expectedDatabaseName string) SpendLogConfig {
	cfg := defaultSpendLogConfig()
	cfg.Mode = SpendLogModeShadow
	cfg.DatabaseURL = databaseURL
	cfg.ExpectedDatabaseName = expectedDatabaseName
	return cfg
}

func minimalValidConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:           8080,
			MaxBodySizeMB:  10,
			MasterKey:      "test-master-key",
			RequestTimeout: 30 * time.Second,
		},
		Credentials: []CredentialConfig{{
			Name:    "upstream",
			Type:    ProviderTypeOpenAI,
			APIKey:  "test-key",
			BaseURL: "https://api.openai.com",
			RPM:     10,
		}},
		Fail2Ban: Fail2BanConfig{MaxAttempts: 3},
	}
}

func validDirectConfig() *Config {
	cfg := minimalValidConfig()
	cfg.SpendLog = defaultSpendLogConfigWithDestination(
		"postgres://localhost/direct-db", "direct-db",
	)
	cfg.SpendLog.Mode = SpendLogModeDirect
	cfg.LiteLLMDB = defaultLiteLLMDBConfig()
	cfg.LiteLLMDB.Enabled = true
	cfg.LiteLLMDB.IsRequired = true
	cfg.LiteLLMDB.LoadLitellmDBModels = true
	cfg.LiteLLMDB.DatabaseURL = "postgres://localhost/control-db"
	cfg.LiteLLMDB.EnforceBudgetReservation = true
	cfg.LiteLLMDB.EnforceKeyRateLimits = true
	cfg.Redis = defaultRedisConfig()
	cfg.Redis.Enabled = true
	cfg.Redis.InitAddresses = []string{"localhost:6379"}
	return cfg
}
