package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	openairesponsesrequest "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const defaultChat2APIURL = "http://127.0.0.1:5005"

type chat2APIRequestTarget struct {
	url         string
	bearerToken string
	accountID   string
}

func (e *CodexExecutor) executeChat2API(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	chatReqBody, err := prepareChat2APIRequestBody(req, opts)
	if err != nil {
		return resp, err
	}

	target, err := e.prepareChat2APIRequest(auth, "/v1/chat/completions")
	if err != nil {
		return resp, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+target.bearerToken)
	if target.accountID != "" {
		headers.Set("ChatGPT-Account-ID", target.accountID)
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       target.url,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      chatReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	client := newChat2APIHTTPClient(120 * time.Second)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target.url, bytes.NewReader(chatReqBody))
	if err != nil {
		return resp, err
	}
	httpReq.Header = headers.Clone()

	httpResp, err := client.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex chat2api executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		err = newCodexStatusErr(httpResp.StatusCode, b)
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, chatReqBody, data, &param)
	return cliproxyexecutor.Response{
		Payload:  []byte(out),
		Headers:  httpResp.Header.Clone(),
		Metadata: map[string]any{"codex_route": "chat2api"},
	}, nil
}

func (e *CodexExecutor) executeChat2APIStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := req.Model
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	chatReqBody, err := prepareChat2APIRequestBody(req, opts)
	if err != nil {
		return nil, err
	}

	target, err := e.prepareChat2APIRequest(auth, "/v1/chat/completions")
	if err != nil {
		return nil, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Authorization", "Bearer "+target.bearerToken)
	if target.accountID != "" {
		headers.Set("ChatGPT-Account-ID", target.accountID)
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       target.url,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      chatReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	client := newChat2APIHTTPClient(0)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target.url, bytes.NewReader(chatReqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header = headers.Clone()

	httpResp, err := client.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex chat2api executor: close response body error: %v", errClose)
		}
		appendAPIResponseChunk(ctx, e.cfg, b)
		return nil, newCodexStatusErr(httpResp.StatusCode, b)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex chat2api executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			appendAPIResponseChunk(ctx, e.cfg, line)
			chunks := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, chatReqBody, line, &param)
			for _, chunk := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk)}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
			return
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

func prepareChat2APIRequestBody(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	if len(originalPayload) == 0 {
		return nil, statusErr{code: http.StatusBadRequest, msg: "chat2api route requires request payload"}
	}
	switch opts.SourceFormat.String() {
	case "openai-response":
		return openairesponsesrequest.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(req.Model, originalPayload, opts.Stream), nil
	default:
		return bytes.Clone(originalPayload), nil
	}
}

func (e *CodexExecutor) prepareChat2APIRequest(auth *cliproxyauth.Auth, path string) (chat2APIRequestTarget, error) {
	accessToken := resolveConversationAccessToken(auth)
	if accessToken == "" {
		return chat2APIRequestTarget{}, statusErr{code: http.StatusBadRequest, msg: "chat2api route requires auth.metadata.access_token"}
	}
	accountID := resolveConversationAccountID(auth)
	if accountID == "" {
		return chat2APIRequestTarget{}, statusErr{code: http.StatusBadRequest, msg: "chat2api route requires auth.metadata.account_id"}
	}
	baseURL := defaultChat2APIURL
	if e.cfg != nil && strings.TrimSpace(e.cfg.Chat2APIURL) != "" {
		baseURL = strings.TrimSpace(e.cfg.Chat2APIURL)
	}
	return chat2APIRequestTarget{
		url:         strings.TrimSuffix(baseURL, "/") + path,
		bearerToken: accessToken + "," + accountID,
		accountID:   accountID,
	}, nil
}

func newChat2APIHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

func shouldPreferChat2APIForCodex(auth *cliproxyauth.Auth) bool {
	return canUseConversationForCodex(auth) && !codexHasRefreshToken(auth)
}
