package vertexresponses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildVertexSSEStream(events []map[string]interface{}) string {
	var sb strings.Builder
	for _, e := range events {
		b, _ := json.Marshal(e)
		sb.WriteString("data: ")
		sb.Write(b)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func parseVertexSSEEvents(output string) []map[string]interface{} {
	var events []map[string]interface{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var e map[string]interface{}
		if json.Unmarshal([]byte(data), &e) == nil {
			events = append(events, e)
		}
	}
	return events
}

func TestTransformVertexStreamToResponses_MessageEventsIncludeRequiredFields(t *testing.T) {
	stream := buildVertexSSEStream([]map[string]interface{}{
		{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"role": "model",
						"parts": []map[string]interface{}{
							{"text": "hello"},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]interface{}{
				"promptTokenCount":     5,
				"candidatesTokenCount": 3,
				"totalTokenCount":      8,
			},
		},
	})

	var out bytes.Buffer
	err := TransformVertexStreamToResponses(
		strings.NewReader(stream), &out, "gemini-test", "", nil, nil,
	)
	require.NoError(t, err)

	events := parseVertexSSEEvents(out.String())
	require.NotEmpty(t, events)

	var messageItemID string
	for _, e := range events {
		_, hasSeq := e["sequence_number"]
		assert.True(t, hasSeq, "every event must include sequence_number: %#v", e)

		typ, _ := e["type"].(string)
		if typ == "response.output_item.added" {
			item, _ := e["item"].(map[string]interface{})
			if item != nil && item["type"] == "message" {
				messageItemID, _ = item["id"].(string)
			}
		}
	}
	require.NotEmpty(t, messageItemID)

	for _, e := range events {
		typ, _ := e["type"].(string)
		switch typ {
		case "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done":
			itemID, _ := e["item_id"].(string)
			assert.Equal(t, messageItemID, itemID, "event %s must include matching item_id", typ)
		}
	}
}
