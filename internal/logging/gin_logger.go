// Package logging provides Gin middleware for HTTP request logging and panic recovery.
// It integrates Gin web framework with logrus for structured logging of HTTP requests,
// responses, and error handling with panic recovery capabilities.
package logging

import (
    "bytes"
    "fmt"
    "net/http"
    "runtime/debug"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
    log "github.com/sirupsen/logrus"
    "github.com/tidwall/gjson"
)

// GinLogrusLogger returns a Gin middleware handler that logs HTTP requests and responses
// using logrus. It captures request details including method, path, status code, latency,
// client IP, and any error messages, formatting them in a Gin-style log format.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for request logging
func GinLogrusLogger() gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        path := c.Request.URL.Path
        raw := c.Request.URL.RawQuery

		c.Next()

		if raw != "" {
			path = path + "?" + raw
		}

		latency := time.Since(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		} else {
			latency = latency.Truncate(time.Millisecond)
		}

		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method
		errorMessage := c.Errors.ByType(gin.ErrorTypePrivate).String()
		timestamp := time.Now().Format("2006/01/02 - 15:04:05")
		logLine := fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s \"%s\"", timestamp, statusCode, latency, clientIP, method, path)
		if errorMessage != "" {
			logLine = logLine + " | " + errorMessage
		}

        switch {
        case statusCode >= http.StatusInternalServerError:
            log.Error(logLine)
            // Emit an additional structured error entry with more context.
            emitVerbose5xxLog(c, statusCode, method, path, latency)
        case statusCode >= http.StatusBadRequest:
            log.Warn(logLine)
        default:
            log.Info(logLine)
        }
    }
}

// GinLogrusRecovery returns a Gin middleware handler that recovers from panics and logs
// them using logrus. When a panic occurs, it captures the panic value, stack trace,
// and request path, then returns a 500 Internal Server Error response to the client.
//
// Returns:
//   - gin.HandlerFunc: A middleware handler for panic recovery
func GinLogrusRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.WithFields(log.Fields{
			"panic": recovered,
			"stack": string(debug.Stack()),
			"path":  c.Request.URL.Path,
		}).Error("recovered from panic")

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

// emitVerbose5xxLog logs a structured entry with upstream request/response excerpts when available.
func emitVerbose5xxLog(c *gin.Context, status int, method, path string, latency time.Duration) {
    // Attempt to read upstream request/response captured by executors/middleware.
    var apiReq, apiResp []byte
    if v, ok := c.Get("API_REQUEST"); ok {
        if b, okb := v.([]byte); okb {
            apiReq = b
        }
    }
    if v, ok := c.Get("API_RESPONSE"); ok {
        if b, okb := v.([]byte); okb {
            apiResp = b
        }
    }
    // Fallback: use API_RESPONSE_ERROR when API_RESPONSE not available
    if len(apiResp) == 0 {
        if v, ok := c.Get("API_RESPONSE_ERROR"); ok {
            if errs, okList := v.([]*interfaces.ErrorMessage); okList {
                // join errors
                var buf bytes.Buffer
                for i := range errs {
                    if errs[i] == nil || errs[i].Error == nil {
                        continue
                    }
                    if buf.Len() > 0 { buf.WriteString("\n") }
                    buf.WriteString(errs[i].Error.Error())
                }
                apiResp = buf.Bytes()
            }
        }
    }

    // Extract model when possible (from upstream request JSON)
    var model string
    if len(apiReq) > 0 {
        model = gjson.GetBytes(apiReq, "model").String()
        if model == "" {
            // Some translators may nest the request; best-effort alternative.
            model = gjson.GetBytes(apiReq, "body.model").String()
        }
    }

    // For 5xx: include FULL provider error body, but omit request excerpt.
    respExcerpt := safeExcerpt(apiResp, -1)

    fields := log.Fields{
        "status":  status,
        "method":  method,
        "path":    path,
        "latency": latency.String(),
        "model":   model,
        "api_response": respExcerpt,
    }
    log.WithFields(fields).Error("request failed (verbose)")
}

// safeExcerpt returns at most n bytes of b as string, trimming whitespace and indicating truncation.
func safeExcerpt(b []byte, n int) string {
    if len(b) == 0 {
        return ""
    }
    s := bytes.TrimSpace(b)
    if n <= 0 || len(s) <= n {
        return string(s)
    }
    head := s[:n]
    return string(head) + "…(truncated)"
}
