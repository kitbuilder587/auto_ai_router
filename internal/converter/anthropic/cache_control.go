package anthropic

import "encoding/json"

var cacheEphemeral = map[string]interface{}{"type": "ephemeral"}

// InjectCacheControl adds Anthropic prompt-caching markers to an OpenAI-format request body.
//
// It marks two cache breakpoints (standard Anthropic multi-turn caching pattern):
//  1. Last content block of the system message (stable across turns)
//  2. Last content block of the second-to-last user message (history boundary)
//
// If the body already contains any cache_control field, it is returned unchanged.
// Non-JSON or structurally unexpected bodies are also returned unchanged.
func InjectCacheControl(body []byte) []byte {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	messages, _ := req["messages"].([]interface{})
	if len(messages) == 0 {
		return body
	}

	if hasAnyCacheControl(messages) {
		return body
	}

	modified := false

	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "system" || role == "developer" {
			if markLastContentBlock(m) {
				modified = true
			}
		}
	}

	// Collect user message indices; mark the second-to-last one (history boundary).
	var userIdxs []int
	for i, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "user" {
			userIdxs = append(userIdxs, i)
		}
	}
	if len(userIdxs) >= 2 {
		histMsg, ok := messages[userIdxs[len(userIdxs)-2]].(map[string]interface{})
		if ok && markLastContentBlock(histMsg) {
			modified = true
		}
	}

	if !modified {
		return body
	}

	result, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return result
}

// hasAnyCacheControl reports whether any content block in messages already carries
// a cache_control field.
func hasAnyCacheControl(messages []interface{}) bool {
	for _, msg := range messages {
		m, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		if contentHasCacheControl(m["content"]) {
			return true
		}
	}
	return false
}

func contentHasCacheControl(content interface{}) bool {
	blocks, ok := content.([]interface{})
	if !ok {
		return false
	}
	for _, block := range blocks {
		b, ok := block.(map[string]interface{})
		if ok && b["cache_control"] != nil {
			return true
		}
	}
	return false
}

// markLastContentBlock adds cache_control to the last block of a message's content.
// String content is promoted to a single-element text block array so the marker
// survives the OpenAI→Anthropic conversion.
func markLastContentBlock(msg map[string]interface{}) bool {
	switch c := msg["content"].(type) {
	case string:
		if c == "" {
			return false
		}
		msg["content"] = []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          c,
				"cache_control": cacheEphemeral,
			},
		}
		return true
	case []interface{}:
		if len(c) == 0 {
			return false
		}
		last, ok := c[len(c)-1].(map[string]interface{})
		if !ok {
			return false
		}
		last["cache_control"] = cacheEphemeral
		return true
	}
	return false
}
