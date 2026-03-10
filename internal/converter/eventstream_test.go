package converter

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEventStreamToSSE(t *testing.T) {
	// Create a simple event stream with one message
	// Frame format: [4 bytes total length][4 bytes headers length][4 bytes prelude CRC][N bytes headers][M bytes payload][4 bytes message CRC]

	// Payload: {"bytes":"<base64>","p":""}
	innerJSON := `{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`
	innerBase64 := base64.StdEncoding.EncodeToString([]byte(innerJSON))
	payload := []byte(`{"bytes":"` + innerBase64 + `","p":""}`)

	// Headers are empty (0 bytes)
	headersLength := uint32(0)
	headersBytes := []byte{}

	// Payload + headers
	payloadAndHeaders := make([]byte, len(headersBytes)+len(payload))
	copy(payloadAndHeaders, headersBytes)
	copy(payloadAndHeaders[len(headersBytes):], payload)

	// Total = 12 (prelude) + headers + payload + 4 (message CRC)
	totalLength := uint32(12 + len(headersBytes) + len(payload) + 4)

	// Build the frame
	frame := make([]byte, 12+len(payloadAndHeaders)+4)
	binary.BigEndian.PutUint32(frame[0:4], totalLength)
	binary.BigEndian.PutUint32(frame[4:8], headersLength)
	// Skip prelude CRC (bytes 8-12)
	copy(frame[12:], payloadAndHeaders)
	// Skip message CRC at the end (last 4 bytes)

	reader := bytes.NewReader(frame)
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	require.NoError(t, err)

	output := writer.String()
	assert.Contains(t, output, "data: ")
	assert.Contains(t, output, innerJSON)
}

func TestDecodeEventStreamToSSE_EmptyPayload(t *testing.T) {
	// Test with empty bytes field - should skip
	innerJSON := `{"type":"message_start"}`
	innerBase64 := base64.StdEncoding.EncodeToString([]byte(innerJSON))
	payload := []byte(`{"bytes":"` + innerBase64 + `","p":""}`)

	headersLength := uint32(0)
	totalLength := uint32(12 + len(payload) + 4)

	frame := make([]byte, 12+len(payload)+4)
	binary.BigEndian.PutUint32(frame[0:4], totalLength)
	binary.BigEndian.PutUint32(frame[4:8], headersLength)
	copy(frame[12:], payload)

	reader := bytes.NewReader(frame)
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	require.NoError(t, err)

	// Should still process the event
	assert.Contains(t, writer.String(), innerJSON)
}

func TestDecodeEventStreamToSSE_EOF(t *testing.T) {
	// Test with empty reader - should return nil error
	reader := bytes.NewReader([]byte{})
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	assert.NoError(t, err)
}

func TestDecodeEventStreamToSSE_InvalidBase64(t *testing.T) {
	// Payload with invalid base64 in bytes field - should skip this frame
	payload := []byte(`{"bytes":"!!!invalid!!!","p":""}`)

	headersLength := uint32(0)
	totalLength := uint32(12 + len(payload) + 4)

	frame := make([]byte, 12+len(payload)+4)
	binary.BigEndian.PutUint32(frame[0:4], totalLength)
	binary.BigEndian.PutUint32(frame[4:8], headersLength)
	copy(frame[12:], payload)

	reader := bytes.NewReader(frame)
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	require.NoError(t, err)

	// Should not write anything for invalid base64
	assert.Empty(t, writer.String())
}

func TestDecodeEventStreamToSSE_MultipleFrames(t *testing.T) {
	// Create multiple frames
	frames := []string{}

	for i := 0; i < 3; i++ {
		innerJSON := `{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + string(rune('a'+i)) + `"}}`
		innerBase64 := base64.StdEncoding.EncodeToString([]byte(innerJSON))
		payload := []byte(`{"bytes":"` + innerBase64 + `","p":""}`)

		headersLength := uint32(0)
		totalLength := uint32(12 + len(payload) + 4)

		frame := make([]byte, 12+len(payload)+4)
		binary.BigEndian.PutUint32(frame[0:4], totalLength)
		binary.BigEndian.PutUint32(frame[4:8], headersLength)
		copy(frame[12:], payload)

		frames = append(frames, string(frame))
	}

	reader := bytes.NewReader([]byte(frames[0] + frames[1] + frames[2]))
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	require.NoError(t, err)

	output := writer.String()
	// Should have 3 data lines
	assert.Contains(t, output, `text":"a"`)
	assert.Contains(t, output, `text":"b"`)
	assert.Contains(t, output, `text":"c"`)
}

func TestDecodeEventStreamToSSE_InvalidJSONPayload(t *testing.T) {
	// Payload that is not valid JSON - should skip this frame
	payload := []byte(`not valid json`)

	headersLength := uint32(0)
	totalLength := uint32(12 + len(payload) + 4)

	frame := make([]byte, 12+len(payload)+4)
	binary.BigEndian.PutUint32(frame[0:4], totalLength)
	binary.BigEndian.PutUint32(frame[4:8], headersLength)
	copy(frame[12:], payload)

	reader := bytes.NewReader(frame)
	var writer bytes.Buffer

	err := DecodeEventStreamToSSE(reader, &writer)
	require.NoError(t, err)

	// Should not write anything for invalid JSON
	assert.Empty(t, writer.String())
}
