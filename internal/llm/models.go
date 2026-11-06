package llm

import (
	"encoding/json"
	"strings"
)

// ModelInfo is what we know about a model's limits. Upstreams differ wildly:
// OpenAI and most relays return only ids, OpenRouter / LiteLLM / Gemini-style
// lists carry token limits and supported parameters. Anything the upstream
// does not say is filled from a built-in reference table.
type ModelInfo struct {
	ID          string  `json:"id"`
	MaxOutput   int     `json:"maxOutput"`   // 0 = unknown
	Context     int     `json:"context"`     // 0 = unknown
	Temperature bool    `json:"temperature"` // false = model rejects temperature
	TempMax     float64 `json:"tempMax"`     // 1 for Claude, 2 for OpenAI-style
	Source      string  `json:"source"`      // "upstream" | "builtin" | ""
}

func intOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		var n int
		for _, c := range strings.TrimSpace(t) {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
}

func firstInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if n := intOf(m[k]); n > 0 {
			return n
		}
	}
	return 0
}

// infoFromUpstream reads whatever limit fields one model entry carries.
func infoFromUpstream(id string, m map[string]any) ModelInfo {
	mi := ModelInfo{ID: id}
	mi.MaxOutput = firstInt(m, "max_output_tokens", "max_completion_tokens", "output_token_limit", "outputTokenLimit", "max_tokens", "maxOutputTokens")
	if tp, ok := m["top_provider"].(map[string]any); ok && mi.MaxOutput == 0 {
		mi.MaxOutput = firstInt(tp, "max_completion_tokens", "max_output_tokens")
	}
	mi.Context = firstInt(m, "context_length", "context_window", "max_input_tokens", "inputTokenLimit", "max_model_len")
	if tp, ok := m["top_provider"].(map[string]any); ok && mi.Context == 0 {
		mi.Context = firstInt(tp, "context_length")
	}
	mi.Temperature = true
	if sp, ok := m["supported_parameters"].([]any); ok && len(sp) > 0 {
		has := false
		for _, p := range sp {
			if s, _ := p.(string); s == "temperature" {
				has = true
			}
		}
		mi.Temperature = has
	}
	if mi.MaxOutput > 0 || mi.Context > 0 {
		mi.Source = "upstream"
	}
	return mi
}

type ref struct {
	match  string
	out    int
	ctx    int
	noTemp bool
}

// Reference limits, matched in order against the lower-cased id (substring).
// Values are the vendors' published max output tokens; relays may cap lower.
var refTable = []ref{
	{"claude-opus-4-1", 32000, 200000, false},
	{"claude-opus-4-0", 32000, 200000, false},
	{"claude-opus-4-2025", 32000, 200000, false},
	{"claude-opus-4", 64000, 200000, false},
	{"claude-sonnet-4", 64000, 200000, false},
	{"claude-haiku-4", 64000, 200000, false},
	{"claude-3-7-sonnet", 64000, 200000, false},
	{"claude-3-5", 8192, 200000, false},
	{"claude-3", 4096, 200000, false},
	{"gpt-5-chat", 16384, 128000, false},
	{"gpt-5", 128000, 400000, true},
	{"gpt-4.1", 32768, 1047576, false},
	{"gpt-4o", 16384, 128000, false},
	{"chatgpt-4o", 16384, 128000, false},
	{"gpt-4-turbo", 4096, 128000, false},
	{"gpt-3.5", 4096, 16385, false},
	{"o1-mini", 65536, 128000, true},
	{"o1", 100000, 200000, true},
	{"o3", 100000, 200000, true},
	{"o4-mini", 100000, 200000, true},
	{"deepseek-reasoner", 64000, 128000, true},
	{"deepseek-r1", 64000, 128000, true},
	{"deepseek-chat", 8192, 128000, false},
	{"deepseek-v3", 8192, 128000, false},
	{"gemini-3", 65536, 1048576, false},
	{"gemini-2.5", 65536, 1048576, false},
	{"gemini-2.0", 8192, 1048576, false},
	{"gemini-1.5", 8192, 1048576, false},
}

func isOSeries(id string) bool {
	for _, p := range []string{"o1", "o3", "o4"} {
		if id == p || strings.HasPrefix(id, p+"-") {
			return true
		}
	}
	return false
}

// Lookup merges upstream info (if any) with the reference table.
func Lookup(provider, id string, up *ModelInfo) ModelInfo {
	mi := ModelInfo{ID: id, Temperature: true}
	if up != nil {
		mi = *up
	}
	lid := strings.ToLower(id)
	if i := strings.LastIndex(lid, "/"); i >= 0 {
		lid = lid[i+1:]
	}
	for _, r := range refTable {
		if len(r.match) > 1 && r.match[0] == 'o' && r.match[1] >= '0' && r.match[1] <= '9' {
			if !isOSeries(lid) || !strings.HasPrefix(lid, r.match) {
				continue
			}
		} else if !strings.Contains(lid, r.match) {
			continue
		}
		if mi.MaxOutput == 0 {
			mi.MaxOutput = r.out
			if mi.Source == "" {
				mi.Source = "builtin"
			}
		}
		if mi.Context == 0 {
			mi.Context = r.ctx
		}
		if r.noTemp {
			mi.Temperature = false
		}
		break
	}
	mi.TempMax = 2
	if provider == ProviderAnthropic || strings.Contains(lid, "claude") {
		mi.TempMax = 1
	}
	return mi
}
