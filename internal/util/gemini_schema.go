package util

import (
	"encoding/json"
	"strings"
)

var geminiAllowedTypes = map[string]string{
	"OBJECT":  "OBJECT",
	"ARRAY":   "ARRAY",
	"STRING":  "STRING",
	"NUMBER":  "NUMBER",
	"INTEGER": "INTEGER",
	"BOOLEAN": "BOOLEAN",
}

// SanitizeSchemaForGemini attempts to down-level a JSON Schema so it is compatible
// with Gemini functionDeclarations. The Gemini schema dialect is stricter than
// OpenAI's, so we drop unsupported features (e.g. anyOf/allOf) and normalise
// primitive types. On failure we fall back to the original schema string so
// callers can still send something reasonable.
func SanitizeSchemaForGemini(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return `{}`, nil
	}

	var schema interface{}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		return raw, err
	}

	sanitized := sanitizeGeminiSchema(schema)
	out, err := json.Marshal(sanitized)
	if err != nil {
		return raw, err
	}

	return string(out), nil
}

func sanitizeGeminiSchema(node interface{}) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		cleaned := make(map[string]interface{}, len(v))
		for key, val := range v {
			lowerKey := strings.ToLower(key)
			switch lowerKey {
			case "$defs", "definitions", "$ref", "anyof", "allof", "oneof", "not", "if", "then", "else", "dependentrequired", "dependentschemas", "patternproperties":
				continue
			case "nullable":
				continue
			case "const":
				if val != nil {
					cleaned["enum"] = []interface{}{val}
				}
				continue
			case "type":
				switch typed := val.(type) {
				case string:
					if normalized := normalizeGeminiType(typed); normalized != "" {
						cleaned[key] = normalized
					}
				case []interface{}:
					for _, candidate := range typed {
						if s, ok := candidate.(string); ok {
							if normalized := normalizeGeminiType(s); normalized != "" {
								cleaned[key] = normalized
								break
							}
						}
					}
				}
				continue
			case "enum":
				if enumVals := sanitizeGeminiEnum(val); len(enumVals) > 0 {
					cleaned[key] = enumVals
				}
				continue
			case "required":
				if required := sanitizeGeminiRequired(val); len(required) > 0 {
					cleaned[key] = required
				}
				continue
			case "properties":
				if props, ok := val.(map[string]interface{}); ok {
					propClean := make(map[string]interface{}, len(props))
					for propKey, propVal := range props {
						if sanitizedProp := sanitizeGeminiSchema(propVal); sanitizedProp != nil {
							propClean[propKey] = sanitizedProp
						}
					}
					cleaned[key] = propClean
				}
				continue
			case "items":
				if sanitizedItems := sanitizeGeminiSchema(val); sanitizedItems != nil {
					cleaned[key] = sanitizedItems
				}
				continue
			case "additionalproperties":
				// Gemini currently only honours false, so retain that and drop everything else.
				if b, ok := val.(bool); ok && !b {
					cleaned[key] = false
				}
				continue
			}

			if sanitized := sanitizeGeminiSchema(val); sanitized != nil {
				cleaned[key] = sanitized
			}
		}
		return cleaned
	case []interface{}:
		arr := make([]interface{}, 0, len(v))
		for _, item := range v {
			if sanitized := sanitizeGeminiSchema(item); sanitized != nil {
				arr = append(arr, sanitized)
			}
		}
		return arr
	default:
		return node
	}
}

func normalizeGeminiType(t string) string {
	upper := strings.ToUpper(strings.TrimSpace(t))
	if mapped, ok := geminiAllowedTypes[upper]; ok {
		return mapped
	}
	switch upper {
	case "OBJECT", "RECORD", "MAP":
		return "OBJECT"
	case "BOOL":
		return "BOOLEAN"
	case "DOUBLE", "FLOAT":
		return "NUMBER"
	case "INT":
		return "INTEGER"
	case "LIST":
		return "ARRAY"
	case "NULL", "ANY", "":
		return ""
	default:
		return ""
	}
}

func sanitizeGeminiEnum(val interface{}) []interface{} {
	items, ok := val.([]interface{})
	if !ok {
		return nil
	}
	enumVals := make([]interface{}, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case string, float64, bool, nil:
			enumVals = append(enumVals, item)
		}
	}
	return enumVals
}

func sanitizeGeminiRequired(val interface{}) []string {
	items, ok := val.([]interface{})
	if !ok {
		return nil
	}
	required := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				required = append(required, s)
			}
		}
	}
	return required
}
