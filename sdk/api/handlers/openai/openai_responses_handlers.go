// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
    "bytes"
    "context"
    "fmt"
    "net/http"
    "time"

	"github.com/gin-gonic/gin"
    . "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
    "github.com/tidwall/gjson"
    "github.com/tidwall/sjson"
)

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
    rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

    // Preprocess request: apply model-suffix inference and inject defaults.
    rawJSON, _ = h.preprocessResponsesRequest(rawJSON)

    // Check if the client requested a streaming response.
    streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
    h.handleNonStreamingResponse(c, rawJSON)
	}

}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	defer func() {
		cliCancel()
	}()

	resp, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}
	_, _ = c.Writer.Write(resp)
	return

	// no legacy fallback

}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

    // New core execution path
    modelName := gjson.GetBytes(rawJSON, "model").String()
    cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
    dataChan, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
    h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
    return
}

// preprocessResponsesRequest normalizes model suffixes into reasoning.effort and injects
// default verbosity and reasoning.summary when absent. It returns the potentially modified
// request body and the (possibly normalized) model name.
func (h *OpenAIResponsesAPIHandler) preprocessResponsesRequest(body []byte) ([]byte, string) {
    modelName := gjson.GetBytes(body, "model").String()
    if modelName == "" {
        return body, modelName
    }

    cfg := h.Cfg // *config.SDKConfig

    // 1) Inject defaults when not supplied by client
    if cfg != nil {
        // text.verbosity
        if !gjson.GetBytes(body, "text.verbosity").Exists() && cfg.Responses.Defaults.Verbosity != "" {
            if v := cfg.Responses.Defaults.Verbosity; v == "low" || v == "medium" || v == "high" {
                body, _ = sjson.SetBytes(body, "text.verbosity", v)
            }
        }
        // reasoning.summary
        if !gjson.GetBytes(body, "reasoning.summary").Exists() && cfg.Responses.Defaults.ReasoningSummary != "" {
            if rs := cfg.Responses.Defaults.ReasoningSummary; rs == "auto" || rs == "detailed" {
                body, _ = sjson.SetBytes(body, "reasoning.summary", rs)
            }
        }
    }

    // 2) Infer reasoning.effort from model suffix when enabled and effort not set by client
    if cfg == nil || cfg.Responses.InferEffortFromModelSuffix {
        // Only act if client did not set reasoning.effort explicitly
        if !gjson.GetBytes(body, "reasoning.effort").Exists() {
            base, effort, ok := inferEffortFromModel(modelName)
            if ok {
                // apply effort and normalize model to base
                body, _ = sjson.SetBytes(body, "reasoning.effort", effort)
                body, _ = sjson.SetBytes(body, "model", base)
                modelName = base
            }
        }
    }

    return body, modelName
}

// inferEffortFromModel parses supported model families with a suffix indicating effort
// and returns base model, effort, and whether a mapping occurred.
// Supported family: gpt-5 only (by requirement).
// Supported suffixes: minimal, low, medium, high
func inferEffortFromModel(model string) (string, string, bool) {
    if model == "" {
        return "", "", false
    }
    // Fast path: find last dash
    idx := -1
    for i := len(model) - 1; i >= 0; i-- {
        if model[i] == '-' {
            idx = i
            break
        }
    }
    if idx <= 0 || idx >= len(model)-1 {
        return "", "", false
    }
    base := model[:idx]
    suffix := model[idx+1:]
    switch suffix {
    case "minimal", "low", "medium", "high":
        if base == "gpt-5" {
            return base, suffix, true
        }
    }
    return "", "", false
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case chunk, ok := <-data:
			if !ok {
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cancel(nil)
				return
			}

			if bytes.HasPrefix(chunk, []byte("event:")) {
				_, _ = c.Writer.Write([]byte("\n"))
			}
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n"))

			flusher.Flush()
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				h.WriteErrorResponse(c, errMsg)
				flusher.Flush()
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
