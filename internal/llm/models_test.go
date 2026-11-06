package llm

import "testing"

func TestLookup(t *testing.T) {
	cases := []struct {
		id   string
		out  int
		temp bool
	}{
		{"claude-sonnet-4-5", 64000, true},
		{"claude-opus-4-1-20250805", 32000, true},
		{"anthropic/claude-3-5-haiku-latest", 8192, true},
		{"gpt-4o-mini", 16384, true},
		{"gpt-5-mini", 128000, false},
		{"o3", 100000, false},
		{"o4-mini", 100000, false},
		{"omni-moderation", 0, true},
		{"deepseek-reasoner", 64000, false},
		{"some-unknown", 0, true},
	}
	for _, c := range cases {
		mi := Lookup(ProviderOpenAI, c.id, nil)
		if mi.MaxOutput != c.out || mi.Temperature != c.temp {
			t.Errorf("%s: got out=%d temp=%v", c.id, mi.MaxOutput, mi.Temperature)
		}
	}
	up := infoFromUpstream("x/y", map[string]any{"context_length": 131072.0, "top_provider": map[string]any{"max_completion_tokens": 20000.0}, "supported_parameters": []any{"max_tokens"}})
	mi := Lookup(ProviderOpenAI, "x/y", &up)
	if mi.MaxOutput != 20000 || mi.Context != 131072 || mi.Temperature || mi.Source != "upstream" {
		t.Errorf("upstream merge: %+v", mi)
	}
	if Lookup(ProviderAnthropic, "claude-haiku-4-5", nil).TempMax != 1 {
		t.Error("claude temp max")
	}
}
