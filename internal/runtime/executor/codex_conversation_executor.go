package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openairesponsesrequest "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/crypto/sha3"
)

const (
	chatGPTConversationDefaultOrigin = "https://chatgpt.com"
	chatGPTConversationDefaultUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	chatGPTConversationPowAttempts   = 500000
)

var chatGPTConversationPerfEpoch = time.Now()

type chatGPTConversationStreamState struct {
	id         string
	created    int64
	model      string
	messageID  string
	lastText   string
	finished   bool
	lastStatus string
}

func (e *CodexExecutor) executeConversation(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := req.Model
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken := resolveConversationAccessToken(auth)
	if accessToken == "" {
		return resp, statusErr{code: http.StatusBadRequest, msg: "conversation route requires auth.metadata.access_token"}
	}
	accountID := resolveConversationAccountID(auth)
	if accountID == "" {
		return resp, statusErr{code: http.StatusBadRequest, msg: "conversation route requires auth.metadata.account_id"}
	}

	chatReqBody, conversationReqBody, origin, err := e.prepareConversationRequest(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}

	var lastErr error
	for _, profile := range conversationTLSProfiles() {
		resp, retryNext, attemptErr := e.executeConversationWithTLSProfile(ctx, auth, req, opts, chatReqBody, conversationReqBody, origin, accessToken, accountID, profile, reporter)
		if attemptErr == nil {
			return resp, nil
		}
		lastErr = attemptErr
		if !retryNext {
			return resp, attemptErr
		}
		logWithRequestID(ctx).Warnf("codex conversation profile %s failed, retrying next profile: %v", profile.name, attemptErr)
	}
	if lastErr == nil {
		lastErr = statusErr{code: http.StatusBadGateway, msg: "conversation route failed without error"}
	}
	return resp, lastErr
}

func (e *CodexExecutor) executeConversationStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := req.Model
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken := resolveConversationAccessToken(auth)
	if accessToken == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "conversation route requires auth.metadata.access_token"}
	}
	accountID := resolveConversationAccountID(auth)
	if accountID == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "conversation route requires auth.metadata.account_id"}
	}

	chatReqBody, conversationReqBody, origin, err := e.prepareConversationRequest(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, profile := range conversationTLSProfiles() {
		result, retryNext, attemptErr := e.executeConversationStreamWithTLSProfile(ctx, auth, req, opts, chatReqBody, conversationReqBody, origin, accessToken, accountID, profile, reporter)
		if attemptErr == nil {
			return result, nil
		}
		lastErr = attemptErr
		if !retryNext {
			return nil, attemptErr
		}
		logWithRequestID(ctx).Warnf("codex conversation stream profile %s failed, retrying next profile: %v", profile.name, attemptErr)
	}
	if lastErr == nil {
		lastErr = statusErr{code: http.StatusBadGateway, msg: "conversation stream failed without error"}
	}
	return nil, lastErr
}

func (e *CodexExecutor) executeConversationWithTLSProfile(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, chatReqBody, conversationReqBody []byte, origin, accessToken, accountID string, profile conversationTLSProfile, reporter *usageReporter) (resp cliproxyexecutor.Response, retryNext bool, err error) {
	conversationClient, err := newConversationTLSClient(e.cfg, auth, 90*time.Second, profile)
	if err != nil {
		return resp, false, err
	}

	headers, err := e.prepareConversationHeaders(ctx, auth, accessToken, accountID, origin, conversationClient, profile)
	if err != nil {
		return resp, shouldRetryConversationTLSProfile(err), err
	}

	urlStr := strings.TrimSuffix(origin, "/") + "/backend-api/conversation"
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       urlStr,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      conversationReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := conversationClient.Do(ctx, http.MethodPost, urlStr, headers.Clone(), conversationReqBody)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, shouldRetryConversationTLSProfile(err), err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex conversation executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if trimmed := strings.TrimSpace(string(b)); trimmed != "" {
			logWithRequestID(ctx).Debugf("codex conversation upstream raw response (%s): %s", profile.name, trimmed)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, shouldRetryConversationTLSProfile(err), err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, false, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	openAIResp, err := buildOpenAINonStreamFromConversationSSE(data, req.Model)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, false, err
	}
	reporter.publish(ctx, parseOpenAIUsage(openAIResp))
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, chatReqBody, openAIResp, &param)
	return cliproxyexecutor.Response{
		Payload:  []byte(out),
		Headers:  httpResp.Header.Clone(),
		Metadata: map[string]any{"codex_route": "conversation", "conversation_tls_profile": profile.name},
	}, false, nil
}

func (e *CodexExecutor) executeConversationStreamWithTLSProfile(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, chatReqBody, conversationReqBody []byte, origin, accessToken, accountID string, profile conversationTLSProfile, reporter *usageReporter) (_ *cliproxyexecutor.StreamResult, retryNext bool, err error) {
	conversationClient, err := newConversationTLSClient(e.cfg, auth, 0, profile)
	if err != nil {
		return nil, false, err
	}

	headers, err := e.prepareConversationHeaders(ctx, auth, accessToken, accountID, origin, conversationClient, profile)
	if err != nil {
		return nil, shouldRetryConversationTLSProfile(err), err
	}
	headers.Set("Accept", "text/event-stream")

	urlStr := strings.TrimSuffix(origin, "/") + "/backend-api/conversation"
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       urlStr,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      conversationReqBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := conversationClient.Do(ctx, http.MethodPost, urlStr, headers.Clone(), conversationReqBody)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, shouldRetryConversationTLSProfile(err), err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex conversation executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, false, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			logWithRequestID(ctx).Debugf("codex conversation upstream raw response (%s): %s", profile.name, trimmed)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, shouldRetryConversationTLSProfile(err), err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex conversation executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		state := &chatGPTConversationStreamState{
			id:      fmt.Sprintf("chatcmpl_%d", time.Now().UnixNano()),
			created: time.Now().Unix(),
			model:   req.Model,
		}
		var param any
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			appendAPIResponseChunk(ctx, e.cfg, line)
			chunks, convErr := convertConversationSSELineToOpenAIChunks(line, state)
			if convErr != nil {
				recordAPIResponseError(ctx, e.cfg, convErr)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: convErr}
				return
			}
			for _, openAIChunk := range chunks {
				translated := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, chatReqBody, openAIChunk, &param)
				for _, lineOut := range translated {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(lineOut)}
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
			return
		}
		if !state.finished && state.lastText != "" {
			finalChunk := buildOpenAIChatCompletionStreamFinishChunk(state)
			translated := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, req.Model, opts.OriginalRequest, chatReqBody, finalChunk, &param)
			for _, lineOut := range translated {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(lineOut)}
			}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, false, nil
}

func (e *CodexExecutor) prepareConversationRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, []byte, string, error) {
	origin := resolveConversationOrigin(auth)
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	if len(originalPayload) == 0 {
		return nil, nil, origin, statusErr{code: http.StatusBadRequest, msg: "conversation route requires request payload"}
	}

	var chatReqBody []byte
	switch opts.SourceFormat.String() {
	case "openai-response":
		chatReqBody = openairesponsesrequest.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(req.Model, originalPayload, opts.Stream)
	default:
		chatReqBody = bytes.Clone(originalPayload)
	}

	conversationReqBody, err := buildConversationRequestFromOpenAIChat(chatReqBody, originalPayload, req.Model)
	if err != nil {
		return nil, nil, origin, err
	}
	return chatReqBody, conversationReqBody, origin, nil
}

func (e *CodexExecutor) prepareConversationHeaders(ctx context.Context, auth *cliproxyauth.Auth, accessToken, accountID, origin string, conversationClient conversationRequestClient, profile conversationTLSProfile) (http.Header, error) {
	headers := make(http.Header)
	ginHeaders := conversationIncomingHeaders(ctx)

	userAgent := strings.TrimSpace(ginHeaders.Get("User-Agent"))
	if userAgent == "" {
		userAgent = profile.defaultUserAgent
		if userAgent == "" {
			userAgent = chatGPTConversationDefaultUA
		}
	}
	acceptLanguage := strings.TrimSpace(ginHeaders.Get("Accept-Language"))
	if acceptLanguage == "" {
		acceptLanguage = "en-US,en;q=0.9"
	}
	deviceID := resolveConversationDeviceID(auth, ginHeaders)
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Accept-Language", acceptLanguage)
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", origin)
	headers.Set("Referer", strings.TrimSuffix(origin, "/")+"/")
	headers.Set("User-Agent", userAgent)
	headers.Set("Oai-Device-Id", deviceID)
	headers.Set("Oai-Language", "en-US")
	headers.Set("Chatgpt-Account-Id", accountID)
	headers.Set("Sec-Fetch-Dest", "empty")
	headers.Set("Sec-Fetch-Mode", "cors")
	headers.Set("Sec-Fetch-Site", "same-origin")
	headers.Set("Connection", "keep-alive")

	dpl, scripts, err := e.fetchConversationBuildInfo(ctx, origin, headers, conversationClient)
	if err != nil {
		return nil, err
	}
	powConfig := buildConversationPowConfig(userAgent, dpl, scripts)
	requirementsToken, _, _ := generateConversationRequirementsToken(powConfig)
	challengeURL := strings.TrimSuffix(origin, "/") + "/backend-api/sentinel/chat-requirements"
	challengeBody := []byte(fmt.Sprintf(`{"p":%q}`, requirementsToken))
	challengeHeaders := headers.Clone()
	challengeHeaders.Set("Accept", "application/json")

	challengeResp, err := conversationClient.Do(ctx, http.MethodPost, challengeURL, challengeHeaders, challengeBody)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = challengeResp.Body.Close()
	}()
	if challengeResp.StatusCode < 200 || challengeResp.StatusCode >= 300 {
		body, _ := io.ReadAll(challengeResp.Body)
		return nil, statusErr{code: challengeResp.StatusCode, msg: string(body)}
	}
	challengeData, err := io.ReadAll(challengeResp.Body)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(gjson.GetBytes(challengeData, "token").String())
	if token == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "chat-requirements did not return token"}
	}
	headers.Set("Openai-Sentinel-Chat-Requirements-Token", token)
	if gjson.GetBytes(challengeData, "proofofwork.required").Bool() {
		seed := gjson.GetBytes(challengeData, "proofofwork.seed").String()
		difficulty := gjson.GetBytes(challengeData, "proofofwork.difficulty").String()
		proofToken, solved, _ := generateConversationProofToken(seed, difficulty, powConfig)
		if !solved {
			return nil, statusErr{code: http.StatusBadGateway, msg: "failed to solve chat-requirements proof-of-work"}
		}
		headers.Set("Openai-Sentinel-Proof-Token", proofToken)
	}
	return headers, nil
}

func shouldRetryConversationTLSProfile(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(message, "tls: handshake failure") ||
		strings.Contains(message, "remote error: tls") {
		return true
	}
	status, ok := err.(statusErr)
	if !ok {
		return false
	}
	if status.code != http.StatusForbidden && status.code != http.StatusTooManyRequests && status.code != http.StatusServiceUnavailable {
		return false
	}
	if message == "" {
		return false
	}
	return strings.Contains(message, "enable javascript and cookies to continue") ||
		strings.Contains(message, "__cf_chl") ||
		strings.Contains(message, "cloudflare") ||
		strings.Contains(message, "managed challenge")
}

func (e *CodexExecutor) fetchConversationBuildInfo(ctx context.Context, origin string, headers http.Header, conversationClient conversationRequestClient) (string, []string, error) {
	requestHeaders := headers.Clone()
	requestHeaders.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	requestHeaders.Del("Content-Type")
	resp, err := conversationClient.Do(ctx, http.MethodGet, strings.TrimSuffix(origin, "/")+"/", requestHeaders, nil)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, statusErr{code: resp.StatusCode, msg: string(body)}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}
	html := string(body)
	dpl := ""
	if match := regexp.MustCompile(`<html[^>]*data-build="([^"]*)"`).FindStringSubmatch(html); len(match) > 1 {
		dpl = strings.TrimSpace(match[1])
	}
	scriptMatches := regexp.MustCompile(`<script[^>]+src="([^"]+)"`).FindAllStringSubmatch(html, -1)
	scripts := make([]string, 0, len(scriptMatches))
	for _, match := range scriptMatches {
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			scripts = append(scripts, strings.TrimSpace(match[1]))
		}
	}
	if len(scripts) == 0 {
		scripts = []string{strings.TrimSuffix(origin, "/") + "/backend-api/sentinel/sdk.js"}
	}
	return dpl, scripts, nil
}

func resolveConversationOrigin(auth *cliproxyauth.Auth) string {
	baseURL := codexBaseURL(auth)
	if baseURL == "" {
		return chatGPTConversationDefaultOrigin
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return chatGPTConversationDefaultOrigin
	}
	return parsed.Scheme + "://" + parsed.Host
}

func resolveConversationAccessToken(auth *cliproxyauth.Auth) string {
	return codexAccessToken(auth)
}

func resolveConversationAccountID(auth *cliproxyauth.Auth) string {
	return codexAccountID(auth)
}

func resolveConversationDeviceID(auth *cliproxyauth.Auth, incoming http.Header) string {
	if v := strings.TrimSpace(incoming.Get("Oai-Device-Id")); v != "" {
		return v
	}
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["device_id"]); v != "" {
			return v
		}
	}
	if auth != nil && auth.Metadata != nil {
		if v, ok := auth.Metadata["device_id"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return uuid.NewString()
}

func conversationIncomingHeaders(ctx context.Context) http.Header {
	if ctx == nil {
		return http.Header{}
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header.Clone()
	}
	return http.Header{}
}

func buildConversationPowConfig(userAgent, dpl string, scripts []string) []any {
	perfNow := float64(time.Since(chatGPTConversationPerfEpoch).Microseconds()) / 1000.0
	timeOrigin := float64(time.Now().UnixNano())/1e6 - perfNow
	scriptURL := chatGPTConversationDefaultOrigin + "/backend-api/sentinel/sdk.js"
	if len(scripts) > 0 && strings.TrimSpace(scripts[0]) != "" {
		scriptURL = strings.TrimSpace(scripts[0])
	}
	return []any{
		3000,
		formatConversationPowTime(),
		float64(4294705152),
		float64(0),
		userAgent,
		scriptURL,
		dpl,
		"en-US",
		"en-US,en",
		float64(0),
		"vendor−Google Inc.",
		"location",
		"window",
		perfNow,
		uuid.NewString(),
		"",
		float64(8),
		timeOrigin,
	}
}

func formatConversationPowTime() string {
	now := time.Now().In(time.FixedZone("EST", -5*3600))
	return now.Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
}

func generateConversationRequirementsToken(config []any) (string, bool, int) {
	answer, solved, attempts := generateConversationPowAnswer(strconv.FormatFloat(rand.Float64(), 'g', -1, 64), "0fffff", config, chatGPTConversationPowAttempts)
	return "gAAAAAC" + answer, solved, attempts
}

func generateConversationProofToken(seed, difficulty string, config []any) (string, bool, int) {
	answer, solved, attempts := generateConversationPowAnswer(seed, difficulty, config, chatGPTConversationPowAttempts)
	return "gAAAAAB" + answer, solved, attempts
}

func generateConversationPowAnswer(seed, difficulty string, config []any, maxAttempts int) (string, bool, int) {
	diffLen := len(difficulty) / 2
	target, err := hexStringToBytes(difficulty)
	if err != nil {
		fallback := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%q", seed)))
		return fallback, false, -1
	}
	configBytes, _ := json.Marshal(config)
	_ = configBytes
	seedBytes := []byte(seed)

	part1, _ := json.Marshal(config[:3])
	part2, _ := json.Marshal(config[4:9])
	part3, _ := json.Marshal(config[10:])
	static1 := append([]byte(strings.TrimSuffix(string(part1), "]")+","), []byte{}...)
	static2 := append([]byte(","+strings.TrimPrefix(strings.TrimSuffix(string(part2), "]"), "[")+","), []byte{}...)
	static3 := append([]byte(","+strings.TrimPrefix(string(part3), "[")), []byte{}...)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		payload := make([]byte, 0, len(static1)+len(static2)+len(static3)+32)
		payload = append(payload, static1...)
		payload = append(payload, strconv.Itoa(attempt)...)
		payload = append(payload, static2...)
		payload = append(payload, strconv.Itoa(attempt>>1)...)
		payload = append(payload, static3...)
		encoded := base64.StdEncoding.EncodeToString(payload)
		hash := sha3.Sum512(append(seedBytes, []byte(encoded)...))
		if bytes.Compare(hash[:diffLen], target) <= 0 {
			return encoded, true, attempt
		}
	}

	fallback := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%q", seed)))
	return fallback, false, -1
}

func hexStringToBytes(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("invalid hex string length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i/2] = byte(v)
	}
	return out, nil
}

func buildConversationRequestFromOpenAIChat(chatReqBody, originalRequest []byte, requestedModel string) ([]byte, error) {
	root := gjson.ParseBytes(chatReqBody)
	messages := make([]map[string]any, 0)
	msgs := root.Get("messages")
	if msgs.Exists() && msgs.IsArray() {
		msgs.ForEach(func(_, msg gjson.Result) bool {
			role := strings.TrimSpace(msg.Get("role").String())
			if role == "" {
				return true
			}
			contentType := "text"
			parts := make([]any, 0)
			content := msg.Get("content")
			switch {
			case content.Type == gjson.String:
				if text := strings.TrimSpace(content.String()); text != "" {
					parts = append(parts, text)
				}
			case content.IsArray():
				content.ForEach(func(_, part gjson.Result) bool {
					switch strings.TrimSpace(part.Get("type").String()) {
					case "", "text":
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							parts = append(parts, text)
						}
					case "image_url":
						if imageURL := strings.TrimSpace(part.Get("image_url.url").String()); imageURL != "" {
							parts = append(parts, "[image_url omitted] "+imageURL)
						}
					}
					return true
				})
			}
			if len(parts) == 0 {
				if toolCalls := msg.Get("tool_calls"); toolCalls.Exists() && strings.TrimSpace(toolCalls.Raw) != "" {
					parts = append(parts, "[tool_calls] "+toolCalls.Raw)
				} else {
					parts = append(parts, "")
				}
			}
			messages = append(messages, map[string]any{
				"id":     uuid.NewString(),
				"author": map[string]any{"role": role},
				"content": map[string]any{
					"content_type": contentType,
					"parts":        parts,
				},
				"metadata":    map[string]any{},
				"create_time": float64(time.Now().UnixNano()) / 1e9,
			})
			return true
		})
	}

	systemHints := detectConversationSystemHints(originalRequest)
	body := map[string]any{
		"action": "next",
		"client_contextual_info": map[string]any{
			"is_dark_mode":      false,
			"time_since_loaded": 123,
			"page_height":       919,
			"page_width":        1920,
			"pixel_ratio":       1.0,
			"screen_height":     1080,
			"screen_width":      1920,
		},
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"conversation_origin":           nil,
		"force_paragen":                 false,
		"force_paragen_model_slug":      "",
		"force_rate_limit":              false,
		"force_use_sse":                 true,
		"history_and_training_disabled": true,
		"messages":                      messages,
		"model":                         mapConversationModel(requestedModel),
		"parent_message_id":             uuid.NewString(),
		"reset_rate_limits":             false,
		"suggestions":                   []any{},
		"supported_encodings":           []any{},
		"system_hints":                  systemHints,
		"timezone":                      "America/Los_Angeles",
		"timezone_offset_min":           -480,
		"variant_purpose":               "comparison_implicit",
		"websocket_request_id":          uuid.NewString(),
	}
	return json.Marshal(body)
}

func detectConversationSystemHints(originalRequest []byte) []string {
	hints := make([]string, 0)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		hints = append(hints, value)
	}
	if gjson.GetBytes(originalRequest, "system_hints").IsArray() {
		gjson.GetBytes(originalRequest, "system_hints").ForEach(func(_, item gjson.Result) bool {
			add(item.String())
			return true
		})
	}
	if gjson.GetBytes(originalRequest, "tools").IsArray() {
		gjson.GetBytes(originalRequest, "tools").ForEach(func(_, item gjson.Result) bool {
			toolType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			if strings.Contains(toolType, "search") {
				add("search")
			}
			return true
		})
	}
	return hints
}

func mapConversationModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch normalized {
	case "gpt-5.2", "gpt-5-2":
		return "gpt-5-2"
	case "gpt-5.4", "gpt-5-4", "gpt-5-4-t-mini":
		return "gpt-5-4-t-mini"
	default:
		if strings.Contains(normalized, ".") {
			return strings.ReplaceAll(normalized, ".", "-")
		}
		return normalized
	}
}

func buildOpenAINonStreamFromConversationSSE(data []byte, model string) ([]byte, error) {
	id := fmt.Sprintf("chatcmpl_%d", time.Now().UnixNano())
	created := time.Now().Unix()
	lastText := ""
	var streamErr error

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		payload := jsonPayload(scanner.Bytes())
		if len(payload) == 0 {
			continue
		}
		if gjson.GetBytes(payload, "type").String() == "error" {
			message := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
			if message == "" {
				message = strings.TrimSpace(gjson.GetBytes(payload, "error").String())
			}
			if message == "" {
				message = "conversation stream returned error"
			}
			streamErr = statusErr{code: http.StatusBadGateway, msg: message}
			break
		}
		author := strings.TrimSpace(gjson.GetBytes(payload, "message.author.role").String())
		contentType := strings.TrimSpace(gjson.GetBytes(payload, "message.content.content_type").String())
		if author != "assistant" || contentType != "text" {
			continue
		}
		text := joinConversationTextParts(gjson.GetBytes(payload, "message.content.parts"))
		if text != "" {
			lastText = text
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if streamErr != nil {
		return nil, streamErr
	}

	response := `{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`
	response, _ = sjson.Set(response, "id", id)
	response, _ = sjson.Set(response, "created", created)
	response, _ = sjson.Set(response, "model", model)
	response, _ = sjson.Set(response, "choices.0.message.content", lastText)
	return []byte(response), nil
}

func convertConversationSSELineToOpenAIChunks(line []byte, state *chatGPTConversationStreamState) ([][]byte, error) {
	payload := jsonPayload(line)
	if len(payload) == 0 {
		return nil, nil
	}
	if gjson.GetBytes(payload, "type").String() == "error" {
		message := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
		if message == "" {
			message = strings.TrimSpace(gjson.GetBytes(payload, "error").String())
		}
		if message == "" {
			message = "conversation stream returned error"
		}
		return nil, statusErr{code: http.StatusBadGateway, msg: message}
	}

	author := strings.TrimSpace(gjson.GetBytes(payload, "message.author.role").String())
	contentType := strings.TrimSpace(gjson.GetBytes(payload, "message.content.content_type").String())
	if author != "assistant" || contentType != "text" {
		return nil, nil
	}
	messageID := strings.TrimSpace(gjson.GetBytes(payload, "message.id").String())
	if messageID != "" && messageID != state.messageID {
		state.messageID = messageID
		state.lastText = ""
	}
	fullText := joinConversationTextParts(gjson.GetBytes(payload, "message.content.parts"))
	delta := computeConversationDelta(state.lastText, fullText)
	status := strings.TrimSpace(gjson.GetBytes(payload, "message.status").String())
	state.lastStatus = status
	out := make([][]byte, 0, 2)
	if delta != "" {
		out = append(out, buildOpenAIChatCompletionStreamChunk(state, delta, state.lastText == ""))
		state.lastText = fullText
	}
	if status != "" && status != "in_progress" && !state.finished {
		out = append(out, buildOpenAIChatCompletionStreamFinishChunk(state))
		state.finished = true
	}
	return out, nil
}

func joinConversationTextParts(parts gjson.Result) string {
	if !parts.IsArray() {
		return ""
	}
	joined := strings.Builder{}
	parts.ForEach(func(_, part gjson.Result) bool {
		text := strings.TrimSpace(part.String())
		if text == "" {
			return true
		}
		if joined.Len() > 0 {
			joined.WriteString("\n")
		}
		joined.WriteString(text)
		return true
	})
	return joined.String()
}

func computeConversationDelta(previous, current string) string {
	if current == "" {
		return ""
	}
	if previous == "" {
		return current
	}
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	return current
}

func buildOpenAIChatCompletionStreamChunk(state *chatGPTConversationStreamState, delta string, includeRole bool) []byte {
	chunk := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`
	chunk, _ = sjson.Set(chunk, "id", state.id)
	chunk, _ = sjson.Set(chunk, "created", state.created)
	chunk, _ = sjson.Set(chunk, "model", state.model)
	if includeRole {
		chunk, _ = sjson.Set(chunk, "choices.0.delta.role", "assistant")
	}
	chunk, _ = sjson.Set(chunk, "choices.0.delta.content", delta)
	return []byte("data: " + chunk)
}

func buildOpenAIChatCompletionStreamFinishChunk(state *chatGPTConversationStreamState) []byte {
	chunk := `{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	chunk, _ = sjson.Set(chunk, "id", state.id)
	chunk, _ = sjson.Set(chunk, "created", state.created)
	chunk, _ = sjson.Set(chunk, "model", state.model)
	return []byte("data: " + chunk)
}
