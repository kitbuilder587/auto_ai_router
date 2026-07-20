package spendsink

import (
	"context"
	"errors"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/litellmdb/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisabledSinkIsFailOpen(t *testing.T) {
	sink := NewDisabledSink("disabled")

	assert.False(t, sink.IsEnabled())
	assert.False(t, sink.IsHealthy())
	require.NoError(t, sink.LogSpend(&models.SpendLogEntry{RequestID: "req-1"}))
	require.NoError(t, sink.Shutdown(context.Background()))
	assert.Equal(t, "disabled", sink.DisabledReason())
}

func TestValidateDatabaseName(t *testing.T) {
	tests := []struct {
		name     string
		actual   string
		expected string
		wantErr  error
	}{
		{name: "matches", actual: "test-db", expected: "test-db"},
		{name: "does not trim or fold", actual: "Test-DB", expected: "test-db", wantErr: ErrUnexpectedDatabase},
		{name: "mismatch", actual: "vsellm-db", expected: "test-db", wantErr: ErrUnexpectedDatabase},
		{name: "empty actual", actual: "", expected: "test-db", wantErr: ErrUnexpectedDatabase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDatabaseName(tt.actual, tt.expected)
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr))
		})
	}
}
