package proxy

import (
	"encoding/json"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/kafkalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Kafka enqueue failure is annotated on the exact shadow entry before the
// authoritative writer sees it, so an external replay job can find the row.
func TestPublishKafkaSpendCopy_FlagsShadowEntryOnQueueFull(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	prx.kafkaLog = &stubKafkaManager{enabled: true, err: kafkalog.ErrQueueFull}
	logCtx := testLogCtx(t)
	entry := prx.buildSpendEntry(logCtx)
	require.NotNil(t, entry)

	err := prx.publishKafkaSpendCopy(logCtx, entry)
	require.ErrorIs(t, err, kafkalog.ErrQueueFull)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	assert.Equal(t, true, metadata["kafka_fallback"])
	assert.Equal(t, "queue_full", metadata["kafka_fallback_reason"])
}

func TestPublishKafkaSpendCopy_NoFallbackFlagOnSuccess(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	kafkaStub := &stubKafkaManager{enabled: true}
	prx.kafkaLog = kafkaStub
	logCtx := testLogCtx(t)
	entry := prx.buildSpendEntry(logCtx)
	require.NotNil(t, entry)

	require.NoError(t, prx.publishKafkaSpendCopy(logCtx, entry))
	require.Len(t, kafkaStub.events, 1)

	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &metadata))
	_, hasFlag := metadata["kafka_fallback"]
	assert.False(t, hasFlag)
}

func TestPublishKafkaSpendCopy_ExactlyOnceAcrossReplayPaths(t *testing.T) {
	prx := NewTestProxyBuilder().Build()
	kafkaStub := &stubKafkaManager{enabled: true}
	prx.kafkaLog = kafkaStub
	logCtx := testLogCtx(t)
	entry := prx.buildSpendEntry(logCtx)
	require.NotNil(t, entry)

	require.NoError(t, prx.publishKafkaSpendCopy(logCtx, entry))
	require.NoError(t, prx.publishKafkaSpendCopy(logCtx, entry))
	require.Len(t, kafkaStub.events, 1)
	assert.Equal(t, entry.RequestID, kafkaStub.events[0].RequestID)
}
