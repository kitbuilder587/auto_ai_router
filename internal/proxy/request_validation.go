package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// requestValidationError describes a client-owned request-shape error detected
// before model routing, credential selection, provider calls, or spend logging.
type requestValidationError struct {
	Message string
	Param   string
}

func invalidRequest(param, message string) *requestValidationError {
	return &requestValidationError{Message: message, Param: param}
}

// validateRequestBody validates only the stable structural contract shared by
// the supported public endpoints. Provider-specific limits and optional fields
// remain the provider's responsibility.
func validateRequestBody(path, contentType string, body []byte) *requestValidationError {
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return validateMultipartRequest(path, contentType, body)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		return invalidRequest("body", "request body is required")
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return invalidRequest("body", "invalid JSON request body")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return invalidRequest("body", "invalid JSON request body")
	}

	object, ok := value.(map[string]any)
	if !ok {
		return invalidRequest("body", "request body must be a JSON object")
	}

	model, ok := object["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return invalidRequest("model", "model field must be a non-empty string")
	}

	if stream, exists := object["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			return invalidRequest("stream", "stream field must be a boolean")
		}
	}

	switch path {
	case "/v1/chat/completions":
		messages, exists := object["messages"]
		if !exists {
			return invalidRequest("messages", "messages field is required")
		}
		items, ok := messages.([]any)
		if !ok {
			return invalidRequest("messages", "messages field must be an array")
		}
		if len(items) == 0 {
			return invalidRequest("messages", "messages field must not be empty")
		}
		for _, item := range items {
			if _, ok := item.(map[string]any); !ok {
				return invalidRequest("messages", "each message must be an object")
			}
		}
	case "/v1/completions":
		prompt, exists := object["prompt"]
		if !exists {
			return invalidRequest("prompt", "prompt field is required")
		}
		if !validCompletionPrompt(prompt) {
			return invalidRequest("prompt", "prompt field has an invalid type")
		}
	case "/v1/embeddings":
		input, exists := object["input"]
		if !exists {
			return invalidRequest("input", "input field is required")
		}
		if !validEmbeddingInput(input) {
			return invalidRequest("input", "input field must be a non-empty string or array")
		}
	case "/v1/responses":
		input, exists := object["input"]
		if !exists || !validResponsesInput(input) {
			return invalidRequest("input", "input field is required and must not be empty")
		}
	case "/v1/images/generations":
		prompt, ok := object["prompt"].(string)
		if !ok || strings.TrimSpace(prompt) == "" {
			return invalidRequest("prompt", "prompt field must be a non-empty string")
		}
	}

	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}

func validCompletionPrompt(value any) bool {
	switch current := value.(type) {
	case string:
		return current != ""
	case json.Number:
		return true
	case []any:
		if len(current) == 0 {
			return false
		}
		for _, item := range current {
			switch item.(type) {
			case string, json.Number:
			case []any:
				if !validNumericTokenArray(item) {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validNumericTokenArray(value any) bool {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if _, ok := item.(json.Number); !ok {
			return false
		}
	}
	return true
}

func validEmbeddingInput(value any) bool {
	switch current := value.(type) {
	case string:
		return current != ""
	case []any:
		if len(current) == 0 {
			return false
		}
		for _, item := range current {
			switch item.(type) {
			case string, json.Number:
			case []any:
				if !validNumericTokenArray(item) {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validResponsesInput(value any) bool {
	switch current := value.(type) {
	case string:
		return current != ""
	case []any:
		return len(current) > 0
	default:
		return false
	}
}

func validateMultipartRequest(path, contentType string, body []byte) *requestValidationError {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return invalidRequest("body", "invalid multipart request body")
	}

	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	modelFound := false
	imageFound := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalidRequest("body", "invalid multipart request body")
		}
		name := part.FormName()
		switch name {
		case "model":
			value, readErr := io.ReadAll(io.LimitReader(part, 1024*1024))
			if readErr != nil {
				return invalidRequest("model", "invalid model field")
			}
			modelFound = strings.TrimSpace(string(value)) != ""
		case "image", "image[]":
			imageFound = imageFound || part.FileName() != ""
		}
	}

	if !modelFound {
		return invalidRequest("model", "model field must be a non-empty string")
	}
	if path == "/v1/images/edits" && !imageFound {
		return invalidRequest("image", "image field is required")
	}
	return nil
}
