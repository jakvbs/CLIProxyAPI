package executor

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied. Defaults are checked
// against the original payload when provided.
//
// Supports gjson query syntax in paths for array element selection:
//
//	'tools.#(name=="Read").description': "Modified"
//
// ASSUMPTION: Payload edits only SET fields, never append/remove array elements.
// If this changes, resolution cache must be invalidated after each mutation.
func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.DefaultRaw) == 0 && len(rules.Override) == 0 && len(rules.OverrideRaw) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return payload
	}
	candidates := payloadModelCandidates(cfg, model, protocol)
	out := payload
	source := original
	if len(source) == 0 {
		source = payload
	}
	appliedDefaults := make(map[string]struct{})
	debug := cfg.Debug

	// Apply default rules: first write wins per field across all matching rules.
	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadRuleMatchesModels(rule, protocol, candidates) {
			continue
		}
		if !payloadRuleRequirementsMet(rule, root, out, debug) {
			continue
		}

		// Phase 1: Resolve all query paths
		resolvedPaths := make(map[string][]string)
		var noMatchPaths []string

		for path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}

			concretePaths, hadQuery, err := resolveQueryPath(out, fullPath, debug)
			if err != nil {
				continue // Skip malformed paths
			}
			if hadQuery && len(concretePaths) == 0 {
				noMatchPaths = append(noMatchPaths, path)
				continue
			}
			if !hadQuery {
				concretePaths = []string{fullPath}
			}
			resolvedPaths[path] = concretePaths
		}

		// Log aggregated warnings
		if debug && len(noMatchPaths) > 0 {
			log.Printf("[payload] default rule: %d query paths matched no elements: %v", len(noMatchPaths), noMatchPaths)
		}

		// Phase 2: Apply values to resolved paths (default: skip if exists)
		for path, value := range rule.Params {
			concretePaths, ok := resolvedPaths[path]
			if !ok {
				continue
			}
			for _, cp := range concretePaths {
				// Check against source (original payload) for defaults
				if gjson.GetBytes(source, cp).Exists() {
					continue
				}
				if _, applied := appliedDefaults[cp]; applied {
					continue
				}
				updated, errSet := sjson.SetBytes(out, cp, value)
				if errSet != nil {
					continue
				}
				out = updated
				appliedDefaults[cp] = struct{}{}
			}
		}
	}
	// Apply default raw rules: first write wins per field across all matching rules.
	for i := range rules.DefaultRaw {
		rule := &rules.DefaultRaw[i]
		if !payloadRuleMatchesModels(rule, protocol, candidates) {
			continue
		}
		if !payloadRuleRequirementsMet(rule, root, out, debug) {
			continue
		}

		// Phase 1: Resolve all query paths
		resolvedPaths := make(map[string][]string)
		var noMatchPaths []string

		for path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}

			concretePaths, hadQuery, err := resolveQueryPath(out, fullPath, debug)
			if err != nil {
				continue
			}
			if hadQuery && len(concretePaths) == 0 {
				noMatchPaths = append(noMatchPaths, path)
				continue
			}
			if !hadQuery {
				concretePaths = []string{fullPath}
			}
			resolvedPaths[path] = concretePaths
		}

		if debug && len(noMatchPaths) > 0 {
			log.Printf("[payload] default-raw rule: %d query paths matched no elements: %v", len(noMatchPaths), noMatchPaths)
		}

		// Phase 2: Apply raw values to resolved paths (default: skip if exists)
		for path, value := range rule.Params {
			concretePaths, ok := resolvedPaths[path]
			if !ok {
				continue
			}
			rawValue, ok := payloadRawValue(value)
			if !ok {
				continue
			}
			for _, cp := range concretePaths {
				if gjson.GetBytes(source, cp).Exists() {
					continue
				}
				if _, applied := appliedDefaults[cp]; applied {
					continue
				}
				updated, errSet := sjson.SetRawBytes(out, cp, rawValue)
				if errSet != nil {
					continue
				}
				out = updated
				appliedDefaults[cp] = struct{}{}
			}
		}
	}
	// Apply override rules: last write wins per field across all matching rules.

	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadRuleMatchesModels(rule, protocol, candidates) {
			continue
		}
		if !payloadRuleRequirementsMet(rule, root, out, debug) {
			continue
		}

		// Phase 1: Resolve all query paths
		resolvedPaths := make(map[string][]string)
		var noMatchPaths []string

		for path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}

			concretePaths, hadQuery, err := resolveQueryPath(out, fullPath, debug)
			if err != nil {
				continue // Skip malformed paths
			}
			if hadQuery && len(concretePaths) == 0 {
				noMatchPaths = append(noMatchPaths, path)
				continue
			}
			if !hadQuery {
				concretePaths = []string{fullPath}
			}
			resolvedPaths[path] = concretePaths
		}

		// Log aggregated warnings
		if debug && len(noMatchPaths) > 0 {
			log.Printf("[payload] override rule: %d query paths matched no elements: %v", len(noMatchPaths), noMatchPaths)
		}

		// Phase 2: Apply values to resolved paths (override: always set)
		for path, value := range rule.Params {
			concretePaths, ok := resolvedPaths[path]
			if !ok {
				continue
			}
			for _, cp := range concretePaths {
				updated, errSet := sjson.SetBytes(out, cp, value)
				if errSet != nil {
					continue
				}
				out = updated
			}
		}
	}
	// Apply override raw rules: last write wins per field across all matching rules.
	for i := range rules.OverrideRaw {
		rule := &rules.OverrideRaw[i]
		if !payloadRuleMatchesModels(rule, protocol, candidates) {
			continue
		}
		if !payloadRuleRequirementsMet(rule, root, out, debug) {
			continue
		}

		// Phase 1: Resolve all query paths
		resolvedPaths := make(map[string][]string)
		var noMatchPaths []string

		for path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}

			concretePaths, hadQuery, err := resolveQueryPath(out, fullPath, debug)
			if err != nil {
				continue
			}
			if hadQuery && len(concretePaths) == 0 {
				noMatchPaths = append(noMatchPaths, path)
				continue
			}
			if !hadQuery {
				concretePaths = []string{fullPath}
			}
			resolvedPaths[path] = concretePaths
		}

		if debug && len(noMatchPaths) > 0 {
			log.Printf("[payload] override-raw rule: %d query paths matched no elements: %v", len(noMatchPaths), noMatchPaths)
		}

		// Phase 2: Apply raw values to resolved paths (override: always set)
		for path, value := range rule.Params {
			concretePaths, ok := resolvedPaths[path]
			if !ok {
				continue
			}
			rawValue, ok := payloadRawValue(value)
			if !ok {
				continue
			}
			for _, cp := range concretePaths {
				updated, errSet := sjson.SetRawBytes(out, cp, rawValue)
				if errSet != nil {
					continue
				}
				out = updated
			}
		}
	}
	return out

}

func payloadRuleMatchesModels(rule *config.PayloadRule, protocol string, models []string) bool {
	if rule == nil || len(models) == 0 {
		return false
	}
	for _, model := range models {
		if payloadRuleMatchesModel(rule, model, protocol) {
			return true
		}
	}
	return false
}

func payloadRuleMatchesModel(rule *config.PayloadRule, model, protocol string) bool {
	if rule == nil {
		return false
	}
	if len(rule.Models) == 0 {
		return false
	}
	for _, entry := range rule.Models {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
			continue
		}
		if matchModelPattern(name, model) {
			return true
		}
	}
	return false
}

func payloadRuleRequirementsMet(rule *config.PayloadRule, root string, payload []byte, debug bool) bool {
	if rule == nil {
		return false
	}
	if len(rule.Require) == 0 {
		return true
	}

	var failed []string
	for _, expr := range rule.Require {
		path, wantValue, hasValue := splitRequirement(expr)
		fullPath := buildPayloadPath(root, path)
		if fullPath == "" {
			continue
		}
		result := gjson.GetBytes(payload, fullPath)
		if !hasValue {
			if !result.Exists() {
				failed = append(failed, expr)
			}
			continue
		}
		if !requirementValueMatches(result, wantValue) {
			failed = append(failed, expr)
		}
	}

	if len(failed) > 0 {
		if debug {
			log.Printf("[payload] rule requires unmet conditions: %v", failed)
		}
		return false
	}
	return true
}

func splitRequirement(expr string) (path, value string, hasValue bool) {
	s := strings.TrimSpace(expr)
	if s == "" {
		return "", "", false
	}
	depth := 0
	inQuote := false
	var quote byte
	escapeNext := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escapeNext {
			escapeNext = false
			continue
		}
		if inQuote {
			if ch == '\\' {
				escapeNext = true
				continue
			}
			if ch == quote {
				inQuote = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = true
			quote = ch
			continue
		}
		switch ch {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth == 0 {
				path = strings.TrimSpace(s[:i])
				value = strings.TrimSpace(s[i+1:])
				return path, value, true
			}
		}
	}
	return s, "", false
}

func requirementValueMatches(result gjson.Result, expected string) bool {
	if !result.Exists() {
		return false
	}
	raw := strings.TrimSpace(expected)
	if raw == "" {
		return result.Type == gjson.String && result.String() == ""
	}

	quoted := false
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			quoted = true
			raw = raw[1 : len(raw)-1]
		}
	}

	if !quoted {
		switch strings.ToLower(raw) {
		case "true":
			return result.Type == gjson.True
		case "false":
			return result.Type == gjson.False
		case "null":
			return result.Type == gjson.Null
		}
		if num, err := strconv.ParseFloat(raw, 64); err == nil {
			return result.Type == gjson.Number && result.Float() == num
		}
	}

	return result.Type == gjson.String && result.String() == raw
}

func payloadModelCandidates(cfg *config.Config, model, protocol string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	candidates := []string{model}
	if cfg == nil {
		return candidates
	}
	aliases := payloadModelAliases(cfg, model, protocol)
	if len(aliases) == 0 {
		return candidates
	}
	seen := map[string]struct{}{strings.ToLower(model): struct{}{}}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, alias)
	}
	return candidates
}

func payloadModelAliases(cfg *config.Config, model, protocol string) []string {
	if cfg == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	channel := strings.ToLower(strings.TrimSpace(protocol))
	if channel == "" {
		return nil
	}
	entries := cfg.OAuthModelAlias[channel]
	if len(entries) == 0 {
		return nil
	}
	aliases := make([]string, 0, 2)
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Name), model) {
			continue
		}
		alias := strings.TrimSpace(entry.Alias)
		if alias == "" {
			continue
		}
		aliases = append(aliases, alias)
	}
	return aliases
}

// buildPayloadPath combines an optional root path with a relative parameter path.
// When root is empty, the parameter path is used as-is. When root is non-empty,
// the parameter path is treated as relative to root.
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

func payloadRawValue(value any) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return nil, false
		}
		return raw, true
	}
}

// matchModelPattern performs simple wildcard matching where '*' matches zero or more characters.
// Examples:
//
//	"*-5" matches "gpt-5"
//	"gpt-*" matches "gpt-5" and "gpt-4"
//	"gemini-*-pro" matches "gemini-2.5-pro" and "gemini-3-pro".
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// Iterative glob-style matcher supporting only '*' wildcard.
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// NormalizeThinkingConfig normalizes thinking-related fields in the payload
// based on model capabilities. For models without thinking support, it strips
// reasoning fields. For models with level-based thinking, it validates and
// normalizes the reasoning effort level. For models with numeric budget thinking,
// it strips the effort string fields.
func NormalizeThinkingConfig(payload []byte, model string, allowCompat bool) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}

	// Thinking normalization is handled by the dedicated thinking pipeline.
	// Payload rules are applied earlier/later depending on provider translation,
	// so doing model-capability stripping here would be both incomplete and risky.
	return payload
}

// NOTE: thinking fields are processed by the dedicated thinking pipeline under
// internal/thinking; payload rules here intentionally do not validate/normalize them.

// =============================================================================
// Query-based payload path resolution
// =============================================================================

// isQuerySegment checks if a path segment contains gjson array query syntax.
// A valid query segment must START with #( or #[ and have a matching closing bracket,
// with non-empty content inside.
func isQuerySegment(segment string) bool {
	s := strings.TrimSpace(segment)
	if len(s) < 4 { // minimum: #(x)
		return false
	}
	if s[0] != '#' {
		return false
	}
	if s[1] == '(' && s[len(s)-1] == ')' {
		inner := s[2 : len(s)-1]
		if strings.TrimSpace(inner) == "" {
			return false // Empty query #() is invalid
		}
		return true
	}
	if s[1] == '[' && s[len(s)-1] == ']' {
		inner := s[2 : len(s)-1]
		if strings.TrimSpace(inner) == "" {
			return false // Empty query #[] is invalid
		}
		return true
	}
	return false
}

// parseQuerySegments splits a JSON path into segments, identifying which segments
// are gjson query expressions. It correctly handles:
// - Dots inside quoted strings: #(name=="a.b.c") stays as one segment
// - Escaped quotes: #(name=="say \"hello\"") is parsed correctly
// - Escaped backslashes: #(path=="c:\\dir") is handled
//
// Returns segments, indices of query segments, and an error for malformed paths.
func parseQuerySegments(path string) ([]string, []int, error) {
	var segments []string
	var queryIndices []int
	var current strings.Builder
	depth := 0
	inQuote := false
	escapeNext := false

	for i, ch := range path {
		// Handle escape sequences inside quotes
		// Supports: \" (escaped quote), \\ (escaped backslash)
		if escapeNext {
			current.WriteRune(ch)
			escapeNext = false
			continue
		}
		// Only backslash inside quotes triggers escape
		if ch == '\\' && inQuote {
			current.WriteRune(ch)
			escapeNext = true // Next char is escaped (could be \ or ")
			continue
		}

		// Toggle quote state (only double quotes, as gjson uses them)
		if ch == '"' {
			inQuote = !inQuote
			current.WriteRune(ch)
			continue
		}

		// Inside quotes - don't interpret anything
		if inQuote {
			current.WriteRune(ch)
			continue
		}

		switch ch {
		case '(', '[':
			depth++
			current.WriteRune(ch)
		case ')', ']':
			depth--
			if depth < 0 {
				return nil, nil, fmt.Errorf("unbalanced brackets at position %d", i)
			}
			current.WriteRune(ch)
		case '.':
			if depth == 0 {
				seg := current.String()
				if seg != "" {
					if isQuerySegment(seg) {
						queryIndices = append(queryIndices, len(segments))
					}
					segments = append(segments, seg)
				}
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	// Validation
	if inQuote {
		return nil, nil, fmt.Errorf("unclosed quote in path")
	}
	if depth != 0 {
		return nil, nil, fmt.Errorf("unbalanced brackets in path")
	}

	// Last segment
	if seg := current.String(); seg != "" {
		if isQuerySegment(seg) {
			queryIndices = append(queryIndices, len(segments))
		}
		segments = append(segments, seg)
	}
	return segments, queryIndices, nil
}

// matchesQuery tests if a JSON element satisfies a gjson query filter.
// It wraps the element in a single-element array and uses gjson to evaluate the query.
//
// IMPORTANT: This function should only be called with validated gjson filter expressions
// (detected by isQuerySegment). Calling with arbitrary paths may give false positives.
func matchesQuery(element gjson.Result, query string) bool {
	// Element must have valid Raw representation
	if element.Raw == "" {
		return false
	}

	// Wrap element in single-element array
	testPayload := "[" + element.Raw + "]"

	// Verify wrapped payload is valid JSON
	if !gjson.Valid(testPayload) {
		return false
	}

	// gjson query on array returns matching elements
	// If query matches, result will exist (could be the element or a sub-field)
	// We just need to know IF the element passes the filter
	match := gjson.Get(testPayload, query)

	// For filter queries like #(name=="Read"), gjson returns the matching element
	// We check if ANY match was found - this means element passed the filter
	return match.Exists()
}

// resolveQueryAtPath finds all array indices where elements match the given query.
// Returns sorted indices in ascending order, or nil if the path doesn't exist or isn't an array.
func resolveQueryAtPath(payload []byte, arrayPath, query string, debug bool) []int {
	arr := gjson.GetBytes(payload, arrayPath)

	// Handle missing path
	if !arr.Exists() {
		if debug {
			log.Printf("[payload] query path %q does not exist", arrayPath)
		}
		return nil
	}

	// Handle non-array path
	if !arr.IsArray() {
		if debug {
			log.Printf("[payload] query path %q is not an array (type: %v)", arrayPath, arr.Type)
		}
		return nil
	}

	var indices []int
	arr.ForEach(func(key, value gjson.Result) bool {
		if matchesQuery(value, query) {
			indices = append(indices, int(key.Int()))
		}
		return true
	})

	// Guarantee stable ascending order
	sort.Ints(indices)
	return indices
}

// resolveQueryPath resolves a JSON path containing gjson query expressions to
// a list of concrete paths with array indices.
//
// Example: "tools.#(name==\"Read\").params.#(type==\"string\")"
// might resolve to: ["tools.0.params.1", "tools.2.params.0"]
//
// Returns:
// - concretePaths: list of resolved paths (empty if no matches)
// - hadQuery: true if the path contained any query expressions
// - err: non-nil for malformed paths
func resolveQueryPath(payload []byte, path string, debug bool) ([]string, bool, error) {
	segments, queryIndices, err := parseQuerySegments(path)
	if err != nil {
		if debug {
			log.Printf("[payload] malformed query path %q: %v", path, err)
		}
		return nil, false, err
	}

	// No query segments - return path as-is
	if len(queryIndices) == 0 {
		return []string{path}, false, nil
	}

	// Build set of query segment indices for O(1) lookup
	querySet := make(map[int]bool, len(queryIndices))
	for _, idx := range queryIndices {
		querySet[idx] = true
	}

	// Start with empty path, build combinations iteratively
	currentPaths := []string{""}

	for i, seg := range segments {
		if !querySet[i] {
			// Regular segment: append to all current paths
			for j := range currentPaths {
				if currentPaths[j] == "" {
					currentPaths[j] = seg
				} else {
					currentPaths[j] += "." + seg
				}
			}
			continue
		}

		// Query segment: expand each current path to matching indices
		var nextPaths []string
		for _, prefix := range currentPaths {
			indices := resolveQueryAtPath(payload, prefix, seg, debug)
			for _, idx := range indices {
				var newPath string
				if prefix == "" {
					newPath = strconv.Itoa(idx)
				} else {
					newPath = prefix + "." + strconv.Itoa(idx)
				}
				nextPaths = append(nextPaths, newPath)
			}
		}
		if len(nextPaths) == 0 {
			return nil, true, nil // No matches found
		}
		currentPaths = nextPaths
	}

	return currentPaths, true, nil
}
