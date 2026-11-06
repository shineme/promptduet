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
