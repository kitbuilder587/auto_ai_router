package logger

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// recordingHandler captures records for assertions.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }

func (h *recordingHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record)
	return nil
}

func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(name string) slog.Handler       { return h }

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func TestNewMulti_FansOutToExtraHandler(t *testing.T) {
	rec := &recordingHandler{}
	log := NewMulti("info", true, rec)

	log.Info("hello", "key", "value")
	log.Debug("filtered out") // below info level

	assert.Equal(t, 1, rec.count())
	assert.Equal(t, "hello", rec.records[0].Message)
}

func TestNewMulti_RespectsLevelForExtraHandlers(t *testing.T) {
	rec := &recordingHandler{}
	log := NewMulti("error", true, rec)

	log.Info("not exported")
	log.Warn("not exported either")
	log.Error("exported")

	assert.Equal(t, 1, rec.count())
	assert.Equal(t, "exported", rec.records[0].Message)
}

func TestNewMulti_NilExtraHandlersSkipped(t *testing.T) {
	// Must not panic and must behave like a plain pretty logger.
	log := NewMulti("info", true, nil, nil)
	log.Info("works fine")
}

func TestNewMulti_StdoutDisabled(t *testing.T) {
	rec := &recordingHandler{}
	log := NewMulti("info", false, rec)

	log.Info("only to extra handler")

	assert.Equal(t, 1, rec.count())
	assert.Equal(t, "only to extra handler", rec.records[0].Message)
}

func TestNewMulti_NoHandlersDiscards(t *testing.T) {
	// stdout off and no extra handlers: logger must be a safe no-op.
	log := NewMulti("info", false, nil)
	log.Info("goes nowhere")
	log.Error("also nowhere")
}

func TestMultiHandler_WithAttrsPropagates(t *testing.T) {
	rec := &recordingHandler{}
	log := NewMulti("info", true, rec).With("component", "test")

	log.Info("with attrs")

	assert.Equal(t, 1, rec.count())
}
