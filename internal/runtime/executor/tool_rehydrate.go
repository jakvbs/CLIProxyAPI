package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// extractSessionID extracts the session UUID from metadata.user_id.
// Format: "user_<hash>_account__session_<uuid>" -> returns "<uuid>"
// Falls back to full user_id if session_ not found (for format changes).
func extractSessionID(body []byte) string {
	userID := gjson.GetBytes(body, "metadata.user_id").String()
	if userID == "" {
		return ""
	}
	idx := strings.Index(userID, "session_")
	if idx == -1 {
		// Fallback: use full user_id as cache key
		return strings.TrimSpace(userID)
	}
	tail := userID[idx+len("session_"):]
	if tail == "" {
		return strings.TrimSpace(userID)
	}
	// Handle potential suffix after UUID (unlikely but defensive)
	if cut := strings.Index(tail, "_"); cut != -1 {
		tail = tail[:cut]
	}
	return strings.TrimSpace(tail)
}

// extractToolNames returns tool names from the request's tools array.
func extractToolNames(body []byte) []string {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, tool := range tools.Array() {
		name := tool.Get("name").String()
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// extractToolReferences finds tool names referenced in conversation history.
// Looks for:
// - tool_search_tool_result blocks with content.tool_references[].tool_name
// - tool_use blocks with name field
func extractToolReferences(body []byte) []string {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string

	for _, msg := range msgs.Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "tool_search_tool_result":
				// Path: content.tool_references[].tool_name (or .name as fallback)
				refs := part.Get("content.tool_references")
				if refs.IsArray() {
					for _, ref := range refs.Array() {
						// Support both tool_name and name fields
						name := ref.Get("tool_name").String()
						if name == "" {
							name = ref.Get("name").String()
						}
						if name == "" {
							continue
						}
						if _, ok := seen[name]; ok {
							continue
						}
						seen[name] = struct{}{}
						out = append(out, name)
					}
				}
			case "tool_use":
				// Direct tool usage
				name := part.Get("name").String()
				if name == "" {
					continue
				}
				if _, ok := seen[name]; ok {
					continue
				}
				seen[name] = struct{}{}
				out = append(out, name)
			}
		}
	}
	return out
}

// rehydrateTools is the main function that:
// 1. Caches tools from requests that have them
// 2. Injects cached tools when request is missing tools but history references them
//
// Returns the potentially modified body.
func rehydrateTools(cache *ToolCache, body []byte) []byte {
	if cache == nil || len(body) == 0 {
		return body
	}
	sessionID := extractSessionID(body)
	if sessionID == "" {
		return body
	}

	// Always cache tools if present in request
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		cache.StoreTools(sessionID, []byte(toolsResult.Raw))
	}

	// Find what tools are referenced in history
	needed := extractToolReferences(body)
	if len(needed) == 0 {
		return body
	}

	// Find what tools are already in the request
	have := extractToolNames(body)
	if len(have) > 0 {
		haveSet := make(map[string]struct{}, len(have))
		for _, n := range have {
			haveSet[n] = struct{}{}
		}
		var missing []string
		for _, n := range needed {
			if _, ok := haveSet[n]; !ok {
				missing = append(missing, n)
			}
		}
		needed = missing
	}
	if len(needed) == 0 {
		return body
	}

	// Get missing tools from cache
	cached := cache.GetTools(sessionID, needed)
	if len(cached) == 0 {
		return body
	}

	// If tools array missing, set it to cached array
	if !toolsResult.Exists() || !toolsResult.IsArray() {
		if updated, err := sjson.SetRawBytes(body, "tools", cached); err == nil {
			return updated
		}
		return body
	}

	// Append missing cached tools to existing tools array
	cachedArr := gjson.ParseBytes(cached)
	if !cachedArr.IsArray() {
		return body
	}
	out := body
	for _, tool := range cachedArr.Array() {
		updated, err := sjson.SetRawBytes(out, "tools.-1", []byte(tool.Raw))
		if err == nil {
			out = updated
		}
	}
	return out
}
