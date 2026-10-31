package wire

import (
	"errors"
	"strings"
)

// ErrorResult is the baseline business-error envelope. Schema errors at the MCP
// SDK boundary deliberately use a separate, untagged content-only result.
func ErrorResult(err error) Result {
	message := "Error: " + err.Error()
	data := map[string]any{}
	var ce *Error
	if errors.As(err, &ce) {
		message = "CodexProError: " + ce.Message
		if ce.Code != "" {
			data["error_code"] = ce.Code
		}
		for key, value := range ce.Data {
			if key == "details" {
				if details, ok := value.(map[string]any); ok {
					for k, v := range details {
						data[k] = v
					}
				}
			} else {
				data[key] = value
			}
		}
	} else {
		var native *NativeError
		if errors.As(err, &native) && native.Name != "" {
			message = native.Name + ": " + err.Error()
		}
	}
	message = Redact(message)
	data["error"] = message
	pieces := []string{message}
	if recovery, ok := data["recovery"].(map[string]any); ok {
		if text, ok := recovery["message"].(string); ok && text != "" {
			pieces = append(pieces, "Recovery: "+text)
		}
	}
	if retry, ok := data["retry_unchanged"].(bool); ok && !retry {
		pieces = append(pieces, "Do not retry the same request unchanged. Refresh its inputs or use the suggested recovery action.")
	}
	return Result{Content: []Content{{"type": "text", "text": strings.Join(pieces, "\n\n")}}, StructuredContent: data, IsError: true}
}
