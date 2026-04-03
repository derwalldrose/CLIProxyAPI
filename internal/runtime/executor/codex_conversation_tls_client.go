package executor

import (
	"bytes"
	"context"
	"io"
	stdhttp "net/http"
	"sort"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type conversationRequestClient interface {
	Do(ctx context.Context, method, url string, headers stdhttp.Header, body []byte) (*stdhttp.Response, error)
}

type conversationTLSProfile struct {
	name             string
	clientProfile    profiles.ClientProfile
	defaultUserAgent string
}

type conversationTLSClient struct {
	client tls_client.HttpClient
}

func newConversationTLSClient(cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration, profile conversationTLSProfile) (*conversationTLSClient, error) {
	options := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profile.clientProfile),
		tls_client.WithDisableHttp3(),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	}
	if timeout > 0 {
		seconds := int(timeout / time.Second)
		if timeout%time.Second != 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}
		options = append(options, tls_client.WithTimeoutSeconds(seconds))
	}

	proxyURL := resolveExecutorProxyURL(cfg, auth)
	if proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	return &conversationTLSClient{client: client}, nil
}

func conversationTLSProfiles() []conversationTLSProfile {
	return []conversationTLSProfile{
		{
			name:             "chrome_146",
			clientProfile:    profiles.Chrome_146,
			defaultUserAgent: chatGPTConversationDefaultUA,
		},
		{
			name:             "safari_16_0",
			clientProfile:    profiles.Safari_16_0,
			defaultUserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 13_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15",
		},
		{
			name:             "safari_15_6_1",
			clientProfile:    profiles.Safari_15_6_1,
			defaultUserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 12_6_1) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.6.1 Safari/605.1.15",
		},
		{
			name:             "cloudflare_custom",
			clientProfile:    profiles.CloudflareCustom,
			defaultUserAgent: chatGPTConversationDefaultUA,
		},
		{
			name:             "chrome_133",
			clientProfile:    profiles.Chrome_133,
			defaultUserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		},
	}
}

func (c *conversationTLSClient) Do(ctx context.Context, method, url string, headers stdhttp.Header, body []byte) (*stdhttp.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := fhttp.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.Header = cloneConversationHeadersToFHTTP(headers)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	return &stdhttp.Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     cloneConversationHeadersToStd(resp.Header),
		Body:       resp.Body,
	}, nil
}

func resolveExecutorProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

func cloneConversationHeadersToFHTTP(src stdhttp.Header) fhttp.Header {
	dst := make(fhttp.Header, len(src)+1)
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	order := conversationHeaderOrder(src)
	if len(order) > 0 {
		dst[fhttp.HeaderOrderKey] = order
	}
	return dst
}

func cloneConversationHeadersToStd(src fhttp.Header) stdhttp.Header {
	dst := make(stdhttp.Header, len(src))
	for key, values := range src {
		if strings.EqualFold(key, fhttp.HeaderOrderKey) {
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func conversationHeaderOrder(h stdhttp.Header) []string {
	preferred := []string{
		"accept",
		"accept-language",
		"accept-encoding",
		"content-type",
		"authorization",
		"chatgpt-account-id",
		"origin",
		"referer",
		"oai-device-id",
		"oai-language",
		"sec-fetch-dest",
		"sec-fetch-mode",
		"sec-fetch-site",
		"user-agent",
		"connection",
	}

	order := make([]string, 0, len(preferred))
	seen := make(map[string]struct{}, len(preferred))
	for _, key := range preferred {
		if strings.TrimSpace(h.Get(key)) == "" {
			continue
		}
		order = append(order, key)
		seen[key] = struct{}{}
	}

	extra := make([]string, 0)
	for key := range h {
		if strings.EqualFold(key, fhttp.HeaderOrderKey) {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" {
			continue
		}
		if _, ok := seen[lower]; ok {
			continue
		}
		extra = append(extra, lower)
	}
	sort.Strings(extra)
	order = append(order, extra...)
	return order
}
