package test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	geminihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var updateErrorCodeBaseline = flag.Bool("update-error-code-baseline", false, "rewrite testdata/error_code_matrix_baseline.json from the current build")

const errorCodeMatrixBaselinePath = "testdata/error_code_matrix_baseline.json"

// errorCodeUpstream is one way a provider refuses a request: an HTTP error
// status, or an error event inside an HTTP 200 stream.
type errorCodeUpstream struct {
	name string
	// inStream marks a refusal that only a streaming upstream request can
	// receive; cells whose upstream request does not stream are skipped.
	inStream bool
	// respond writes the refusal; withCode decides whether the provider's
	// error code is in it.
	respond func(w http.ResponseWriter, withCode bool)
}

type errorCodeProvider struct {
	name  string
	model string
	// routeProvider is the provider key the model registry routes by; empty
	// means the auth's provider.
	routeProvider string
	executor      func(cfg *config.Config) cliproxyauth.ProviderExecutor
	auth          func(baseURL string) *cliproxyauth.Auth
	cfg           func() *config.Config
	upstreams     []errorCodeUpstream
}

type errorCodeClientFormat struct {
	name   string
	method string
	path   func(model string, stream bool) string
	body   func(model string, stream bool) string
}

// errorCodeCell is what one client saw: the HTTP status and the body.
type errorCodeCell struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

const matrixCode = "matrix_check"

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func writeSSE(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, frame := range frames {
		_, _ = fmt.Fprint(w, frame)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func openAIErrorObject(withCode bool) string {
	if withCode {
		return `{"type":"invalid_request_error","code":"` + matrixCode + `","message":"refused by matrix check"}`
	}
	return `{"type":"invalid_request_error","message":"refused by matrix check"}`
}

func googleErrorBody(withCode bool) string {
	if withCode {
		return `{"error":{"code":400,"message":"refused by matrix check","status":"FAILED_PRECONDITION","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"` + matrixCode + `","domain":"googleapis.com"}]}}`
	}
	return `{"error":{"code":400,"message":"refused by matrix check"}}`
}

func errorCodeProviders() []errorCodeProvider {
	codexAuth := func(name string) func(string) *cliproxyauth.Auth {
		return func(baseURL string) *cliproxyauth.Auth {
			return &cliproxyauth.Auth{ID: name, Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"base_url": baseURL, "api_key": "matrix"},
				Metadata:   map[string]any{"disable_cooling": true}}
		}
	}
	return []errorCodeProvider{
		{
			name:     "codex",
			model:    "gpt-5.4",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor { return runtimeexecutor.NewCodexExecutor(cfg) },
			auth:     codexAuth("matrix-codex"),
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, `{"error":`+openAIErrorObject(withCode)+`}`)
				}},
				{name: "error_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w, `data: {"type":"error","error":`+openAIErrorObject(withCode)+`,"sequence_number":1}`+"\n\n")
				}},
				{name: "response_failed_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w,
						`data: {"type":"response.created","response":{"id":"resp_matrix","status":"in_progress"},"sequence_number":0}`+"\n\n",
						`data: {"type":"response.failed","response":{"id":"resp_matrix","status":"failed","error":`+openAIErrorObject(withCode)+`},"sequence_number":1}`+"\n\n")
				}},
			},
		},
		{
			name:     "claude",
			model:    "claude-sonnet-4-5-20250929",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor { return runtimeexecutor.NewClaudeExecutor(cfg) },
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{ID: "matrix-claude", Provider: "claude", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"base_url": baseURL, "api_key": "matrix"},
					Metadata:   map[string]any{"disable_cooling": true}}
			},
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, `{"type":"error","error":`+openAIErrorObject(withCode)+`}`)
				}},
				{name: "error_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w, "event: error\ndata: {\"type\":\"error\",\"error\":"+openAIErrorObject(withCode)+"}\n\n")
				}},
			},
		},
		{
			name:     "gemini",
			model:    "gemini-2.5-pro",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor { return runtimeexecutor.NewGeminiExecutor(cfg) },
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{ID: "matrix-gemini", Provider: "gemini", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"base_url": baseURL, "api_key": "matrix"},
					Metadata:   map[string]any{"disable_cooling": true}}
			},
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, googleErrorBody(withCode))
				}},
			},
		},
		{
			name:  "antigravity",
			model: "gemini-3-pro-high",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return runtimeexecutor.NewAntigravityExecutor(cfg)
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{ID: "matrix-antigravity", Provider: "antigravity", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"base_url": baseURL},
					Metadata: map[string]any{"disable_cooling": true, "access_token": "matrix", "project_id": "matrix",
						"expired": "2999-01-01T00:00:00Z"}}
			},
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, googleErrorBody(withCode))
				}},
				{name: "error_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w, "data: "+googleErrorBody(withCode)+"\n\n")
				}},
			},
		},
		{
			name:          "openai-compatibility",
			model:         "matrix-compat-model",
			routeProvider: "openai-compatible-compat",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return runtimeexecutor.NewOpenAICompatExecutor("openai-compatible-compat", cfg)
			},
			cfg: func() *config.Config {
				return &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
					Name:   "compat",
					Models: []config.OpenAICompatibilityModel{{Name: "matrix-compat-model", Alias: "matrix-compat-model"}},
				}}}
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{ID: "matrix-compat", Provider: "openai-compatibility", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"base_url": baseURL + "/v1", "api_key": "matrix", "compat_name": "compat", "provider_key": "compat"},
					Metadata:   map[string]any{"disable_cooling": true}}
			},
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, `{"error":`+openAIErrorObject(withCode)+`}`)
				}},
				{name: "error_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w, `data: {"error":`+openAIErrorObject(withCode)+`}`+"\n\n")
				}},
			},
		},
		{
			name:  "gemini-interactions",
			model: "gemini-2.5-flash",
			executor: func(cfg *config.Config) cliproxyauth.ProviderExecutor {
				return runtimeexecutor.NewGeminiInteractionsExecutor(cfg)
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{ID: "matrix-interactions", Provider: "gemini-interactions", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"base_url": baseURL, "api_key": "matrix"},
					Metadata:   map[string]any{"disable_cooling": true}}
			},
			upstreams: []errorCodeUpstream{
				{name: "http_error", respond: func(w http.ResponseWriter, withCode bool) {
					writeJSON(w, http.StatusBadRequest, googleErrorBody(withCode))
				}},
				{name: "failed_event", inStream: true, respond: func(w http.ResponseWriter, withCode bool) {
					writeSSE(w,
						"event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction_matrix\",\"status\":\"in_progress\"}}\n\n",
						"event: interaction.failed\ndata: {\"event_type\":\"interaction.failed\",\"interaction\":{\"id\":\"interaction_matrix\",\"status\":\"failed\",\"error\":"+openAIErrorObject(withCode)+"}}\n\n")
				}},
			},
		},
	}
}

func errorCodeClientFormats() []errorCodeClientFormat {
	return []errorCodeClientFormat{
		{name: "claude", method: http.MethodPost,
			path: func(string, bool) string { return "/v1/messages" },
			body: func(model string, stream bool) string {
				return fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream)
			}},
		{name: "openai_chat", method: http.MethodPost,
			path: func(string, bool) string { return "/v1/chat/completions" },
			body: func(model string, stream bool) string {
				return fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream)
			}},
		{name: "openai_responses", method: http.MethodPost,
			path: func(string, bool) string { return "/v1/responses" },
			body: func(model string, stream bool) string {
				return fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello"}`, model, stream)
			}},
		{name: "gemini", method: http.MethodPost,
			path: func(model string, stream bool) string {
				if stream {
					return "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
				}
				return "/v1beta/models/" + model + ":generateContent"
			},
			body: func(string, bool) string { return `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}` }},
		{name: "interactions", method: http.MethodPost,
			path: func(string, bool) string { return "/v1beta/interactions" },
			body: func(model string, stream bool) string {
				return fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hello"}`, model, stream)
			}},
	}
}

func errorCodeRouter(base *handlers.BaseAPIHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	claude := claudehandlers.NewClaudeCodeAPIHandler(base)
	openai := openaihandlers.NewOpenAIAPIHandler(base)
	responses := openaihandlers.NewOpenAIResponsesAPIHandler(base)
	gemini := geminihandlers.NewGeminiAPIHandler(base)
	router.POST("/v1/messages", claude.ClaudeMessages)
	router.POST("/v1/chat/completions", openai.ChatCompletions)
	router.POST("/v1/responses", responses.Responses)
	router.POST("/v1beta/models/*action", gemini.GeminiHandler)
	router.POST("/v1beta/interactions", gemini.Interactions)
	return router
}

// runErrorCodeCell drives one request through the real handler, executor and
// translators against a fake upstream that refuses it.
// It reports false when the refusal shape cannot reach this cell, because the
// upstream request does not stream.
func runErrorCodeCell(t *testing.T, p errorCodeProvider, up errorCodeUpstream, format errorCodeClientFormat, stream, withCode bool) (errorCodeCell, bool) {
	t.Helper()
	applicable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up.inStream && !upstreamRequestStreams(r) {
			applicable = false
			writeJSON(w, http.StatusInternalServerError, `{"error":{"message":"refusal shape not applicable"}}`)
			return
		}
		up.respond(w, withCode)
	}))
	defer server.Close()

	cfg := &config.Config{}
	if p.cfg != nil {
		cfg = p.cfg()
	}
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(p.executor(cfg))
	auth := p.auth(server.URL)
	routeProvider := p.routeProvider
	if routeProvider == "" {
		routeProvider = auth.Provider
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, routeProvider, []*registry.ModelInfo{{ID: p.model}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	router := errorCodeRouter(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(format.method, format.path(p.model, stream), strings.NewReader(format.body(p.model, stream)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return errorCodeCell{Status: recorder.Code, Body: normalizeErrorCodeBody(recorder.Body.String())}, applicable
}

// upstreamRequestStreams reports whether the executor asked the upstream for
// a stream: a streaming Gemini method, an SSE query or "stream": true.
func upstreamRequestStreams(r *http.Request) bool {
	if strings.Contains(r.URL.Path, "streamGenerateContent") || r.URL.Query().Get("alt") == "sse" {
		return true
	}
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &payload) == nil && payload.Stream
}

var errorCodeVolatile = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`"(created|created_at|createTime|completed_at)":\s*[0-9]+`), `"$1":0`},
	{regexp.MustCompile(`(chatcmpl|resp|msg|fc|rs|call|toolu)[-_][A-Za-z0-9_-]{6,}`), `${1}_X`},
	{regexp.MustCompile(`"(id|response_id|item_id|responseId|interaction_id)":"[^"]*"`), `"$1":"X"`},
	{regexp.MustCompile(`"(created|updated)":"[^"]*"`), `"$1":"X"`},
}

// normalizeErrorCodeBody replaces generated ids and timestamps so two runs of
// the same cell compare byte for byte.
func normalizeErrorCodeBody(body string) string {
	for _, v := range errorCodeVolatile {
		body = v.pattern.ReplaceAllString(body, v.replacement)
	}
	return body
}

func errorCodeCellName(p errorCodeProvider, up errorCodeUpstream, format errorCodeClientFormat, stream, withCode bool) string {
	mode := "nonstream"
	if stream {
		mode = "stream"
	}
	code := "nocode"
	if withCode {
		code = "code"
	}
	return strings.Join([]string{p.name, up.name, format.name, mode, code}, "/")
}

// errorCodeSwallowedCells are cells whose upstream refusal never reaches the
// client as an error at 681ce41b: the client gets an empty success instead.
// Turning them into errors would change their status, which F041 rules out,
// so the matrix only pins that they stay as they are.
var errorCodeSwallowedCells = map[string]bool{
	"antigravity/error_event/claude/nonstream/code":           true,
	"antigravity/error_event/gemini/nonstream/code":           true,
	"antigravity/error_event/interactions/nonstream/code":     true,
	"antigravity/error_event/openai_chat/nonstream/code":      true,
	"antigravity/error_event/openai_responses/nonstream/code": true,
}

// TestUpstreamErrorCodeMatrix drives every provider, upstream refusal shape,
// client format and stream mode, with and without a provider error code,
// through the real handlers, executors and translators. Cells without a code
// must match the baseline captured at upstream 681ce41b byte for byte. Cells
// with a code must carry it in every error object the client receives, and
// with the code removed they must equal the baseline: status, type and
// message never change. Rerun with -update-error-code-baseline only on a
// build whose error output is the reference.
func TestUpstreamErrorCodeMatrix(t *testing.T) {
	type cellCase struct {
		format   string
		withCode bool
		cell     errorCodeCell
	}
	got := map[string]cellCase{}
	for _, p := range errorCodeProviders() {
		for _, up := range p.upstreams {
			for _, format := range errorCodeClientFormats() {
				for _, stream := range []bool{false, true} {
					for _, withCode := range []bool{false, true} {
						name := errorCodeCellName(p, up, format, stream, withCode)
						if cell, ok := runErrorCodeCell(t, p, up, format, stream, withCode); ok {
							got[name] = cellCase{format: format.name, withCode: withCode, cell: cell}
						}
					}
				}
			}
		}
	}

	if *updateErrorCodeBaseline {
		cells := make(map[string]errorCodeCell, len(got))
		for name, c := range got {
			cells[name] = c.cell
		}
		writeErrorCodeBaseline(t, cells)
		return
	}

	baseline := readErrorCodeBaseline(t)
	for name := range baseline {
		if _, ok := got[name]; !ok {
			t.Errorf("%s: in the baseline but not driven any more", name)
		}
	}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	carried := 0
	for _, name := range names {
		c := got[name]
		base, ok := baseline[name]
		if !ok {
			t.Errorf("%s: not in the baseline", name)
			continue
		}
		if !c.withCode {
			if c.cell != base {
				t.Errorf("%s: differs from 681ce41b\n got: %d %s\nwant: %d %s", name, c.cell.Status, c.cell.Body, base.Status, base.Body)
			}
			continue
		}
		if c.cell.Status != base.Status {
			t.Errorf("%s: status %d, 681ce41b gave %d", name, c.cell.Status, base.Status)
		}
		errorObjects := errorObjectsIn(c.cell.Body)
		if errorCodeSwallowedCells[name] {
			if len(errorObjects) != 0 {
				t.Errorf("%s: listed as swallowed but now returns an error; drop it from errorCodeSwallowedCells", name)
			}
		} else if len(errorObjects) == 0 {
			t.Errorf("%s: no error object in the client response:\n%s", name, c.cell.Body)
		}
		for _, errorObject := range errorObjects {
			if !errorObjectCarriesCode(errorObject, c.format) {
				t.Errorf("%s: error object %v does not carry %q in the %s field", name, errorObject, matrixCode, c.format)
			}
		}
		if gotStripped, baseStripped := stripMatrixCode(c.cell.Body), stripMatrixCode(base.Body); gotStripped != baseStripped {
			t.Errorf("%s: with the code removed, differs from 681ce41b\n got: %s\nwant: %s", name, gotStripped, baseStripped)
		}
		carried++
	}
	t.Logf("cells: %d, with a code: %d", len(got), carried)
}

func readErrorCodeBaseline(t *testing.T) map[string]errorCodeCell {
	t.Helper()
	data, errRead := os.ReadFile(errorCodeMatrixBaselinePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var baseline map[string]errorCodeCell
	if errUnmarshal := json.Unmarshal(data, &baseline); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return baseline
}

// errorCodeDocuments returns the JSON documents of a response body: the body
// itself, or the data payload of each SSE frame.
func errorCodeDocuments(body string) []map[string]any {
	var docs []map[string]any
	var whole map[string]any
	if json.Unmarshal([]byte(body), &whole) == nil {
		return append(docs, whole)
	}
	for _, line := range strings.Split(body, "\n") {
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var doc map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &doc) == nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// errorObjectsIn returns every error object a client receives: error,
// response.error and interaction.error of each document.
func errorObjectsIn(body string) []map[string]any {
	var objects []map[string]any
	for _, doc := range errorCodeDocuments(body) {
		for _, holder := range []map[string]any{doc, asObject(doc["response"]), asObject(doc["interaction"])} {
			if errorObject := asObject(holder["error"]); errorObject != nil {
				objects = append(objects, errorObject)
			}
		}
	}
	return objects
}

func asObject(v any) map[string]any {
	object, _ := v.(map[string]any)
	return object
}

// errorObjectCarriesCode reports whether the code is in error.code, or in the
// reason of a google.rpc.ErrorInfo detail, the field Gemini defines because
// its error.code is the HTTP status. Claude clients must get error.code.
func errorObjectCarriesCode(errorObject map[string]any, format string) bool {
	if code, _ := errorObject["code"].(string); code == matrixCode {
		return true
	}
	if format == "claude" {
		return false
	}
	details, _ := errorObject["details"].([]any)
	for _, detail := range details {
		if reason, _ := asObject(detail)["reason"].(string); reason == matrixCode {
			return true
		}
	}
	return false
}

// stripMatrixCode removes the matrix code wherever a client format may carry
// it and re-encodes each document with sorted keys, so a body with the code
// can be compared with one from a build that dropped it.
func stripMatrixCode(body string) string {
	var whole map[string]any
	if json.Unmarshal([]byte(body), &whole) == nil {
		return encodeStripped(whole)
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var doc map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &doc) == nil {
			lines[i] = "data: " + encodeStripped(doc)
		}
	}
	return strings.Join(lines, "\n")
}

func encodeStripped(doc map[string]any) string {
	for _, holder := range []map[string]any{doc, asObject(doc["response"]), asObject(doc["interaction"])} {
		errorObject := asObject(holder["error"])
		if errorObject == nil {
			continue
		}
		if code, _ := errorObject["code"].(string); code == matrixCode {
			delete(errorObject, "code")
		}
		if details, ok := errorObject["details"].([]any); ok {
			kept := details[:0]
			for _, detail := range details {
				if reason, _ := asObject(detail)["reason"].(string); reason != matrixCode {
					kept = append(kept, detail)
				}
			}
			if len(kept) == 0 {
				delete(errorObject, "details")
			} else {
				errorObject["details"] = kept
			}
		}
	}
	encoded, _ := json.Marshal(doc)
	return string(encoded)
}

func writeErrorCodeBaseline(t *testing.T, cells map[string]errorCodeCell) {
	t.Helper()
	names := make([]string, 0, len(cells))
	for name := range cells {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("{\n")
	for i, name := range names {
		entry, errMarshal := json.Marshal(cells[name])
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		key, _ := json.Marshal(name)
		fmt.Fprintf(&b, "  %s: %s", key, entry)
		if i < len(names)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if errMkdir := os.MkdirAll(filepath.Dir(errorCodeMatrixBaselinePath), 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errWrite := os.WriteFile(errorCodeMatrixBaselinePath, []byte(b.String()), 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
}
