package management

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type authFileTestModelRequest struct {
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	Route     string `json:"route"`
}

func (h *Handler) TestAuthFileModel(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req authFileTestModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prompt is required"})
		return
	}

	auth := h.resolveAuthByNameOrIndex(req.Name, req.AuthIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	requestedRoute, alt, ok := normalizeAuthTestRoute(req.Route)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "route must be one of: auto, responses, conversation"})
		return
	}

	rawJSON, err := json.Marshal(map[string]any{
		"model":  model,
		"input":  prompt,
		"stream": false,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode request"})
		return
	}

	selectedAuthID := auth.ID
	meta := map[string]any{
		cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
		cliproxyexecutor.RequestedModelMetadataKey: model,
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(id string) {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				selectedAuthID = trimmed
			}
		},
	}

	resp, err := h.authManager.Execute(
		c.Request.Context(),
		[]string{auth.Provider},
		cliproxyexecutor.Request{
			Model:   model,
			Payload: rawJSON,
		},
		cliproxyexecutor.Options{
			Stream:          false,
			Alt:             alt,
			OriginalRequest: rawJSON,
			SourceFormat:    sdktranslator.FromString("openai-response"),
			Metadata:        meta,
		},
	)

	effectiveRoute := requestedRoute
	if route, ok := resp.Metadata["codex_route"].(string); ok && strings.TrimSpace(route) != "" {
		effectiveRoute = strings.TrimSpace(route)
	}

	result := gin.H{
		"ok":                 err == nil,
		"auth_id":            auth.ID,
		"auth_index":         auth.Index,
		"auth_name":          auth.FileName,
		"provider":           auth.Provider,
		"model":              model,
		"prompt":             prompt,
		"requested_route":    requestedRoute,
		"effective_route":    effectiveRoute,
		"selected_auth_id":   selectedAuthID,
		"selected_auth_ok":   selectedAuthID == auth.ID,
		"response_payload":   string(resp.Payload),
		"response_headers":   resp.Headers,
		"output_text":        extractOpenAIResponseText(resp.Payload),
		"response_truncated": len(resp.Payload) > 16384,
	}

	if result["response_truncated"].(bool) {
		result["response_payload"] = string(resp.Payload[:16384])
	}

	if err != nil {
		statusCode := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok {
			if code := se.StatusCode(); code > 0 {
				statusCode = code
			}
		}
		result["status_code"] = statusCode
		result["error"] = err.Error()
	}

	c.JSON(http.StatusOK, result)
}

func normalizeAuthTestRoute(route string) (requested string, alt string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(route)) {
	case "", "auto":
		return "auto", "", true
	case "responses", "response":
		return "responses", "responses", true
	case "conversation":
		return "conversation", "conversation", true
	default:
		return "", "", false
	}
}

func (h *Handler) resolveAuthByNameOrIndex(name, authIndex string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name != "" {
		if auth, ok := h.authManager.GetByID(name); ok {
			return auth
		}
		if auth, ok := h.authManager.FindByFileName(name); ok {
			return auth
		}
	}
	return h.authByIndex(authIndex)
}

func extractOpenAIResponseText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if text := strings.TrimSpace(gjson.GetBytes(payload, "output_text").String()); text != "" {
		return text
	}
	var pieces []string
	gjson.GetBytes(payload, "output").ForEach(func(_, item gjson.Result) bool {
		item.Get("content").ForEach(func(_, content gjson.Result) bool {
			for _, path := range []string{"text", "output_text", "content", "value"} {
				if value := strings.TrimSpace(content.Get(path).String()); value != "" {
					pieces = append(pieces, value)
					break
				}
			}
			return true
		})
		return true
	})
	if len(pieces) > 0 {
		return strings.Join(pieces, "\n")
	}
	if text := strings.TrimSpace(gjson.GetBytes(payload, "choices.0.message.content").String()); text != "" {
		return text
	}
	if text := strings.TrimSpace(gjson.GetBytes(payload, "choices.0.message.content.0.text").String()); text != "" {
		return text
	}
	return ""
}
