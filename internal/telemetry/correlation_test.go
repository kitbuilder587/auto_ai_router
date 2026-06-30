package telemetry

import (
	"context"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// capturingLogExporter records exported log records for assertions.
type capturingLogExporter struct {
	records []sdklog.Record
}

func (e *capturingLogExporter) Export(ctx context.Context, recs []sdklog.Record) error {
	e.records = append(e.records, recs...)
	return nil
}

func (e *capturingLogExporter) Shutdown(ctx context.Context) error   { return nil }
func (e *capturingLogExporter) ForceFlush(ctx context.Context) error { return nil }

// TestLogTraceCorrelation verifies the full correlation chain: a *Context slog
// call inside an active span must produce an OTLP log record carrying that
// span's trace_id/span_id, with the context passing intact through MultiHandler.
func TestLogTraceCorrelation(t *testing.T) {
	exp := &capturingLogExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	otelHandler := otelslog.NewHandler(ScopeName, otelslog.WithLoggerProvider(lp))

	log := logger.NewMulti("debug", false, otelHandler)

	tp := sdktrace.NewTracerProvider() // AlwaysSample by default
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "test-op")

	log.InfoContext(ctx, "inside span", "key", "value")
	log.Info("outside span")

	span.End()
	require.NoError(t, lp.ForceFlush(context.Background()))
	require.Len(t, exp.records, 2)

	inSpan := exp.records[0]
	assert.True(t, inSpan.TraceID().IsValid(), "log inside span must carry trace_id")
	assert.Equal(t, span.SpanContext().TraceID(), inSpan.TraceID())
	assert.Equal(t, span.SpanContext().SpanID(), inSpan.SpanID())

	outSpan := exp.records[1]
	assert.False(t, outSpan.TraceID().IsValid(), "log outside span must have no trace_id")
}
