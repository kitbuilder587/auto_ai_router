// Package spendsink owns the isolated, fail-open LiteLLM-compatible spend
// writer. It intentionally has no auth or model-table responsibilities.
package spendsink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/connection"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb/spendlog"
	"github.com/mixaill76/auto_ai_router/internal/monitoring"
)

var ErrUnexpectedDatabase = errors.New("spend sink: unexpected database")

// CommitResult is the acknowledged database outcome for a synchronous spend
// write. It aliases the writer-owned type so data-plane callers do not need to
// duplicate commit semantics.
type CommitResult = spendlog.CommitResult

// Sink is the data-plane-facing spend writer contract.
type Sink interface {
	LogSpend(entry *models.SpendLogEntry) error
	CommitSpend(ctx context.Context, entry *models.SpendLogEntry) (CommitResult, error)
	ReadKeySpend(ctx context.Context, apiKeyHash string) (value float64, known bool, err error)
	IsEnabled() bool
	IsHealthy() bool
	Stats() models.SpendLoggerStats
	Shutdown(ctx context.Context) error
}

// DisabledSink keeps request handling fail-open when spend logging is disabled
// explicitly or rejected during startup preflight.
type DisabledSink struct {
	reason string
}

func NewDisabledSink(reason string) *DisabledSink {
	return &DisabledSink{reason: reason}
}

func (s *DisabledSink) LogSpend(*models.SpendLogEntry) error { return nil }
func (s *DisabledSink) CommitSpend(context.Context, *models.SpendLogEntry) (CommitResult, error) {
	return CommitResult{}, nil
}
func (s *DisabledSink) ReadKeySpend(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}
func (s *DisabledSink) IsEnabled() bool                { return false }
func (s *DisabledSink) IsHealthy() bool                { return false }
func (s *DisabledSink) Stats() models.SpendLoggerStats { return models.SpendLoggerStats{} }
func (s *DisabledSink) Shutdown(context.Context) error { return nil }
func (s *DisabledSink) DisabledReason() string         { return s.reason }

// PostgresSink owns a connection pool used only by the spend logger.
type PostgresSink struct {
	pool          *connection.ConnectionPool
	logger        *spendlog.Logger
	poolCloseOnce sync.Once
}

// New connects to the configured database, verifies current_database() exactly,
// and starts the asynchronous writer only after the guard succeeds.
func New(ctx context.Context, cfg config.SpendLogConfig, log *slog.Logger) (Sink, error) {
	if !cfg.IsEnabled() {
		return NewDisabledSink("disabled by configuration"), nil
	}

	dbCfg := &models.Config{
		DatabaseURL:         cfg.DatabaseURL,
		MaxConns:            int32(cfg.MaxConns),
		MinConns:            int32(cfg.MinConns),
		HealthCheckInterval: cfg.HealthCheckInterval,
		ConnectTimeout:      cfg.ConnectTimeout,
		LogQueueSize:        cfg.LogQueueSize,
		LogBatchSize:        cfg.LogBatchSize,
		LogFlushInterval:    cfg.LogFlushInterval,
		Logger:              log,
	}
	pool, err := connection.NewConnectionPool(dbCfg)
	if err != nil {
		return nil, fmt.Errorf("spend sink: connect: %w", err)
	}

	var actualDatabase string
	if err := pool.Pool().QueryRow(ctx, "SELECT current_database()").Scan(&actualDatabase); err != nil {
		pool.Close()
		return nil, fmt.Errorf("spend sink: database guard query: %w", err)
	}
	if err := validateDatabaseName(actualDatabase, cfg.ExpectedDatabaseName); err != nil {
		pool.Close()
		return nil, err
	}
	pool.SetHealthObserver(monitoring.SetSpendSinkHealthy)

	writer := spendlog.NewLogger(pool, dbCfg)
	writer.Start()
	return &PostgresSink{pool: pool, logger: writer}, nil
}

func validateDatabaseName(actual, expected string) error {
	if actual != expected || actual == "" {
		return fmt.Errorf("%w: expected %q, connected to %q", ErrUnexpectedDatabase, expected, actual)
	}
	return nil
}

func (s *PostgresSink) LogSpend(entry *models.SpendLogEntry) error {
	return s.logger.TryLog(entry)
}

func (s *PostgresSink) CommitSpend(ctx context.Context, entry *models.SpendLogEntry) (CommitResult, error) {
	return s.logger.CommitSpend(ctx, entry)
}

func (s *PostgresSink) ReadKeySpend(ctx context.Context, apiKeyHash string) (float64, bool, error) {
	return s.logger.ReadKeySpend(ctx, apiKeyHash)
}

func (s *PostgresSink) IsEnabled() bool { return true }

func (s *PostgresSink) IsHealthy() bool {
	return s.pool != nil && s.pool.IsHealthy()
}

func (s *PostgresSink) Stats() models.SpendLoggerStats {
	return s.logger.Stats()
}

func (s *PostgresSink) Shutdown(ctx context.Context) error {
	err := s.logger.Shutdown(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.poolCloseOnce.Do(s.pool.Close)
	return err
}

var _ Sink = (*PostgresSink)(nil)
var _ Sink = (*DisabledSink)(nil)
