package clienterror

import (
	"errors"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// googleErrorInfoType is the detail type Google APIs use for a
// machine-readable error reason.
const googleErrorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"

// upstreamCodePaths are the places providers put a string error code:
// OpenAI, Codex and Claude-compatible bodies use error.code, Codex error
// events may carry it at the top level, Responses response.failed events
// under response, and Interactions failure events under interaction.
var upstreamCodePaths = []string{"error.code", "code", "response.error.code", "interaction.error.code"}

// UpstreamErrorCode returns the provider's machine-readable error code from an
// upstream error payload, such as Codex's "cyber_policy", or "" when the
// payload carries none.
//
// Only string codes count: Google APIs put the HTTP status in a numeric
// error.code and the specific reason in a google.rpc.ErrorInfo detail, which
// is read instead. error.status ("INVALID_ARGUMENT") is not a code here: it
// is the class matching the HTTP status the client already gets.
func UpstreamErrorCode(payload ojson.Result) string {
	for _, path := range upstreamCodePaths {
		if code := payload.Get(path); code.Type == ojson.String {
			if trimmed := strings.TrimSpace(code.String()); trimmed != "" {
				return strings.Clone(trimmed)
			}
		}
	}
	for _, detail := range payload.Get("error.details").Array() {
		if detail.Get("@type").String() != googleErrorInfoType {
			continue
		}
		if reason := strings.TrimSpace(detail.Get("reason").String()); reason != "" {
			return strings.Clone(reason)
		}
	}
	return ""
}

// UpstreamErrorCodeFromText is UpstreamErrorCode for an error text that may
// be an upstream JSON body; it returns "" for any other text.
func UpstreamErrorCodeFromText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || !ojson.Valid(text) {
		return ""
	}
	return UpstreamErrorCode(ojson.Parse(text))
}

// UpstreamErrorCodeFromError returns the code an executor attached to err
// when the error text no longer contains the upstream body.
func UpstreamErrorCodeFromError(err error) string {
	var carrier interface{ UpstreamErrorCode() string }
	if errors.As(err, &carrier) && carrier != nil {
		return carrier.UpstreamErrorCode()
	}
	return ""
}

// GeminiErrorInfoDetails returns the Gemini error.details array that carries
// code as a google.rpc.ErrorInfo reason, the field Gemini defines for it.
func GeminiErrorInfoDetails(code string) []byte {
	doc := ojson.NewDocString(`[{"@type":"` + googleErrorInfoType + `","reason":""}]`)
	_ = doc.Set("0.reason", code)
	return doc.Bytes()
}
