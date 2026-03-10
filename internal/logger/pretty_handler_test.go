package logger

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPrettyHandler_WithAttrs(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	// WithAttrs should return a handler (currently returns same handler)
	newHandler := handler.WithAttrs([]slog.Attr{
		{Key: "test", Value: slog.StringValue("value")},
	})
	assert.NotNil(t, newHandler)
}

func TestPrettyHandler_WithGroup(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	// WithGroup should return a handler (currently returns same handler)
	newHandler := handler.WithGroup("testGroup")
	assert.NotNil(t, newHandler)
}

func TestPrettyHandler_Enabled(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	ctx := context.Background()

	// Debug should be disabled (Level is Info)
	assert.False(t, handler.Enabled(ctx, slog.LevelDebug))

	// Info should be enabled
	assert.True(t, handler.Enabled(ctx, slog.LevelInfo))

	// Error should be enabled
	assert.True(t, handler.Enabled(ctx, slog.LevelError))

	// Warn should be enabled
	assert.True(t, handler.Enabled(ctx, slog.LevelWarn))
}

func TestPrettyHandler_Enabled_DebugLevel(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelDebug,
		},
	}

	ctx := context.Background()

	// All levels should be enabled when level is Debug
	assert.True(t, handler.Enabled(ctx, slog.LevelDebug))
	assert.True(t, handler.Enabled(ctx, slog.LevelInfo))
	assert.True(t, handler.Enabled(ctx, slog.LevelWarn))
	assert.True(t, handler.Enabled(ctx, slog.LevelError))
}

func TestPrettyHandler_Enabled_ErrorLevel(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelError,
		},
	}

	ctx := context.Background()

	// Only Error level should be enabled when level is Error
	assert.False(t, handler.Enabled(ctx, slog.LevelDebug))
	assert.False(t, handler.Enabled(ctx, slog.LevelInfo))
	assert.False(t, handler.Enabled(ctx, slog.LevelWarn))
	assert.True(t, handler.Enabled(ctx, slog.LevelError))
}

func TestPrettyHandler_Handle(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	testTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	record := slog.NewRecord(testTime, slog.LevelInfo, "test message", 0)

	// Should not panic
	err := handler.Handle(context.Background(), record)
	assert.NoError(t, err)
}

func TestPrettyHandler_Handle_DifferentLevels(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	testTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	testCases := []slog.Level{
		slog.LevelDebug,
		slog.LevelInfo,
		slog.LevelWarn,
		slog.LevelError,
	}

	for _, level := range testCases {
		record := slog.NewRecord(testTime, level, "test message", 0)
		err := handler.Handle(context.Background(), record)
		assert.NoError(t, err, "should handle level %v", level)
	}
}

func TestPrettyHandler_Handle_EmptyMessage(t *testing.T) {
	handler := &PrettyHandler{
		opts: &slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	}

	testTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	record := slog.NewRecord(testTime, slog.LevelInfo, "", 0)

	err := handler.Handle(context.Background(), record)
	assert.NoError(t, err)
}
