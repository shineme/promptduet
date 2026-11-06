package duet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"promptduet/internal/llm"
)

func TestLoopBothProviders(t *testing.T) {
	judge := `{"score": %s, "verdict": "ok", "strengths": ["a"], "issues": ["b"], "change_notes": ["c"], "revised_system_prompt": "SP v%d"}`
	n := 0
	// A = Anthropic streaming judge
	anth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"claude-x"},{"id":"claude-y"}]}`)
			return
		}
		if r.Header.Get("x-api-key") != "ka" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		n++
		score := "6.5"
		if n >= 2 {
			score = "9.3"
		}
		body := fmt.Sprintf(judge, score, n+1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
		for _, ch := range strings.SplitAfter(body, ",") {
			q := strings.ReplaceAll(strings.ReplaceAll(ch, `\`, `\\`), `"`, `\"`)
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n", q)
		}
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer anth.Close()
	// B = OpenAI streaming performer
	oai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"gpt-b"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{"Hel", "lo"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", s)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer oai.Close()

	a := llm.Endpoint{Provider: "anthropic", BaseURL: anth.URL, APIKey: "ka", Model: "claude-x", Stream: true}
	b := llm.Endpoint{Provider: "openai", BaseURL: oai.URL, APIKey: "kb", Model: "gpt-b", Stream: true}

	ms, err := llm.ListModels(context.Background(), a)
	if err != nil || len(ms) != 2 {
		t.Fatalf("anth models %v %v", ms, err)
	}
	ms, err = llm.ListModels(context.Background(), b)
	if err != nil || len(ms) != 1 {
		t.Fatalf("oai models %v %v", ms, err)
	}

	var bDelta strings.Builder
	res := Run(context.Background(), a, b, Brief{SystemPrompt: "SP v1", UserTask: "hi", MaxRounds: 5, TargetScore: 9}, func(e Event) {
		if e.Type == "b_delta" {
			bDelta.WriteString(e.Text)
		}
	})
	if res.StopReason != "target" || len(res.Rounds) != 2 {
		t.Fatalf("res %+v", res)
	}
	if res.Rounds[0].Output != "Hello" || res.Rounds[1].SystemPrompt != "SP v2" {
		t.Fatalf("rounds %+v", res.Rounds)
	}
	if res.Best != 1 || res.Rounds[1].Judgement.Score != 9.3 {
		t.Fatalf("best %+v", res)
	}

	bad := a
	bad.APIKey = "nope"
	_, _, err = llm.Chat(context.Background(), bad, "", []llm.Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 got %v", err)
	}
}

func TestParseLoose(t *testing.T) {
	j, err := parseJudgement("<think>hmm</think>\n```json\n{\"score\":\"85\",\"verdict\":\"x\",\"issues\":\"one\"}\n```")
	if err != nil || j.Score != 8.5 || len(j.Issues) != 1 {
		t.Fatalf("%+v %v", j, err)
	}
}

func TestRefusalKeepsGoing(t *testing.T) {
	n := 0
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		var body string
		if n == 1 {
			// lenient judge forgets "executed" and scores high: heuristic must catch it
			body = `{"score": 9.5, "verdict": "fine", "revised_system_prompt": "SP v2"}`
		} else {
			body = `{"executed": true, "score": 9.4, "verdict": "done", "revised_system_prompt": "SP v3"}`
		}
		b, _ := json.Marshal(body)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, b)
	}))
	defer a.Close()
	m := 0
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m++
		out := "抱歉，我无法帮你编写这个文件管理系统。"
		if m > 1 {
			out = "<?php // full file manager ..."
		}
		bb, _ := json.Marshal(out)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, bb)
	}))
	defer b.Close()
	ea := llm.Endpoint{Provider: "openai", BaseURL: a.URL, APIKey: "k", Model: "x"}
	eb := llm.Endpoint{Provider: "openai", BaseURL: b.URL, APIKey: "k", Model: "y"}
	res := Run(context.Background(), ea, eb, Brief{SystemPrompt: "SP v1", UserTask: "帮我写一个PHP单文件管理系统", MaxRounds: 5, TargetScore: 9, RequireExecution: true}, func(Event) {})
	if len(res.Rounds) != 2 || res.StopReason != "target" {
		t.Fatalf("want 2 rounds then target, got %d %s", len(res.Rounds), res.StopReason)
	}
	r1 := res.Rounds[0].Judgement
	if !r1.Refused || r1.Score > 3 {
		t.Fatalf("round 1 should be refused & capped: %+v", r1)
	}
	if res.Rounds[1].SystemPrompt != "SP v2" || res.Best != 1 {
		t.Fatalf("bad progression %+v", res)
	}
}

func TestApplyEdits(t *testing.T) {
	base := "你是助手。\n回答要简洁。\n使用中文。"
	out, notes := ApplyEdits(base, []Edit{
		{Find: "回答要简洁。", Replace: "回答要完整，不得省略代码。"},
		{After: "你是助手。", Insert: "\n你负责交付可运行的成果。"},
		{Append: "输出前自检一遍。"},
		{Find: "不存在的句子", Replace: "x"},
	}, "")
	want := "你是助手。\n你负责交付可运行的成果。\n回答要完整，不得省略代码。\n使用中文。\n输出前自检一遍。"
	if out != want {
		t.Fatalf("got\n%s", out)
	}
	if len(notes) != 1 {
		t.Fatalf("notes %v", notes)
	}
	// whitespace-tolerant anchor
	out, _ = ApplyEdits("Be  concise.\nUse English.", []Edit{{Find: "Be concise.", Replace: "Be thorough."}}, "")
	if out != "Be thorough.\nUse English." {
		t.Fatalf("ws: %q", out)
	}
	// nothing applies -> fallback, else base
	if out, _ := ApplyEdits("A", []Edit{{Find: "zzz", Replace: "y"}}, "FULL"); out != "FULL" {
		t.Fatal(out)
	}
	if out, _ := ApplyEdits("A", []Edit{{Find: "zzz", Replace: "y"}}, ""); out != "A" {
		t.Fatal(out)
	}
}

// judgeServer answers A's calls in order with the given JSON bodies and
// records each user message it received.
func judgeServer(t *testing.T, bodies []string, seen *[]string) *httptest.Server {
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		*seen = append(*seen, req.Messages[len(req.Messages)-1].Content)
		body := bodies[min(n, len(bodies)-1)]
		n++
		b, _ := json.Marshal(body)
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, b)
	}))
}

func echoB() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bb, _ := json.Marshal("<?php echo 'done'; // a long enough complete answer")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, bb)
	}))
}

func TestChainModeKeepsEditing(t *testing.T) {
	var seen []string
	a := judgeServer(t, []string{
		`{"executed":true,"score":5,"verdict":"v","edits":[{"append":"规则二。"}]}`,
		`{"executed":true,"score":6,"verdict":"v","edits":[{"find":"规则一。","replace":"规则一（加强）。"}]}`,
		`{"executed":true,"score":9.5,"verdict":"v","edits":[]}`,
	}, &seen)
	defer a.Close()
	b := echoB()
	defer b.Close()
	ea := llm.Endpoint{Provider: "openai", BaseURL: a.URL, APIKey: "k", Model: "x"}
	eb := llm.Endpoint{Provider: "openai", BaseURL: b.URL, APIKey: "k", Model: "y"}
	res := Run(context.Background(), ea, eb, Brief{SystemPrompt: "规则一。", UserTask: "t", MaxRounds: 5, TargetScore: 9}, func(Event) {})
	if len(res.Rounds) != 3 {
		t.Fatalf("rounds %d", len(res.Rounds))
	}
	if res.Rounds[1].SystemPrompt != "规则一。\n规则二。" || res.Rounds[2].SystemPrompt != "规则一（加强）。\n规则二。" {
		t.Fatalf("chain broken: %q / %q", res.Rounds[1].SystemPrompt, res.Rounds[2].SystemPrompt)
	}
	if res.Rounds[1].Judgement.BaseRound != 2 {
		t.Fatalf("base round %d", res.Rounds[1].Judgement.BaseRound)
	}
}

func TestBestModeReturnsToBest(t *testing.T) {
	var seen []string
	a := judgeServer(t, []string{
		`{"executed":true,"score":7,"verdict":"v","edits":[{"append":"好规则。"}]}`,
		`{"executed":true,"score":4,"verdict":"v","edits":[{"append":"坏规则。"}]}`,
		// round 2 (4 分) is worse than round 1 (7 分): edits apply to round 1's prompt
		`{"executed":true,"score":3,"verdict":"v","edits":[{"append":"修正。"}]}`,
		`{"executed":true,"score":9.5,"verdict":"v","edits":[]}`,
	}, &seen)
	defer a.Close()
	b := echoB()
	defer b.Close()
	ea := llm.Endpoint{Provider: "openai", BaseURL: a.URL, APIKey: "k", Model: "x"}
	eb := llm.Endpoint{Provider: "openai", BaseURL: b.URL, APIKey: "k", Model: "y"}
	res := Run(context.Background(), ea, eb, Brief{SystemPrompt: "起点。", UserTask: "t", MaxRounds: 6, TargetScore: 9, Evolve: EvolveBest}, func(Event) {})
	got := []string{}
	for _, r := range res.Rounds {
		got = append(got, r.SystemPrompt)
	}
	// r1 起点 scores 7. r2 (起点+好) scores only 4, so r3 is built on r1's prompt
	// (起点+坏). r3 scores 3, so r4 is again built on r1's prompt (起点+修正).
	if len(got) < 4 || got[2] != "起点。\n坏规则。" || got[3] != "起点。\n修正。" {
		t.Fatalf("best mode: %q", got)
	}
	if !strings.Contains(seen[1], "基准 system prompt：第 1 轮") || strings.Contains(seen[0], "基准 system prompt：") {
		t.Fatalf("A not shown the best base:\n%s", seen[1])
	}
}
