// Package telemetry initializes the OpenTelemetry SDK: an OTLP trace exporter
// with a TracerProvider and an OTLP log exporter with a LoggerProvider.
// Both are optional and controlled by the otel section of the YAML config.
//
// Traces use the global otel.TracerProvider / TextMapPropagator so that
// otelhttp server and client instrumentation picks them up automatically.
// Logs are exposed as a slog.Handler (otelslog bridge) that the application
// fans out to alongside the stdout handler.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

const (
	// ScopeName is the instrumentation scope reported for spans and log records.
	ScopeName = "github.com/mixaill76/auto_ai_router"

	shutdownTimeout = 10 * time.Second
)

// Telemetry holds initialized OTEL SDK providers.
// A nil *Telemetry is valid and means "OTEL disabled" — all methods are nil-safe.
type Telemetry struct {
	tracerProvider *sdktrace.TracerProvider
	loggerProvider *sdklog.LoggerProvider
	logHandler     slog.Handler
}

// Setup initializes OTEL exporters according to cfg.
// Returns nil (and no error) when OTEL is disabled in the config.
//
// diag is a logger for export diagnostics (batch sizes at DEBUG, export
// failures at WARN). It MUST NOT itself ship records via OTEL: logging about
// log export through the OTEL pipeline would generate a new record per export
// batch and feed the pipeline forever. Pass a stdout-only logger; nil disables
// diagnostics.
func Setup(ctx context.Context, cfg *config.OTELConfig, version, commit string, diag *slog.Logger) (*Telemetry, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	if diag == nil {
		diag = slog.New(slog.DiscardHandler)
	}

	// Surface internal SDK errors (queue overflows, async export problems)
	// instead of the default stderr printing.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		diag.Warn("OpenTelemetry SDK error", "error", err)
	}))

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to build OTEL resource: %w", err)
	}

	t := &Telemetry{}

	if cfg.TracesEnabled {
		traceExporter, err := newTraceExporter(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
		t.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(&debugSpanExporter{inner: traceExporter, log: diag}),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceSampleRatio))),
		)
		otel.SetTracerProvider(t.tracerProvider)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
	}

	if cfg.LogsEnabled {
		logExporter, err := newLogExporter(ctx, cfg)
		if err != nil {
			// Roll back the partially initialized tracer provider so we don't
			// leak a background batch goroutine on a failed Setup.
			if t.tracerProvider != nil {
				shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
				_ = t.tracerProvider.Shutdown(shutdownCtx)
				cancel()
			}
			return nil, fmt.Errorf("failed to create OTLP log exporter: %w", err)
		}
		t.loggerProvider = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(&debugLogExporter{inner: logExporter, log: diag})),
		)
		t.logHandler = otelslog.NewHandler(ScopeName,
			otelslog.WithLoggerProvider(t.loggerProvider),
			otelslog.WithVersion(version),
		)
	}

	return t, nil
}

// LogHandler returns the slog.Handler that ships records via OTLP,
// or nil when log export is disabled.
func (t *Telemetry) LogHandler() slog.Handler {
	if t == nil {
		return nil
	}
	return t.logHandler
}

// TracesEnabled reports whether the TracerProvider was initialized.
func (t *Telemetry) TracesEnabled() bool {
	return t != nil && t.tracerProvider != nil
}

// Shutdown flushes pending spans and log records and stops the providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	var errs []error
	if t.loggerProvider != nil {
		if err := t.loggerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("logger provider shutdown: %w", err))
		}
	}
	if t.tracerProvider != nil {
		if err := t.tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
		}
	}
	return errors.Join(errs...)
}

// debugSpanExporter wraps a span exporter to log every export batch:
// successes at DEBUG (visible with logging_level: debug), failures at WARN.
type debugSpanExporter struct {
	inner sdktrace.SpanExporter
	log   *slog.Logger
}

func (e *debugSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	start := time.Now()
	err := e.inner.ExportSpans(ctx, spans)
	if err != nil {
		e.log.Warn("OTLP trace export failed", "spans", len(spans), "error", err)
		return err
	}
	e.log.Debug("OTLP trace export succeeded", "spans", len(spans), "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

func (e *debugSpanExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// debugLogExporter wraps a log exporter to log every export batch.
// Diagnostics go to the stdout-only diag logger (see Setup) — never through
// the OTEL pipeline itself, which would loop.
type debugLogExporter struct {
	inner sdklog.Exporter
	log   *slog.Logger
}

func (e *debugLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	start := time.Now()
	err := e.inner.Export(ctx, records)
	if err != nil {
		e.log.Warn("OTLP log export failed", "records", len(records), "error", err)
		return err
	}
	e.log.Debug("OTLP log export succeeded", "records", len(records), "duration", time.Since(start).Round(time.Millisecond))
	return nil
}

func (e *debugLogExporter) ForceFlush(ctx context.Context) error {
	return e.inner.ForceFlush(ctx)
}

func (e *debugLogExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// hasURLScheme reports whether the endpoint includes an explicit scheme
// (e.g. "http://collector:4318") as opposed to a bare "host:port".
func hasURLScheme(endpoint string) bool {
	return strings.Contains(endpoint, "://")
}

// withSignalPath appends the standard OTLP/HTTP signal path (e.g. "/v1/logs")
// to an endpoint URL that has no explicit path. WithEndpointURL uses the URL
// path as-is, so "http://collector:4318" would otherwise post to "/" and get
// a 404 from a standard collector.
func withSignalPath(endpointURL, signalPath string) string {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return endpointURL // let the exporter surface the parse error
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = signalPath
		return u.String()
	}
	return endpointURL
}

func newTraceExporter(ctx context.Context, cfg *config.OTELConfig) (*otlptrace.Exporter, error) {
	if cfg.Protocol == "http" {
		opts := []otlptracehttp.Option{}
		if hasURLScheme(cfg.Endpoint) {
			opts = append(opts, otlptracehttp.WithEndpointURL(withSignalPath(cfg.Endpoint, "/v1/traces")))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		return otlptracehttp.New(ctx, opts...)
	}

	opts := []otlptracegrpc.Option{}
	if hasURLScheme(cfg.Endpoint) {
		opts = append(opts, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	return otlptracegrpc.New(ctx, opts...)
}

func newLogExporter(ctx context.Context, cfg *config.OTELConfig) (sdklog.Exporter, error) {
	if cfg.Protocol == "http" {
		opts := []otlploghttp.Option{}
		if hasURLScheme(cfg.Endpoint) {
			opts = append(opts, otlploghttp.WithEndpointURL(withSignalPath(cfg.Endpoint, "/v1/logs")))
		} else {
			opts = append(opts, otlploghttp.WithEndpoint(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlploghttp.WithHeaders(cfg.Headers))
		}
		return otlploghttp.New(ctx, opts...)
	}

	opts := []otlploggrpc.Option{}
	if hasURLScheme(cfg.Endpoint) {
		opts = append(opts, otlploggrpc.WithEndpointURL(cfg.Endpoint))
	} else {
		opts = append(opts, otlploggrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlploggrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlploggrpc.WithHeaders(cfg.Headers))
	}
	return otlploggrpc.New(ctx, opts...)
}
