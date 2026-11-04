package contracts

import (
	"bytes"
	"encoding/json"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// withAgentLabel applies the shared tracking extension to every inventory,
// leaving the historical captured descriptors as reference evidence.
func withAgentLabel(raw json.RawMessage) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(envelope["inputSchema"], &schema); err != nil {
		return nil, err
	}
	field, err := json.Marshal(map[string]any{
		"type": "string", "minLength": 1, "maxLength": wire.AgentMaxLength, "pattern": wire.AgentPattern,
		"description": "Optional caller/session label for activity tracking only; never used for authorization or routing. Use 1-4 ASCII letters, digits or dashes.",
	})
	if err != nil {
		return nil, err
	}
	properties := schema["properties"]
	if len(properties) == 0 {
		properties = json.RawMessage(`{}`)
	}
	properties, err = setObjectField(properties, "agent", field)
	if err != nil {
		return nil, err
	}
	inputSchema, err := setObjectField(envelope["inputSchema"], "properties", properties)
	if err != nil {
		return nil, err
	}
	return setObjectField(raw, "inputSchema", inputSchema)
}

// Preserve field order: validation reports existing required-field failures in
// the order captured from the reference implementation.
func setObjectField(raw json.RawMessage, name string, value json.RawMessage) (json.RawMessage, error) {
	keys, fields, err := orderedObject(raw)
	if err != nil {
		return nil, err
	}
	if _, exists := fields[name]; !exists {
		keys = append(keys, name)
	}
	fields[name] = value
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(fields[key])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
