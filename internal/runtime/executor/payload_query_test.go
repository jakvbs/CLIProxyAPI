package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// applyPayloadConfig is a test helper that wraps applyPayloadConfigWithRoot
// with empty protocol, root, and original parameters for simpler test cases.
func applyPayloadConfig(cfg *config.Config, model string, payload []byte) []byte {
	return applyPayloadConfigWithRoot(cfg, model, "", "", payload, nil)
}

// =============================================================================
// isQuerySegment tests
// =============================================================================

func TestIsQuerySegment(t *testing.T) {
	tests := []struct {
		name     string
		segment  string
		expected bool
	}{
		// Valid query segments
		{"valid parentheses query", `#(name=="Read")`, true},
		{"valid bracket query", `#[0]`, true},
		{"valid simple query", `#(x)`, true},
		{"valid with spaces inside", `#( name == "Read" )`, true},
		{"valid complex query", `#(name%"mcp_*")`, true},
		{"valid comparison query", `#(value>10)`, true},

		// Invalid - empty queries
		{"empty parentheses", `#()`, false},
		{"empty brackets", `#[]`, false},
		{"whitespace only inside", `#(   )`, false},

		// Invalid - not starting with #
		{"missing hash", `(name=="Read")`, false},
		{"hash in middle", `foo#(bar)`, false},

		// Invalid - incomplete
		{"incomplete - no closing", `#(name`, false},
		{"incomplete - no opening", `#name)`, false},
		{"just hash", `#`, false},
		{"hash and paren only", `#(`, false},

		// Invalid - wrong format
		{"regular path segment", `tools`, false},
		{"numeric segment", `0`, false},
		{"dot path", `tools.name`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isQuerySegment(tt.segment)
			assert.Equal(t, tt.expected, result, "segment: %q", tt.segment)
		})
	}
}

// =============================================================================
// parseQuerySegments tests
// =============================================================================

func TestParseQuerySegments_Basic(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantSegments []string
		wantQueryIdx []int
		wantErr      bool
	}{
		{
			name:         "simple path no query",
			path:         "tools.0.name",
			wantSegments: []string{"tools", "0", "name"},
			wantQueryIdx: nil,
			wantErr:      false,
		},
		{
			name:         "single query segment",
			path:         `tools.#(name=="Read").description`,
			wantSegments: []string{"tools", `#(name=="Read")`, "description"},
			wantQueryIdx: []int{1},
			wantErr:      false,
		},
		{
			name:         "nested queries",
			path:         `tools.#(name=="Read").params.#(type=="string")`,
			wantSegments: []string{"tools", `#(name=="Read")`, "params", `#(type=="string")`},
			wantQueryIdx: []int{1, 3},
			wantErr:      false,
		},
		{
			name:         "query at start",
			path:         `#(name=="Read").description`,
			wantSegments: []string{`#(name=="Read")`, "description"},
			wantQueryIdx: []int{0},
			wantErr:      false,
		},
		{
			name:         "query at end",
			path:         `tools.#(name=="Read")`,
			wantSegments: []string{"tools", `#(name=="Read")`},
			wantQueryIdx: []int{1},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments, queryIdx, err := parseQuerySegments(tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantSegments, segments)
			assert.Equal(t, tt.wantQueryIdx, queryIdx)
		})
	}
}

func TestParseQuerySegments_QuotedDots(t *testing.T) {
	// Dots inside quoted strings should NOT split the segment
	path := `tools.#(name=="a.b.c").description`
	segments, queryIdx, err := parseQuerySegments(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"tools", `#(name=="a.b.c")`, "description"}, segments)
	assert.Equal(t, []int{1}, queryIdx)
}

func TestParseQuerySegments_EscapedQuotes(t *testing.T) {
	// Escaped quotes inside strings should be handled
	path := `tools.#(name=="say \"hello\"").description`
	segments, queryIdx, err := parseQuerySegments(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"tools", `#(name=="say \"hello\"")`, "description"}, segments)
	assert.Equal(t, []int{1}, queryIdx)
}

func TestParseQuerySegments_EscapedBackslash(t *testing.T) {
	// Escaped backslashes should be handled
	path := `tools.#(path=="c:\\dir").name`
	segments, queryIdx, err := parseQuerySegments(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"tools", `#(path=="c:\\dir")`, "name"}, segments)
	assert.Equal(t, []int{1}, queryIdx)
}

func TestParseQuerySegments_UnbalancedBrackets(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"extra closing paren", `tools.#(name=="Read")).description`},
		{"extra opening paren", `tools.#((name=="Read").description`},
		// Note: mismatched brackets like #(name=="Read"] are NOT detected as errors
		// because we only track depth, not bracket type. This is acceptable since
		// gjson would fail to parse such malformed queries anyway.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseQuerySegments(tt.path)
			assert.Error(t, err, "path: %q", tt.path)
		})
	}
}

func TestParseQuerySegments_UnclosedQuote(t *testing.T) {
	path := `tools.#(name=="Read).description`
	_, _, err := parseQuerySegments(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unclosed quote")
}

// =============================================================================
// matchesQuery tests
// =============================================================================

func TestMatchesQuery_Basic(t *testing.T) {
	tests := []struct {
		name     string
		element  string
		query    string
		expected bool
	}{
		{
			name:     "exact match",
			element:  `{"name":"Read","type":"function"}`,
			query:    `#(name=="Read")`,
			expected: true,
		},
		{
			name:     "no match",
			element:  `{"name":"Write","type":"function"}`,
			query:    `#(name=="Read")`,
			expected: false,
		},
		{
			name:     "wildcard case match",
			element:  `{"name":"READ"}`,
			query:    `#(name%"READ")`, // gjson % operator for pattern matching
			expected: true,
		},
		{
			name:     "wildcard match",
			element:  `{"name":"mcp_filesystem"}`,
			query:    `#(name%"mcp_*")`,
			expected: true,
		},
		{
			name:     "not equal match",
			element:  `{"name":"Write"}`,
			query:    `#(name!="Read")`,
			expected: true,
		},
		{
			name:     "comparison match",
			element:  `{"value":15}`,
			query:    `#(value>10)`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			element := gjson.Parse(tt.element)
			result := matchesQuery(element, tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMatchesQuery_InvalidJSON(t *testing.T) {
	// Empty Raw should return false
	element := gjson.Result{Raw: ""}
	result := matchesQuery(element, `#(name=="Read")`)
	assert.False(t, result)
}

func TestMatchesQuery_Primitives(t *testing.T) {
	// Query on primitive values (strings, numbers)
	tests := []struct {
		name     string
		element  string
		query    string
		expected bool
	}{
		{
			name:     "string primitive match",
			element:  `"read"`,
			query:    `#(=="read")`,
			expected: true,
		},
		{
			name:     "string primitive no match",
			element:  `"write"`,
			query:    `#(=="read")`,
			expected: false,
		},
		{
			name:     "number primitive match",
			element:  `42`,
			query:    `#(==42)`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			element := gjson.Parse(tt.element)
			result := matchesQuery(element, tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMatchesQuery_SubObjectQuery(t *testing.T) {
	// Query that returns a sub-field should still work (Exists() check)
	element := gjson.Parse(`{"name":"Read","config":{"timeout":30}}`)
	// This query would return the config object, not the element itself
	result := matchesQuery(element, `#(config.timeout==30)`)
	assert.True(t, result)
}

// =============================================================================
// resolveQueryAtPath tests
// =============================================================================

func TestResolveQueryAtPath_Basic(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name": "Read", "type": "function"},
			{"name": "Write", "type": "function"},
			{"name": "Read", "type": "tool"}
		]
	}`)

	indices := resolveQueryAtPath(payload, "tools", `#(name=="Read")`, false)
	assert.Equal(t, []int{0, 2}, indices)
}

func TestResolveQueryAtPath_NoMatch(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name": "Write"},
			{"name": "Execute"}
		]
	}`)

	indices := resolveQueryAtPath(payload, "tools", `#(name=="Read")`, false)
	assert.Nil(t, indices)
}

func TestResolveQueryAtPath_NonArray(t *testing.T) {
	payload := []byte(`{"tools": {"name": "Read"}}`)
	indices := resolveQueryAtPath(payload, "tools", `#(name=="Read")`, false)
	assert.Nil(t, indices)
}

func TestResolveQueryAtPath_MissingPath(t *testing.T) {
	payload := []byte(`{"other": []}`)
	indices := resolveQueryAtPath(payload, "tools", `#(name=="Read")`, false)
	assert.Nil(t, indices)
}

func TestResolveQueryAtPath_SortedIndices(t *testing.T) {
	// Ensure indices are returned in ascending order
	payload := []byte(`{
		"items": [
			{"match": false},
			{"match": true},
			{"match": false},
			{"match": true},
			{"match": true}
		]
	}`)

	indices := resolveQueryAtPath(payload, "items", `#(match==true)`, false)
	assert.Equal(t, []int{1, 3, 4}, indices)

	// Verify sorted
	for i := 1; i < len(indices); i++ {
		assert.True(t, indices[i] > indices[i-1], "indices should be sorted")
	}
}

func TestResolveQueryAtPath_PrimitiveArray(t *testing.T) {
	payload := []byte(`{"permissions": ["read", "write", "execute"]}`)
	indices := resolveQueryAtPath(payload, "permissions", `#(=="read")`, false)
	assert.Equal(t, []int{0}, indices)
}

// =============================================================================
// resolveQueryPath tests
// =============================================================================

func TestResolveQueryPath_NoQuery(t *testing.T) {
	payload := []byte(`{"tools": [{"name": "Read"}]}`)
	paths, hadQuery, err := resolveQueryPath(payload, "tools.0.name", false)
	require.NoError(t, err)
	assert.False(t, hadQuery)
	assert.Equal(t, []string{"tools.0.name"}, paths)
}

func TestResolveQueryPath_SingleQuery(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{"name": "Read"},
			{"name": "Write"},
			{"name": "Read"}
		]
	}`)

	paths, hadQuery, err := resolveQueryPath(payload, `tools.#(name=="Read").description`, false)
	require.NoError(t, err)
	assert.True(t, hadQuery)
	assert.Equal(t, []string{"tools.0.description", "tools.2.description"}, paths)
}

func TestResolveQueryPath_Nested(t *testing.T) {
	payload := []byte(`{
		"tools": [
			{
				"name": "Read",
				"params": [
					{"type": "string", "name": "path"},
					{"type": "int", "name": "offset"}
				]
			},
			{
				"name": "Write",
				"params": [
					{"type": "string", "name": "content"}
				]
			}
		]
	}`)

	paths, hadQuery, err := resolveQueryPath(payload, `tools.#(name=="Read").params.#(type=="string").required`, false)
	require.NoError(t, err)
	assert.True(t, hadQuery)
	assert.Equal(t, []string{"tools.0.params.0.required"}, paths)
}

func TestResolveQueryPath_NoMatch(t *testing.T) {
	payload := []byte(`{"tools": [{"name": "Write"}]}`)
	paths, hadQuery, err := resolveQueryPath(payload, `tools.#(name=="Read").description`, false)
	require.NoError(t, err)
	assert.True(t, hadQuery)
	assert.Nil(t, paths)
}

func TestResolveQueryPath_MalformedError(t *testing.T) {
	payload := []byte(`{"tools": []}`)
	_, _, err := resolveQueryPath(payload, `tools.#(name=="Read).description`, false)
	assert.Error(t, err)
}

// =============================================================================
// Integration tests with applyPayloadConfig
// =============================================================================

func TestApplyPayloadConfig_QueryOverride(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					`tools.#(name=="Read").description`: "Modified description",
				},
			}},
		},
	}

	payload := []byte(`{
		"tools": [
			{"name": "Read", "description": "Original"},
			{"name": "Write", "description": "Keep this"}
		]
	}`)

	result := applyPayloadConfig(cfg, "test-model", payload)

	assert.Equal(t, "Modified description", gjson.GetBytes(result, "tools.0.description").String())
	assert.Equal(t, "Keep this", gjson.GetBytes(result, "tools.1.description").String())
}

func TestApplyPayloadConfig_QueryDefault(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					`tools.#(name=="Read").timeout`: 30,
				},
			}},
		},
	}

	payload := []byte(`{
		"tools": [
			{"name": "Read"},
			{"name": "Read", "timeout": 60},
			{"name": "Write"}
		]
	}`)

	result := applyPayloadConfig(cfg, "test-model", payload)

	// First Read tool should get default timeout
	assert.Equal(t, int64(30), gjson.GetBytes(result, "tools.0.timeout").Int())
	// Second Read tool already has timeout, should keep it
	assert.Equal(t, int64(60), gjson.GetBytes(result, "tools.1.timeout").Int())
	// Write tool should not be affected
	assert.False(t, gjson.GetBytes(result, "tools.2.timeout").Exists())
}

func TestApplyPayloadConfig_MultiMatch(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					`tools.#(type=="function").cache_control`: map[string]any{"type": "ephemeral"},
				},
			}},
		},
	}

	payload := []byte(`{
		"tools": [
			{"name": "A", "type": "function"},
			{"name": "B", "type": "tool"},
			{"name": "C", "type": "function"}
		]
	}`)

	result := applyPayloadConfig(cfg, "test-model", payload)

	assert.Equal(t, "ephemeral", gjson.GetBytes(result, "tools.0.cache_control.type").String())
	assert.False(t, gjson.GetBytes(result, "tools.1.cache_control").Exists())
	assert.Equal(t, "ephemeral", gjson.GetBytes(result, "tools.2.cache_control.type").String())
}

func TestApplyPayloadConfig_BackwardsCompat(t *testing.T) {
	// Regular paths without queries should still work
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					"model":             "override-model",
					"tools.0.name":      "FirstTool",
					"nested.deep.value": 123,
				},
			}},
		},
	}

	payload := []byte(`{
		"model": "original",
		"tools": [{"name": "Original"}],
		"nested": {"deep": {}}
	}`)

	result := applyPayloadConfig(cfg, "test-model", payload)

	assert.Equal(t, "override-model", gjson.GetBytes(result, "model").String())
	assert.Equal(t, "FirstTool", gjson.GetBytes(result, "tools.0.name").String())
	assert.Equal(t, int64(123), gjson.GetBytes(result, "nested.deep.value").Int())
}

func TestApplyPayloadConfig_NoMatchSilentSkip(t *testing.T) {
	cfg := &config.Config{
		Debug: false, // No warnings in non-debug mode
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					`tools.#(name=="NonExistent").value`: "should not crash",
				},
			}},
		},
	}

	payload := []byte(`{"tools": [{"name": "Read"}]}`)

	// Should not panic or error
	result := applyPayloadConfig(cfg, "test-model", payload)

	// Payload should be unchanged
	assert.Equal(t, "Read", gjson.GetBytes(result, "tools.0.name").String())
}

func TestApplyPayloadConfig_WildcardQuery(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Params: map[string]any{
					`tools.#(name%"mcp_*").restricted`: true,
				},
			}},
		},
	}

	payload := []byte(`{
		"tools": [
			{"name": "mcp_filesystem"},
			{"name": "Read"},
			{"name": "mcp_browser"}
		]
	}`)

	result := applyPayloadConfig(cfg, "test-model", payload)

	assert.True(t, gjson.GetBytes(result, "tools.0.restricted").Bool())
	assert.False(t, gjson.GetBytes(result, "tools.1.restricted").Exists())
	assert.True(t, gjson.GetBytes(result, "tools.2.restricted").Bool())
}
