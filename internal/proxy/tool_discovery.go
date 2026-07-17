package proxy

import (
	"encoding/json"
	"strings"
)

// extractOpenAIChatToolNames returns the unique function names declared by an
// already-validated OpenAI Chat request. Discovery is deliberately a sidecar:
// malformed optional tool declarations are ignored and the request bytes are
// never rewritten. Responses and native Anthropic requests are outside this
// compatibility surface.
func extractOpenAIChatToolNames(path string, body []byte) []string {
	path = strings.TrimSuffix(path, "/")
	if path != "/v1/chat/completions" && path != "/chat/completions" {
		return nil
	}

	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil
	}
	tools, ok := request["tools"].([]any)
	if !ok {
		return nil
	}

	names := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := function["name"].(string)
		if !ok || name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	return names
}
