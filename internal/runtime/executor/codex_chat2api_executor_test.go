package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestShouldPreferChat2APIForCodex(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "access-only-token",
			"account_id":   "acct-123",
		},
	}
	if !shouldPreferChat2APIForCodex(auth) {
		t.Fatal("shouldPreferChat2APIForCodex() = false, want true for access_token-only auth")
	}

	auth.Metadata["refresh_token"] = "refresh-token"
	if shouldPreferChat2APIForCodex(auth) {
		t.Fatal("shouldPreferChat2APIForCodex() = true, want false when refresh_token exists")
	}
}

func TestCodexExecute_UsesChat2APIForAccessTokenOnlyAuth(t *testing.T) {
	t.Helper()

	var gotAuthHeader string
	var gotAccountHeader string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		gotAuthHeader = r.Header.Get("Authorization")
		gotAccountHeader = r.Header.Get("ChatGPT-Account-ID")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat2api"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{Chat2APIURL: server.URL}})
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "atk",
			"account_id":   "acct-123",
		},
	}
	requestBody := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)

	resp, err := executor.Execute(
		context.Background(),
		auth,
		cliproxyexecutor.Request{Model: "gpt-5.4", Payload: requestBody},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: requestBody},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := resp.Metadata["codex_route"]; got != "chat2api" {
		t.Fatalf("codex_route = %v, want %q", got, "chat2api")
	}
	if gotAuthHeader != "Bearer atk,acct-123" {
		t.Fatalf("Authorization header = %q, want %q", gotAuthHeader, "Bearer atk,acct-123")
	}
	if gotAccountHeader != "acct-123" {
		t.Fatalf("ChatGPT-Account-ID header = %q, want %q", gotAccountHeader, "acct-123")
	}
	if string(gotBody) != string(requestBody) {
		t.Fatalf("upstream body = %s, want %s", gotBody, requestBody)
	}

	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message.Content != "hello from chat2api" {
		t.Fatalf("unexpected translated payload: %s", string(resp.Payload))
	}
}
