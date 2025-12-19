package executor

import (
	"testing"
	"time"
)

func TestToolCache_StoreAndGet(t *testing.T) {
	cache := NewToolCache(30 * time.Minute)

	tools := []byte(`[{"name":"tool1","type":"function"},{"name":"tool2","type":"function"}]`)
	cache.StoreTools("session-123", tools)

	got := cache.GetTools("session-123", []string{"tool1"})
	if got == nil {
		t.Fatal("expected tools, got nil")
	}
	if string(got) != `[{"name":"tool1","type":"function"}]` {
		t.Errorf("unexpected result: %s", got)
	}

	got = cache.GetTools("session-123", []string{"tool1", "tool2"})
	if got == nil {
		t.Fatal("expected tools, got nil")
	}
}

func TestToolCache_TTLExpiry(t *testing.T) {
	cache := NewToolCache(1 * time.Millisecond)

	tools := []byte(`[{"name":"tool1","type":"function"}]`)
	cache.StoreTools("session-123", tools)

	time.Sleep(5 * time.Millisecond)

	got := cache.GetTools("session-123", []string{"tool1"})
	if got != nil {
		t.Errorf("expected nil after TTL expiry, got: %s", got)
	}
}

func TestToolCache_SessionIsolation(t *testing.T) {
	cache := NewToolCache(30 * time.Minute)

	cache.StoreTools("session-A", []byte(`[{"name":"toolA","type":"function"}]`))
	cache.StoreTools("session-B", []byte(`[{"name":"toolB","type":"function"}]`))

	gotA := cache.GetTools("session-A", []string{"toolA"})
	if gotA == nil {
		t.Fatal("expected toolA for session-A")
	}

	gotB := cache.GetTools("session-B", []string{"toolB"})
	if gotB == nil {
		t.Fatal("expected toolB for session-B")
	}

	// Cross-session should not work
	gotCross := cache.GetTools("session-A", []string{"toolB"})
	if gotCross != nil {
		t.Errorf("session-A should not have toolB: %s", gotCross)
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "standard format",
			body:     `{"metadata":{"user_id":"user_abc123_account__session_da00b44d-d080-48ae-9048-7855aa851eaf"}}`,
			expected: "da00b44d-d080-48ae-9048-7855aa851eaf",
		},
		{
			name:     "no session - fallback to full user_id",
			body:     `{"metadata":{"user_id":"user_abc123_account"}}`,
			expected: "user_abc123_account",
		},
		{
			name:     "no metadata",
			body:     `{"model":"claude-3"}`,
			expected: "",
		},
		{
			name:     "empty user_id",
			body:     `{"metadata":{"user_id":""}}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionID([]byte(tt.body))
			if got != tt.expected {
				t.Errorf("extractSessionID() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExtractToolReferences(t *testing.T) {
	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_search_tool_result",
						"content": {
							"tool_references": [
								{"type": "tool_reference", "tool_name": "mcp__chrome__click"},
								{"type": "tool_reference", "tool_name": "mcp__chrome__navigate"}
							]
						}
					},
					{
						"type": "tool_use",
						"name": "Read"
					}
				]
			}
		]
	}`)

	refs := extractToolReferences(body)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}

	expected := map[string]bool{
		"mcp__chrome__click":    true,
		"mcp__chrome__navigate": true,
		"Read":                  true,
	}
	for _, ref := range refs {
		if !expected[ref] {
			t.Errorf("unexpected ref: %s", ref)
		}
	}
}

func TestExtractToolReferences_NameFallback(t *testing.T) {
	// Test fallback to "name" field when "tool_name" is missing
	body := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_search_tool_result",
						"content": {
							"tool_references": [
								{"type": "tool_reference", "name": "FallbackTool"},
								{"type": "tool_reference", "tool_name": "PrimaryTool"}
							]
						}
					}
				]
			}
		]
	}`)

	refs := extractToolReferences(body)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d: %v", len(refs), refs)
	}

	expected := map[string]bool{
		"FallbackTool": true,
		"PrimaryTool":  true,
	}
	for _, ref := range refs {
		if !expected[ref] {
			t.Errorf("unexpected ref: %s", ref)
		}
	}
}

func TestExtractToolNames(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name": "tool1", "type": "function"},
			{"name": "tool2", "type": "function"}
		]
	}`)

	names := extractToolNames(body)
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "tool1" || names[1] != "tool2" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestRehydrateTools_NoTools(t *testing.T) {
	cache := NewToolCache(30 * time.Minute)

	// First request with tools
	bodyWithTools := []byte(`{
		"metadata": {"user_id": "user_x_account__session_abc123"},
		"tools": [{"name": "Read", "type": "function"}],
		"messages": []
	}`)
	rehydrateTools(cache, bodyWithTools)

	// Second request without tools but with tool_reference
	bodyWithoutTools := []byte(`{
		"metadata": {"user_id": "user_x_account__session_abc123"},
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"name": "Read"
					}
				]
			}
		]
	}`)

	result := rehydrateTools(cache, bodyWithoutTools)

	// Should have tools array injected
	if string(result) == string(bodyWithoutTools) {
		t.Error("expected body to be modified with tools")
	}
}

func TestRehydrateTools_WithTools(t *testing.T) {
	cache := NewToolCache(30 * time.Minute)

	// First request with multiple tools
	bodyWithTools := []byte(`{
		"metadata": {"user_id": "user_x_account__session_abc123"},
		"tools": [
			{"name": "Read", "type": "function"},
			{"name": "Write", "type": "function"}
		],
		"messages": []
	}`)
	rehydrateTools(cache, bodyWithTools)

	// Second request with partial tools and reference to missing one
	bodyPartial := []byte(`{
		"metadata": {"user_id": "user_x_account__session_abc123"},
		"tools": [{"name": "Read", "type": "function"}],
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "name": "Read"},
					{"type": "tool_use", "name": "Write"}
				]
			}
		]
	}`)

	result := rehydrateTools(cache, bodyPartial)

	// Should have Write appended
	if string(result) == string(bodyPartial) {
		t.Error("expected body to be modified with Write tool")
	}
}

func TestRehydrateTools_CacheMiss(t *testing.T) {
	cache := NewToolCache(30 * time.Minute)

	// Request with reference to tool that was never cached
	body := []byte(`{
		"metadata": {"user_id": "user_x_account__session_new"},
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "name": "UnknownTool"}
				]
			}
		]
	}`)

	result := rehydrateTools(cache, body)

	// Should be unchanged (cache miss is a no-op)
	if string(result) != string(body) {
		t.Errorf("expected no change on cache miss, got: %s", result)
	}
}
