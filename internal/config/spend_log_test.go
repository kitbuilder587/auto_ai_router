package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSpendLogConfig_Shadow(t *testing.T) {
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
  mode: shadow
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

	assert.Equal(t, SpendLogModeShadow, cfg.SpendLog.Mode)
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
	assert.Equal(t, SpendLogModeDisabled, cfg.SpendLog.Mode)
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
			spendLog:    SpendLogConfig{Mode: SpendLogModeDisabled},
			wantNoError: true,
		},
		{
			name:     "unknown mode",
			spendLog: SpendLogConfig{Mode: "primary"},
			wantErr:  "spend_log.mode",
		},
		{
			name:     "shadow requires database url",
			spendLog: SpendLogConfig{Mode: SpendLogModeShadow, ExpectedDatabaseName: "test-db"},
			wantErr:  "spend_log.database_url",
		},
		{
			name: "shadow requires expected database name",
			spendLog: SpendLogConfig{
				Mode:        SpendLogModeShadow,
				DatabaseURL: "postgres://localhost/test-db",
			},
			wantErr: "spend_log.expected_database_name",
		},
		{
			name: "shadow rejects non postgres url",
			spendLog: SpendLogConfig{
				Mode:                 SpendLogModeShadow,
				DatabaseURL:          "mysql://localhost/test-db",
				ExpectedDatabaseName: "test-db",
			},
			wantErr: "postgres://",
		},
		{
			name:        "shadow valid",
			spendLog:    validShadowSpendLogConfig(),
			wantNoError: true,
		},
		{
			name: "shadow requires canonical api base",
			spendLog: func() SpendLogConfig {
				cfg := validShadowSpendLogConfig()
				cfg.APIBase = "http://another-air/v1"
				return cfg
			}(),
			wantErr: "spend_log.api_base",
		},
		{
			name: "shadow rejects unsafe queue values",
			spendLog: func() SpendLogConfig {
				cfg := validShadowSpendLogConfig()
				cfg.LogBatchSize = -1
				return cfg
			}(),
			wantErr: "queue size",
		},
		{
			name: "shadow rejects invalid pool limits",
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
