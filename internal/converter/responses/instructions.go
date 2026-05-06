package responses

import "strings"

// ExtractInstructionsText extracts a plain-text string from the instructions field.
// instructions can be a string or an array of message objects.
func ExtractInstructionsText(instructions interface{}) string {
	if instructions == nil {
		return ""
	}
	if s, ok := instructions.(string); ok {
		return s
	}
	// Array form — join content strings.
	var sb strings.Builder
	if arr, ok := instructions.([]interface{}); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				if content, ok := m["content"].(string); ok && content != "" {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(content)
				}
			}
		}
	}
	return sb.String()
}
