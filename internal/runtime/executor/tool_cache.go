package executor

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// ToolCache stores tool definitions per session to rehydrate requests
// that are missing tools but reference them in conversation history.
//
// Claude Code doesn't send tools in every request (optimization).
// When tool_search_tool_bm25 discovers tools and returns tool_references
// in history, but subsequent requests don't include those tools,
// Claude API returns 400 "Tool reference 'X' not found in available tools".
//
// This cache stores tools from requests that include them and injects
// them back when needed.
type ToolCache struct {
	mu       sync.RWMutex
	sessions map[string]*sessionTools
	ttl      time.Duration
}

type sessionTools struct {
	tools     map[string]json.RawMessage // tool name -> raw JSON
	expiresAt time.Time
	lastSeen  time.Time
}

// NewToolCache creates a new tool cache with the given TTL.
// Recommended TTL is 30 minutes for conversational sessions.
func NewToolCache(ttl time.Duration) *ToolCache {
	return &ToolCache{
		sessions: make(map[string]*sessionTools),
		ttl:      ttl,
	}
}

// StoreTools extracts tool definitions from a JSON array and caches them.
// tools should be the raw JSON of the "tools" array from the request.
func (c *ToolCache) StoreTools(sessionID string, tools []byte) {
	if c == nil || sessionID == "" || len(tools) == 0 {
		return
	}
	root := gjson.ParseBytes(tools)
	if !root.IsArray() {
		return
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	sess := c.sessions[sessionID]
	if sess == nil {
		sess = &sessionTools{
			tools: make(map[string]json.RawMessage),
		}
		c.sessions[sessionID] = sess
	}
	sess.lastSeen = now
	sess.expiresAt = now.Add(c.ttl)

	for _, tool := range root.Array() {
		name := tool.Get("name").String()
		if name == "" {
			continue
		}
		sess.tools[name] = json.RawMessage(tool.Raw)
	}
}

// GetTools retrieves cached tool definitions for the given names.
// Returns a JSON array of tools, or nil if none found.
func (c *ToolCache) GetTools(sessionID string, neededNames []string) []byte {
	if c == nil || sessionID == "" || len(neededNames) == 0 {
		return nil
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupLocked(now)

	sess := c.sessions[sessionID]
	if sess == nil {
		return nil
	}
	sess.lastSeen = now
	sess.expiresAt = now.Add(c.ttl)

	var out []json.RawMessage
	seen := make(map[string]struct{}, len(neededNames))
	for _, name := range neededNames {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if raw, ok := sess.tools[name]; ok && len(raw) > 0 {
			out = append(out, raw)
		}
	}
	if len(out) == 0 {
		return nil
	}
	merged, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return merged
}

// Cleanup removes expired sessions. Called automatically on access,
// but can be called manually for background cleanup.
func (c *ToolCache) Cleanup() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
}

func (c *ToolCache) cleanupLocked(now time.Time) {
	for key, sess := range c.sessions {
		if sess == nil || sess.expiresAt.Before(now) {
			delete(c.sessions, key)
		}
	}
}
