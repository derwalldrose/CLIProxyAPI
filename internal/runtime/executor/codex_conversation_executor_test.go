package executor

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexCreds_UsesMetadataAccessTokenAndBaseURL(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "access-from-metadata",
			"base_url":     "https://chatgpt.com/backend-api",
		},
	}

	token, baseURL := codexCreds(auth)
	if token != "access-from-metadata" {
		t.Fatalf("codexCreds token = %q, want %q", token, "access-from-metadata")
	}
	if baseURL != "https://chatgpt.com/backend-api" {
		t.Fatalf("codexCreds baseURL = %q, want %q", baseURL, "https://chatgpt.com/backend-api")
	}
	if got := resolveConversationOrigin(auth); got != "https://chatgpt.com" {
		t.Fatalf("resolveConversationOrigin() = %q, want %q", got, "https://chatgpt.com")
	}
}

func TestCodexAccountID_UsesAliasesAndIDToken(t *testing.T) {
	t.Run("workspace_id alias", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"workspace_id": "workspace-123",
			},
		}
		if got := codexAccountID(auth); got != "workspace-123" {
			t.Fatalf("codexAccountID() = %q, want %q", got, "workspace-123")
		}
	})

	t.Run("id_token fallback", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"id_token": testUnsignedJWT(t, map[string]any{
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct-from-id-token",
					},
				}),
			},
		}
		if got := codexAccountID(auth); got != "acct-from-id-token" {
			t.Fatalf("codexAccountID() = %q, want %q", got, "acct-from-id-token")
		}
	})
}

func TestMapConversationModel(t *testing.T) {
	cases := map[string]string{
		"gpt-5.4":        "gpt-5-4-t-mini",
		"gpt-5-4":        "gpt-5-4-t-mini",
		"gpt-5-4-t-mini": "gpt-5-4-t-mini",
		"gpt-5.2":        "gpt-5-2",
		"gpt-4.1-mini":   "gpt-4-1-mini",
		"custom-model":   "custom-model",
		" team/gpt-5.4 ": "team/gpt-5-4",
	}

	for input, want := range cases {
		if got := mapConversationModel(input); got != want {
			t.Fatalf("mapConversationModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDetectConversationSystemHints(t *testing.T) {
	raw := []byte(`{
		"system_hints":["search","search"],
		"tools":[
			{"type":"web_search_preview"},
			{"type":"function"}
		]
	}`)

	got := detectConversationSystemHints(raw)
	if len(got) != 1 || got[0] != "search" {
		t.Fatalf("detectConversationSystemHints() = %#v, want []string{\"search\"}", got)
	}
}

func TestConversationOnlyAuth_DoesNotFallbackToResponses(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "access-only-token",
			"account_id":   "acct-123",
		},
	}

	if !shouldPreferConversationForCodex(auth) {
		t.Fatal("shouldPreferConversationForCodex() = false, want true for access_token-only auth")
	}
	if shouldFallbackFromConversationForCodex(auth, statusErr{code: 401, msg: `{"detail":"Unauthorized"}`}) {
		t.Fatal("shouldFallbackFromConversationForCodex() = true, want false for access_token-only auth")
	}
}

func TestConversationPreferredAuth_WithRefreshToken_CanFallbackToResponses(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token":  "access-token",
			"account_id":    "acct-123",
			"refresh_token": "refresh-token",
		},
	}

	if shouldPreferConversationForCodex(auth) {
		t.Fatal("shouldPreferConversationForCodex() = true, want false when refresh_token exists")
	}
	if !shouldFallbackFromConversationForCodex(auth, statusErr{code: 401, msg: `{"detail":"Unauthorized"}`}) {
		t.Fatal("shouldFallbackFromConversationForCodex() = false, want true when refresh_token exists")
	}
}

func testUnsignedJWT(t *testing.T, payload map[string]any) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]any{
		"alg": "none",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	return base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON) + "."
}
