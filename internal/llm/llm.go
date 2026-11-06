// Package llm is a small, dependency-free client for OpenAI-compatible and
// Anthropic (Claude) chat APIs, with model listing and streaming support.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"

	DefaultOpenAIBase    = "https://api.openai.com/v1"
	DefaultAnthropicBase = "https://api.anthropic.com"
	anthropicVersion     = "2023-06-01"
)

// Endpoint describes one upstream model the app can talk to.
type Endpoint struct {
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"baseURL"`
	APIKey      string  `json:"apiKey"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"maxTokens"`
	Stream      bool    `json:"stream"`
	// TimeoutSec caps one whole request (0 = 900s). A stream that sends no
	// bytes at all for StallSec (0 = 180s) is aborted as stalled.
	TimeoutSec int `json:"timeoutSec"`
	StallSec   int `json:"stallSec"`
}

// Message is a single chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage reports token accounting when the upstream returns it.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

func (e Endpoint) provider() string {
	p := strings.ToLower(strings.TrimSpace(e.Provider))
	if p == "claude" {
		return ProviderAnthropic
	}
	if p == "" {
		return ProviderOpenAI
	}
	return p
}

// base returns the normalized API root.
// OpenAI style: ".../v1"   Anthropic style: root without "/v1".
func (e Endpoint) base() string {
	b := strings.TrimRight(strings.TrimSpace(e.BaseURL), "/")
	switch e.provider() {
	case ProviderAnthropic:
		if b == "" {
			b = DefaultAnthropicBase
		}
		b = strings.TrimSuffix(b, "/v1")
		b = strings.TrimSuffix(b, "/messages")
		return b
	default:
		if b == "" {
			b = DefaultOpenAIBase
		}
		b = strings.TrimSuffix(b, "/chat/completions")
		b = strings.TrimSuffix(b, "/models")
		// Many proxies are given as bare hosts; append /v1 when no version segment is present.
		if !hasVersionSegment(b) {
			b += "/v1"
		}
		return b
	}
}

func hasVersionSegment(u string) bool {
	parts := strings.Split(u, "/")
	if len(parts) == 0 {
		return false
	}
	last := parts[len(parts)-1]
	if len(last) >= 2 && last[0] == 'v' && last[1] >= '0' && last[1] <= '9' {
		return true
	}
	// e.g. .../openai, .../api/paas/v4 already handled; treat "compatible-mode/v1" etc.
	return strings.Contains(u, "/v1/") || strings.Contains(u, "/v2/") || strings.Contains(u, "/v3/") || strings.Contains(u, "/v4/")
}

func (e Endpoint) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	switch e.provider() {
	case ProviderAnthropic:
		req.Header.Set("x-api-key", e.APIKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		// Some relays only accept bearer auth; sending both is harmless.
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
}

// ListModels fetches the available models from the upstream, with whatever
// token limits it reports, topped up from the built-in reference table.
func ListModels(ctx context.Context, e Endpoint) ([]ModelInfo, error) {
	if strings.TrimSpace(e.APIKey) == "" {
		return nil, errors.New("缺少 API Key")
	}
	url := e.base() + "/models"
	if e.provider() == ProviderAnthropic {
		url = e.base() + "/v1/models?limit=1000"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	e.setHeaders(req)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接失败：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, upstreamError(resp.StatusCode, body)
	}
	var parsed struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("无法解析模型列表：%w", err)
	}
	seen := map[string]bool{}
	var out []ModelInfo
	add := func(m map[string]any) {
		id, _ := m["id"].(string)
		if id == "" {
			id, _ = m["name"].(string)
		}
		id = strings.TrimSpace(strings.TrimPrefix(id, "models/"))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		up := infoFromUpstream(id, m)
		out = append(out, Lookup(e.provider(), id, &up))
	}
	for _, m := range parsed.Data {
		add(m)
	}
	for _, m := range parsed.Models {
		add(m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, errors.New("上游没有返回任何模型")
	}
	return out, nil
}

func upstreamError(code int, body []byte) error {
	var e struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(body, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				msg = m
			}
		}
		if msg == "" {
			msg = e.Msg
		}
	}
	if msg == "" {
		msg = snippet(body)
	}
	hint := ""
	switch code {
	case 401, 403:
		hint = "（密钥无效或无权限）"
	case 404:
		hint = "（地址或模型不存在，检查接口地址）"
	case 429:
		hint = "（被限流或额度不足）"
	}
	return fmt.Errorf("HTTP %d%s %s", code, hint, msg)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) > 1500 {
		return string(r[:1500]) + "…"
	}
	return s
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
