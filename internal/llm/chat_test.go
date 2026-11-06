package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func capture() (context.Context, *[]string) {
	var mu sync.Mutex
	lines := []string{}
	ctx := WithHooks(context.Background(), Hooks{Log: func(l, m string) { mu.Lock(); lines = append(lines, l+" "+m); mu.Unlock() }})
	return ctx, &lines
}

func sse(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f := w.(http.Flusher)
	f.Flush()
	return f
}

func TestStreamSingleNewlineNoDoneHangs(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := sse(w)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"想一想\"}}]}\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<?php \"}}]}\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"echo 1;\"},\"finish_reason\":\"stop\"}]}\n")
		f.Flush()
		<-block // relay never closes
	}))
	defer srv.Close()
	defer close(block)
	ctx, lines := capture()
	var think strings.Builder
	ctx = WithHooks(ctx, Hooks{Log: getHooks(ctx).Log, Think: func(d string) { think.WriteString(d) }})
	e := Endpoint{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Stream: true, StallSec: 2}
	t0 := time.Now()
	out, _, err := Chat(ctx, e, "", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil || out != "<?php echo 1;" || think.String() != "想一想" {
		t.Fatalf("out=%q err=%v think=%q\n%s", out, err, think.String(), strings.Join(*lines, "\n"))
	}
	if time.Since(t0) > 6*time.Second {
		t.Fatal("took too long")
	}
}

func TestStreamStallErrors(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w)
		<-block
	}))
	defer srv.Close()
	defer close(block)
	ctx, lines := capture()
	e := Endpoint{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Stream: true, StallSec: 2}
	_, _, err := Chat(ctx, e, "", []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "没有任何数据") {
		t.Fatalf("want stall error, got %v", err)
	}
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "POST ") || !strings.Contains(joined, "HTTP 200") || !strings.Contains(joined, "ERROR") {
		t.Fatalf("log missing detail:\n%s", joined)
	}
	if strings.Contains(joined, "Bearer") {
		t.Fatal("key leaked")
	}
}

func TestStreamErrorObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w)
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"quota exceeded\",\"type\":\"insufficient_quota\"}}\n\n")
	}))
	defer srv.Close()
	ctx, _ := capture()
	e := Endpoint{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Stream: true}
	_, _, err := Chat(ctx, e, "", []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("got %v", err)
	}
}

func TestReasoningOnlyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"long thought\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	ctx, _ := capture()
	e := Endpoint{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Stream: true}
	_, _, err := Chat(ctx, e, "", []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "推理") {
		t.Fatalf("got %v", err)
	}
}

func TestAnthropicThinkingStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w)
		for _, d := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", d)
		}
	}))
	defer srv.Close()
	ctx, _ := capture()
	e := Endpoint{Provider: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude", Stream: true}
	out, u, err := Chat(ctx, e, "s", []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil || out != "ok" || u.OutputTokens != 2 {
		t.Fatalf("%q %v %+v", out, err, u)
	}
}
