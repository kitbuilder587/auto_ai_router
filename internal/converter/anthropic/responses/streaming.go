package anthropicresponses

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/converter/anthropic"
	"github.com/mixaill76/auto_ai_router/internal/converter/responses"
)

// anthropicStreamAccumulator tracks state across Anthropic SSE streaming events.
type anthropicStreamAccumulator struct {
	responseID string
	model      string
	createdAt  int64
	meta       *responses.ResponsesMetadata

	// Accumulated content by block index
	currentBlockType   string
	currentBlockID     string
	currentBlockName   string
	currentText        string
	currentThinking    string
	currentToolArgs    string
	currentReasoningID string // ID assigned at content_block_start for "thinking"

	// Completed output items
	msgContent  []responses.OutputContent
	outputItems []responses.OutputItem
	// reasoningItem *responses.OutputItem

	// Usage
	inputTokens  int
	outputTokens int
	cachedTokens int

	// Stream status
	stopReason         string
	headerEmitted      bool
	messageStarted     bool
	messageItemID      string
	messageOutputIndex int // output_index where the message item was placed
	sequenceNumber     int
}

// TransformAnthropicStreamToResponses reads Anthropic SSE and writes Responses API SSE events.
// onComplete is called with the fully-built Response once the stream ends.
func TransformAnthropicStreamToResponses(
	reader io.Reader,
	writer io.Writer,
	model, responseID string,
	meta *responses.ResponsesMetadata,
	onComplete func(*responses.Response),
) error {
	if responseID == "" {
		responseID = generateResponseID()
	}
	acc := &anthropicStreamAccumulator{
		responseID: responseID,
		model:      model,
		createdAt:  time.Now().Unix(),
		meta:       meta,
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event anthropic.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			slog.Debug("[anthropicresponses/streaming] failed to parse event", "error", err)
			continue
		}

		if err := processAnthropicEvent(writer, acc, &event); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("stream read error: %w", err)
	}

	if err := emitAnthropicCompletionEvents(writer, acc); err != nil {
		return err
	}

	if onComplete != nil {
		onComplete(buildAnthropicCompletedResponse(acc))
	}
	return nil
}

func processAnthropicEvent(w io.Writer, acc *anthropicStreamAccumulator, event *anthropic.AnthropicStreamEvent) error {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			acc.inputTokens = event.Message.Usage.InputTokens
			acc.cachedTokens = event.Message.Usage.CacheReadInputTokens
		}

	case "content_block_start":
		if event.ContentBlock == nil {
			return nil
		}
		acc.currentBlockType = event.ContentBlock.Type
		acc.currentBlockID = event.ContentBlock.ID
		acc.currentBlockName = event.ContentBlock.Name
		acc.currentText = ""
		acc.currentThinking = ""
		acc.currentToolArgs = ""

		switch event.ContentBlock.Type {
		case "thinking":
			if !acc.headerEmitted {
				if err := emitAnthropicHeaderEvents(w, acc); err != nil {
					return err
				}
			}
			// Announce the reasoning item immediately so the client knows its output_index.
			acc.currentReasoningID = generateItemID("rs_")
			outputIdx := len(acc.outputItems)
			if err := writeAnthropicSSE(w, "response.output_item.added", map[string]interface{}{
				"type":         "response.output_item.added",
				"output_index": outputIdx,
				"item": map[string]interface{}{
					"type":    "reasoning",
					"id":      acc.currentReasoningID,
					"status":  "in_progress",
					"summary": []interface{}{},
				},
			}, acc); err != nil {
				return err
			}

		case "tool_use":
			// function_call item will be emitted on block stop
			if !acc.headerEmitted {
				if err := emitAnthropicHeaderEvents(w, acc); err != nil {
					return err
				}
			}
		}

	case "content_block_delta":
		if event.Delta == nil {
			return nil
		}
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				if err := handleAnthropicTextDelta(w, acc, event.Delta.Text); err != nil {
					return err
				}
			}
		case "thinking_delta":
			acc.currentThinking += event.Delta.Thinking
		case "input_json_delta":
			acc.currentToolArgs += event.Delta.PartialJSON
		}

	case "content_block_stop":
		if err := finalizeCurrentBlock(w, acc); err != nil {
			return err
		}

	case "message_delta":
		if event.Delta != nil && event.Delta.StopReason != "" {
			acc.stopReason = event.Delta.StopReason
		}
		if event.Usage != nil {
			acc.outputTokens = event.Usage.OutputTokens
		}

	case "message_stop":
		// Stream is ending; completion events emitted after the scan loop.
	}
	return nil
}

func handleAnthropicTextDelta(w io.Writer, acc *anthropicStreamAccumulator, delta string) error {
	if !acc.headerEmitted {
		if err := emitAnthropicHeaderEvents(w, acc); err != nil {
			return err
		}
	}
	if !acc.messageStarted {
		// Save the output index BEFORE emitAnthropicMessageStart sets messageStarted=true,
		acc.messageOutputIndex = len(acc.outputItems)
		if err := emitAnthropicMessageStart(w, acc); err != nil {
			return err
		}
	}
	acc.currentText += delta
	return writeAnthropicSSE(w, "response.output_text.delta", map[string]interface{}{
		"type":          "response.output_text.delta",
		"output_index":  acc.messageOutputIndex,
		"content_index": 0,
		"delta":         delta,
	}, acc)
}

func finalizeCurrentBlock(w io.Writer, acc *anthropicStreamAccumulator) error {
	switch acc.currentBlockType {
	case "text":
		if acc.currentText != "" {
			acc.msgContent = append(acc.msgContent, responses.OutputContent{
				Type:        "output_text",
				Text:        acc.currentText,
				Annotations: []responses.Annotation{},
			})
		}
		acc.currentText = ""

	case "thinking":
		if acc.currentThinking != "" {
			itemID := acc.currentReasoningID
			if itemID == "" {
				itemID = generateItemID("rs_")
			}
			item := responses.OutputItem{
				Type:   "reasoning",
				ID:     itemID,
				Status: "completed",
				Summary: []responses.OutputContent{
					{Type: "summary_text", Text: acc.currentThinking},
				},
			}
			acc.outputItems = append(acc.outputItems, item)
		}
		acc.currentReasoningID = ""

	case "tool_use":
		argsJSON := acc.currentToolArgs
		if argsJSON == "" {
			argsJSON = "{}"
		}
		item := responses.OutputItem{
			Type:      "function_call",
			ID:        generateItemID("fc_"),
			Status:    "completed",
			CallID:    acc.currentBlockID,
			Name:      acc.currentBlockName,
			Arguments: argsJSON,
		}
		acc.outputItems = append(acc.outputItems, item)
	}

	acc.currentBlockType = ""
	return nil
}

func emitAnthropicHeaderEvents(w io.Writer, acc *anthropicStreamAccumulator) error {
	acc.headerEmitted = true
	respObj := buildAnthropicInProgressResponse(acc)
	if err := writeAnthropicSSE(w, "response.created", map[string]interface{}{
		"type":     "response.created",
		"response": respObj,
	}, acc); err != nil {
		return err
	}
	return writeAnthropicSSE(w, "response.in_progress", map[string]interface{}{
		"type":     "response.in_progress",
		"response": respObj,
	}, acc)
}

func emitAnthropicMessageStart(w io.Writer, acc *anthropicStreamAccumulator) error {
	acc.messageStarted = true
	acc.messageItemID = generateItemID("msg_")
	outputIdx := len(acc.outputItems)

	if err := writeAnthropicSSE(w, "response.output_item.added", map[string]interface{}{
		"type":         "response.output_item.added",
		"output_index": outputIdx,
		"item": map[string]interface{}{
			"type":    "message",
			"id":      acc.messageItemID,
			"status":  "in_progress",
			"role":    "assistant",
			"content": []interface{}{},
		},
	}, acc); err != nil {
		return err
	}

	return writeAnthropicSSE(w, "response.content_part.added", map[string]interface{}{
		"type":          "response.content_part.added",
		"output_index":  outputIdx,
		"content_index": 0,
		"part": map[string]interface{}{
			"type":        "output_text",
			"text":        "",
			"annotations": []interface{}{},
		},
	}, acc)
}

func emitAnthropicCompletionEvents(w io.Writer, acc *anthropicStreamAccumulator) error {
	if !acc.headerEmitted {
		if err := emitAnthropicHeaderEvents(w, acc); err != nil {
			return err
		}
	}

	outputIdx := 0

	// Close previously completed non-message items (reasoning, tool calls from streaming).
	for _, item := range acc.outputItems {
		if err := writeAnthropicSSE(w, "response.output_item.done", map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": outputIdx,
			"item":         item,
		}, acc); err != nil {
			return err
		}
		outputIdx++
	}

	// Close message item if started.
	fullText := ""
	for _, c := range acc.msgContent {
		fullText += c.Text
	}
	if acc.messageStarted && fullText != "" {
		if err := writeAnthropicSSE(w, "response.output_text.done", map[string]interface{}{
			"type":          "response.output_text.done",
			"output_index":  outputIdx,
			"content_index": 0,
			"text":          fullText,
		}, acc); err != nil {
			return err
		}
		if err := writeAnthropicSSE(w, "response.content_part.done", map[string]interface{}{
			"type":          "response.content_part.done",
			"output_index":  outputIdx,
			"content_index": 0,
			"part": map[string]interface{}{
				"type":        "output_text",
				"text":        fullText,
				"annotations": []interface{}{},
			},
		}, acc); err != nil {
			return err
		}
		if err := writeAnthropicSSE(w, "response.output_item.done", map[string]interface{}{
			"type":         "response.output_item.done",
			"output_index": outputIdx,
			"item": map[string]interface{}{
				"type":    "message",
				"id":      acc.messageItemID,
				"status":  "completed",
				"role":    "assistant",
				"content": acc.msgContent,
			},
		}, acc); err != nil {
			return err
		}
	}

	return writeAnthropicSSE(w, "response.completed", map[string]interface{}{
		"type":     "response.completed",
		"response": buildAnthropicCompletedResponse(acc),
	}, acc)
}

func buildAnthropicInProgressResponse(acc *anthropicStreamAccumulator) map[string]interface{} {
	resp := map[string]interface{}{
		"id":         acc.responseID,
		"object":     "response",
		"created_at": acc.createdAt,
		"model":      acc.model,
		"status":     "in_progress",
		"output":     []interface{}{},
		"metadata":   map[string]interface{}{},
	}
	if acc.meta != nil && acc.meta.PreviousResponseID != "" {
		resp["previous_response_id"] = acc.meta.PreviousResponseID
	}
	return resp
}

func buildAnthropicCompletedResponse(acc *anthropicStreamAccumulator) *responses.Response {
	status, incompleteDetails := anthropicStopReasonToStatus(acc.stopReason)

	var output []responses.OutputItem
	output = append(output, acc.outputItems...)

	fullText := ""
	for _, c := range acc.msgContent {
		fullText += c.Text
	}

	if acc.messageStarted {
		msgContent := acc.msgContent
		if len(msgContent) == 0 {
			msgContent = []responses.OutputContent{{Type: "output_text", Text: "", Annotations: []responses.Annotation{}}}
		}
		output = append(output, responses.OutputItem{
			Type:    "message",
			ID:      acc.messageItemID,
			Status:  "completed",
			Role:    "assistant",
			Content: msgContent,
		})
	}

	if len(output) == 0 {
		output = []responses.OutputItem{{
			Type:    "message",
			ID:      generateItemID("msg_"),
			Status:  "completed",
			Role:    "assistant",
			Content: []responses.OutputContent{{Type: "output_text", Text: "", Annotations: []responses.Annotation{}}},
		}}
	}

	usage := &responses.Usage{
		InputTokens:  acc.inputTokens,
		OutputTokens: acc.outputTokens,
		TotalTokens:  acc.inputTokens + acc.outputTokens,
		InputTokensDetails: responses.InputDetails{
			CachedTokens: acc.cachedTokens,
		},
	}

	completedAt := acc.createdAt
	metadata := map[string]string{}
	var prevRespID interface{}
	if acc.meta != nil {
		for k, v := range acc.meta.Metadata {
			metadata[k] = v
		}
		if acc.meta.PreviousResponseID != "" {
			prevRespID = acc.meta.PreviousResponseID
		}
	}

	return &responses.Response{
		ID:                 acc.responseID,
		Object:             "response",
		CreatedAt:          acc.createdAt,
		CompletedAt:        &completedAt,
		Model:              acc.model,
		Status:             status,
		IncompleteDetails:  incompleteDetails,
		Output:             output,
		Usage:              usage,
		Error:              nil,
		Metadata:           metadata,
		Tools:              []responses.Tool{},
		ParallelToolCalls:  true,
		PreviousResponseID: prevRespID,
		Instructions:       nil,
	}
}

func writeAnthropicSSE(w io.Writer, eventType string, data interface{}, acc *anthropicStreamAccumulator) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("writeAnthropicSSE marshal %s: %w", eventType, err)
	}
	acc.sequenceNumber++
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, b)
	return err
}
