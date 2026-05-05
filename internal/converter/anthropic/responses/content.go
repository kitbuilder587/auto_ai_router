package anthropicresponses

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/mixaill76/auto_ai_router/internal/converter/anthropic"
)

// contentPartToAnthropic converts a Responses API content part map to an Anthropic ContentBlock.
// Returns nil when the part type is unknown and should be silently skipped.
func contentPartToAnthropic(partMap map[string]interface{}) (*anthropic.ContentBlock, error) {
	partType, _ := partMap["type"].(string)
	switch partType {
	case "input_text", "output_text", "text":
		text, _ := partMap["text"].(string)
		return &anthropic.ContentBlock{Type: "text", Text: text}, nil

	case "input_image":
		return convertInputImageToAnthropic(partMap)

	case "input_audio":
		// Anthropic does not support audio input natively.
		return nil, fmt.Errorf("input_audio is not supported by Anthropic")

	case "input_file":
		return convertInputFileToAnthropic(partMap)

	case "reasoning_text", "summary_text":
		text, _ := partMap["text"].(string)
		return &anthropic.ContentBlock{Type: "thinking", Thinking: text}, nil

	default:
		return nil, nil
	}
}

func convertInputImageToAnthropic(partMap map[string]interface{}) (*anthropic.ContentBlock, error) {
	var imgURL string
	switch v := partMap["image_url"].(type) {
	case string:
		imgURL = v
	case map[string]interface{}:
		imgURL, _ = v["url"].(string)
	}

	if imgURL == "" {
		return nil, fmt.Errorf("input_image: missing image_url")
	}

	// data: URL → base64 inline
	if strings.HasPrefix(imgURL, "data:") {
		mimeType, data, err := parseDataURL(imgURL)
		if err != nil {
			return nil, fmt.Errorf("input_image: %w", err)
		}
		return &anthropic.ContentBlock{
			Type: "image",
			Source: &anthropic.MediaSource{
				Type:      "base64",
				MediaType: mimeType,
				Data:      data,
			},
		}, nil
	}

	// Regular URL → url source type
	return &anthropic.ContentBlock{
		Type: "image",
		Source: &anthropic.MediaSource{
			Type: "url",
			URL:  imgURL,
		},
	}, nil
}

func convertInputFileToAnthropic(partMap map[string]interface{}) (*anthropic.ContentBlock, error) {
	fileURL, _ := partMap["file_url"].(string)
	if fileURL == "" {
		return nil, fmt.Errorf("input_file: missing file_url")
	}
	return &anthropic.ContentBlock{
		Type: "document",
		Source: &anthropic.MediaSource{
			Type: "url",
			URL:  fileURL,
		},
	}, nil
}

// parseDataURL parses a data: URL into mimeType and base64-encoded data string.
func parseDataURL(dataURL string) (mimeType, b64data string, err error) {
	rest := strings.TrimPrefix(dataURL, "data:")
	semi := strings.Index(rest, ";")
	if semi < 0 {
		return "", "", fmt.Errorf("invalid data URL: missing semicolon")
	}
	mimeType = rest[:semi]
	after := rest[semi+1:]
	if !strings.HasPrefix(after, "base64,") {
		return "", "", fmt.Errorf("invalid data URL: expected base64 encoding")
	}
	raw := after[7:]
	// Validate it's decodable but return raw base64 string (Anthropic wants the base64 string)
	if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
		return "", "", fmt.Errorf("invalid base64 data: %w", err)
	}
	return mimeType, raw, nil
}
