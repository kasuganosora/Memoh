package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/channel/common"
	"github.com/memohai/memoh/internal/settings"
)

const (
	webFetchMaxTextContent = 10000
	webFetchTimeout        = 30 * time.Second
	webFetchUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
)

type WebFetchProvider struct {
	logger   *slog.Logger
	settings *settings.Service
}

func NewWebFetchProvider(log *slog.Logger, settingsSvc *settings.Service) *WebFetchProvider {
	if log == nil {
		log = slog.Default()
	}
	return &WebFetchProvider{
		logger:   log.With(slog.String("tool", "webfetch")),
		settings: settingsSvc,
	}
}

func (p *WebFetchProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	sess := session
	return []sdk.Tool{
		{
			Name:        "web_fetch",
			Description: "Fetch a URL and convert the response to readable content. Supports HTML (converts to Markdown), JSON, XML, and plain text formats.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "markdown", "json", "xml", "text"},
						"description": "Output format (default: auto - detects from content type)",
					},
				},
				"required": []string{"url"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				rawURL := strings.TrimSpace(StringArg(args, "url"))
				if rawURL == "" {
					return nil, errors.New("url is required")
				}
				format := strings.TrimSpace(StringArg(args, "format"))
				if format == "" {
					format = "auto"
				}
				return p.callWebFetch(ctx.Context, sess.BotID, rawURL, format)
			},
		},
	}, nil
}

// buildHTTPClient creates an HTTP client with optional proxy support.
// If the bot has an http_proxy_url configured, it routes requests through
// that proxy to mitigate SSRF risks (the proxy can enforce network policies).
// Otherwise, it falls back to ProxyFromEnvironment (HTTP_PROXY/HTTPS_PROXY).
func (p *WebFetchProvider) buildHTTPClient(ctx context.Context, botID string) *http.Client {
	var proxyURL string
	if p.settings != nil && strings.TrimSpace(botID) != "" {
		if botSettings, err := p.settings.GetBot(ctx, botID); err == nil {
			proxyURL = botSettings.HttpProxyUrl
		}
	}

	proxyCfg := common.HTTPProxyConfig{URL: proxyURL}
	client, err := common.NewHTTPClient(webFetchTimeout, proxyCfg)
	if err != nil {
		p.logger.Warn("failed to build proxy-aware HTTP client, falling back to default",
			slog.String("proxy_url", proxyURL),
			slog.Any("error", err),
		)
		return &http.Client{Timeout: webFetchTimeout}
	}
	return client
}

func (p *WebFetchProvider) callWebFetch(ctx context.Context, botID, rawURL, format string) (any, error) {
	client := p.buildHTTPClient(ctx, botID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	req.Header.Set("User-Agent", webFetchUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http error: %d %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	detected := format
	if format == "auto" {
		detected = detectWebFetchFormat(contentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch detected {
	case "json":
		return p.processJSON(rawURL, contentType, body)
	case "xml":
		return p.processXML(rawURL, contentType, body)
	case "markdown":
		return p.processHTML(rawURL, contentType, body)
	default:
		return p.processText(rawURL, contentType, body)
	}
}

func detectWebFetchFormat(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "application/json"):
		return "json"
	case strings.Contains(ct, "application/xml"), strings.Contains(ct, "text/xml"):
		return "xml"
	case strings.Contains(ct, "text/html"):
		return "markdown"
	default:
		return "text"
	}
}

func (*WebFetchProvider) processJSON(fetchedURL, contentType string, body []byte) (any, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, errors.New("failed to parse json")
	}
	return map[string]any{"success": true, "url": fetchedURL, "format": "json", "contentType": contentType, "data": data}, nil
}

func (*WebFetchProvider) processXML(fetchedURL, contentType string, body []byte) (any, error) {
	content := string(body)
	if len(content) > webFetchMaxTextContent {
		content = content[:webFetchMaxTextContent]
	}
	return map[string]any{"success": true, "url": fetchedURL, "format": "xml", "contentType": contentType, "content": content}, nil
}

func (p *WebFetchProvider) processHTML(fetchedURL, contentType string, body []byte) (any, error) {
	parsed, err := url.Parse(fetchedURL)
	if err != nil {
		parsed = &url.URL{}
	}
	article, err := readability.FromReader(strings.NewReader(string(body)), parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to extract readable content from html: %w", err)
	}
	if strings.TrimSpace(article.Content) == "" {
		return nil, errors.New("failed to extract readable content from html")
	}
	markdown, err := htmltomarkdown.ConvertString(article.Content)
	if err != nil {
		p.logger.Warn("html-to-markdown conversion failed, falling back to text", slog.Any("error", err))
		markdown = article.TextContent
	}
	textPreview := article.TextContent
	if len(textPreview) > 500 {
		textPreview = textPreview[:500]
	}
	return map[string]any{
		"success": true, "url": fetchedURL, "format": "markdown", "contentType": contentType,
		"title": article.Title, "byline": article.Byline, "excerpt": article.Excerpt,
		"content": markdown, "textContent": textPreview, "length": article.Length,
	}, nil
}

func (*WebFetchProvider) processText(fetchedURL, contentType string, body []byte) (any, error) {
	content := string(body)
	length := len(content)
	if length > webFetchMaxTextContent {
		content = content[:webFetchMaxTextContent]
	}
	return map[string]any{"success": true, "url": fetchedURL, "format": "text", "contentType": contentType, "content": content, "length": length}, nil
}
