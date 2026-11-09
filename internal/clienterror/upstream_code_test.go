package clienterror

import (
	"errors"
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestUpstreamErrorCode(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"openai error.code", `{"error":{"type":"invalid_request","code":"cyber_policy","message":"m"}}`, "cyber_policy"},
		{"codex top-level code", `{"type":"error","code":"rate_limit_exceeded","message":"m"}`, "rate_limit_exceeded"},
		{"responses response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`, "server_error"},
		{"interactions interaction.failed", `{"event_type":"interaction.failed","interaction":{"error":{"code":"quota"}}}`, "quota"},
		{"google ErrorInfo reason", `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.Help"},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`, "API_KEY_INVALID"},
		{"google numeric code and status only", `{"error":{"code":429,"message":"m","status":"RESOURCE_EXHAUSTED"}}`, ""},
		{"claude error without code", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, ""},
		{"blank code", `{"error":{"code":"  ","message":"m"}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpstreamErrorCode(ojson.Parse(tt.payload)); got != tt.want {
				t.Fatalf("UpstreamErrorCode(%s) = %q, want %q", tt.payload, got, tt.want)
			}
			if got := UpstreamErrorCodeFromText(tt.payload); got != tt.want {
				t.Fatalf("UpstreamErrorCodeFromText(%s) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
	if got := UpstreamErrorCodeFromText("upstream returned error event: cyber_policy"); got != "" {
		t.Fatalf("plain text gave code %q", got)
	}
}

type codeCarrier struct{ code string }

func (e codeCarrier) Error() string             { return "carrier" }
func (e codeCarrier) UpstreamErrorCode() string { return e.code }

func TestUpstreamErrorCodeFromError(t *testing.T) {
	wrapped := fmt.Errorf("wrapped: %w", codeCarrier{code: "cyber_policy"})
	if got := UpstreamErrorCodeFromError(wrapped); got != "cyber_policy" {
		t.Fatalf("UpstreamErrorCodeFromError = %q, want cyber_policy", got)
	}
	if got := UpstreamErrorCodeFromError(errors.New("plain")); got != "" {
		t.Fatalf("plain error gave code %q", got)
	}
}

func TestGeminiErrorInfoDetails(t *testing.T) {
	want := `[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"cyber_policy"}]`
	if got := string(GeminiErrorInfoDetails("cyber_policy")); got != want {
		t.Fatalf("GeminiErrorInfoDetails = %s, want %s", got, want)
	}
}
