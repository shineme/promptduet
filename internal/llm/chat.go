package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── hooks: logging + progress, carried on the context ─────────────────────

// Progress is a live snapshot of one request, sent on every phase change and
// as a heartbeat while it runs.
type Progress struct {
	Phase     string `json:"phase"` // connecting | waiting | thinking | streaming | done | error
	ElapsedMs int64  `json:"elapsedMs"`
	IdleMs    int64  `json:"idleMs"`
	Content   int    `json:"content"`   // characters of answer text so far
	Reasoning int    `json:"reasoning"` // characters of reasoning text so far
	Chunks    int    `json:"chunks"`
}

type Hooks struct {
	Log      func(level, msg string)
	Progress func(Progress)
	Think    func(delta string) // reasoning text, if the upstream streams it
}

type hooksKey struct{}

func WithHooks(ctx context.Context, h Hooks) context.Context {
	return context.WithValue(ctx, hooksKey{}, h)
}

func getHooks(ctx context.Context) Hooks {
	h, _ := ctx.Value(hooksKey{}).(Hooks)
	if h.Log == nil {
		h.Log = func(string, string) {}
	}
	if h.Progress == nil {
		h.Progress = func(Progress) {}
	}
	if h.Think == nil {
		h.Think = func(string) {}
	}
	return h
}

// SubHooks prefixes log lines (e.g. "R01 B") and replaces progress/think sinks.
func SubHooks(ctx context.Context, prefix string, progress func(Progress), think func(string)) context.Context {
	parent := getHooks(ctx)
	return WithHooks(ctx, Hooks{
		Log:      func(l, m string) { parent.Log(l, prefix+" "+m) },
		Progress: progress,
		Think:    think,
	})
}

// ── transport ────────────────────────────────────────────────────────────

var transport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   20 * time.Second,
	ExpectContinueTimeout: time.Second,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConnsPerHost:   4,
	ForceAttemptHTTP2:     true,
}
var httpClient = &http.Client{Transport: transport}

// tracker watches one request: idle detection, heartbeat logs, progress.
type tracker struct {
	ctx       context.Context
	h         Hooks
	start     time.Time
	mu        sync.Mutex
	last      time.Time
	phase     string
	content   int
	reasoning int
	chunks    int
	bytes     int64
	stalled   bool
	finished  bool // finish_reason / message_stop seen
	stall     time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

func newTracker(ctx context.Context, e Endpoint, cancel context.CancelFunc) *tracker {
	st := time.Duration(e.StallSec) * time.Second
	if st <= 0 {
		st = 180 * time.Second
	}
	if !e.Stream {
		// A non-streaming body arrives all at once; only the total timeout applies.
		st = 24 * time.Hour
	}
	now := time.Now()
	t := &tracker{ctx: ctx, h: getHooks(ctx), start: now, last: now, phase: "connecting", stall: st, cancel: cancel, done: make(chan struct{})}
	go t.watch()
	return t
}

func (t *tracker) snap() Progress {
	return Progress{Phase: t.phase, ElapsedMs: time.Since(t.start).Milliseconds(), IdleMs: time.Since(t.last).Milliseconds(), Content: t.content, Reasoning: t.reasoning, Chunks: t.chunks}
}

func (t *tracker) setPhase(p string) {
	t.mu.Lock()
	changed := t.phase != p
	t.phase = p
	s := t.snap()
	t.mu.Unlock()
	if changed {
		t.h.Progress(s)
	}
}

func (t *tracker) touch(n int) {
	t.mu.Lock()
	t.last = time.Now()
	t.bytes += int64(n)
	t.mu.Unlock()
}

func (t *tracker) add(content, reasoning string) {
	t.mu.Lock()
	t.content += len([]rune(content))
	t.reasoning += len([]rune(reasoning))
	t.chunks++
	t.mu.Unlock()
	if content != "" {
		t.setPhase("streaming")
	} else if reasoning != "" {
		t.setPhase("thinking")
	}
}

func (t *tracker) watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			t.mu.Lock()
			idle := time.Since(t.last)
			s := t.snap()
			finished := t.finished
			t.mu.Unlock()
			t.h.Progress(s)
			limit := t.stall
			if finished && limit > 15*time.Second {
				// The answer is complete; don't wait long for a missing [DONE].
				limit = 15 * time.Second
			}
			if idle > limit {
				t.mu.Lock()
				t.stalled = true
				t.mu.Unlock()
				t.h.Log("WARN", fmt.Sprintf("%.0f 秒没有收到任何数据，主动断开（阶段：%s）", idle.Seconds(), phaseCN(s.Phase)))
				t.cancel()
				return
			}
			if time.Since(lastBeat) >= 15*time.Second {
				lastBeat = time.Now()
				t.h.Log("INFO", fmt.Sprintf("心跳 · %s · 已用 %ds · 正文 %d 字 · 推理 %d 字 · %d 块 · %d 字节 · 距上次数据 %.0fs",
					phaseCN(s.Phase), s.ElapsedMs/1000, s.Content, s.Reasoning, s.Chunks, t.bytes, idle.Seconds()))
			}
		}
	}
}

func (t *tracker) stop() { close(t.done) }

func phaseCN(p string) string {
	switch p {
	case "connecting":
		return "连接中"
	case "waiting":
		return "等待首字"
	case "thinking":
		return "推理中"
	case "streaming":
		return "输出中"
	}
	return p
}

type trackedBody struct {
	r io.ReadCloser
	t *tracker
}

func (b trackedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.t.touch(n)
	}
	return n, err
}
func (b trackedBody) Close() error { return b.r.Close() }

// ── Chat ─────────────────────────────────────────────────────────────────

// Chat sends a conversation and returns the full reply. When e.Stream is
// true, onDelta is called with each text fragment as it arrives.
func Chat(ctx context.Context, e Endpoint, system string, msgs []Message, onDelta func(string)) (string, Usage, error) {
	h := getHooks(ctx)
	if strings.TrimSpace(e.APIKey) == "" {
		return "", Usage{}, errors.New("缺少 API Key")
	}
	if strings.TrimSpace(e.Model) == "" {
		return "", Usage{}, errors.New("还没有选择模型")
	}
	if e.MaxTokens <= 0 {
		e.MaxTokens = 4096
	}
	total := time.Duration(e.TimeoutSec) * time.Second
	if total <= 0 {
		total = 900 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	t := newTracker(ctx, e, cancel)
	defer t.stop()

	var out string
	var u Usage
	var err error
	if e.provider() == ProviderAnthropic {
		out, u, err = chatAnthropic(cctx, e, system, msgs, onDelta, t)
	} else {
		out, u, err = chatOpenAI(cctx, e, system, msgs, onDelta, t)
	}
	el := time.Since(t.start).Round(100 * time.Millisecond)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		h.Log("INFO", fmt.Sprintf("已手动停止（%s）", el))
		err = ctx.Err()
	case t.stalled && t.finished:
		// Complete answer, upstream just never closed the stream.
		h.Log("WARN", "正文已结束，但上游没有发送结束标记，已按完成处理")
		err = nil
	case t.stalled:
		err = fmt.Errorf("上游 %d 秒没有任何数据，已断开（已用 %s，正文 %d 字，推理 %d 字）。可能是中转站卡住，或模型推理太久；可在 Agent 卡片调大「无数据超时」", int(t.stall.Seconds()), el, t.content, t.reasoning)
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("单次请求超过 %d 秒仍未完成，已断开（正文 %d 字，推理 %d 字）。可调大「请求超时」或调小最大输出", int(total.Seconds()), t.content, t.reasoning)
	}
	if err == nil && strings.TrimSpace(out) == "" {
		if t.reasoning > 0 {
			err = fmt.Errorf("模型只输出了推理内容（%d 字），没有正文。常见原因是最大输出被推理用完", t.reasoning)
		} else {
			err = errors.New("上游返回了空内容")
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		h.Log("ERROR", fmt.Sprintf("失败（%s）：%v", el, err))
		t.setPhase("error")
	} else if err == nil {
		h.Log("INFO", fmt.Sprintf("完成 · %s · 正文 %d 字 · 推理 %d 字 · tokens 入 %d / 出 %d", el, len([]rune(out)), t.reasoning, u.InputTokens, u.OutputTokens))
		t.setPhase("done")
	}
	return out, u, err
}

func describePayload(url string, payload map[string]any, body []byte) string {
	keys := []string{}
	for _, k := range []string{"model", "stream", "max_tokens", "max_completion_tokens", "temperature"} {
		if v, ok := payload[k]; ok {
			keys = append(keys, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return fmt.Sprintf("POST %s · %s · 请求体 %d 字节", url, strings.Join(keys, " "), len(body))
}

func post(ctx context.Context, e Endpoint, url string, payload map[string]any, t *tracker) (*http.Response, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	t.h.Log("INFO", describePayload(url, payload, buf))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	e.setHeaders(req)
	if e.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	t.setPhase("connecting")
	t0 := time.Now()
	// Headers usually come fast; until they do, count the wait as idle time too.
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("连接失败：%w", err)
	}
	t.touch(0)
	t.h.Log("INFO", fmt.Sprintf("响应 HTTP %d · %s · 首包 %s%s", resp.StatusCode, resp.Header.Get("Content-Type"), time.Since(t0).Round(10*time.Millisecond), reqID(resp)))
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		t.h.Log("ERROR", "错误响应体："+snippet(body))
		return nil, upstreamError(resp.StatusCode, body)
	}
	t.setPhase("waiting")
	resp.Body = trackedBody{resp.Body, t}
	return resp, nil
}

func reqID(r *http.Response) string {
	for _, k := range []string{"X-Request-Id", "Request-Id", "X-Oneapi-Request-Id", "Cf-Ray"} {
		if v := r.Header.Get(k); v != "" {
			return " · " + k + "=" + v
		}
	}
	return ""
}

func isEventStream(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream")
}

// ── OpenAI-compatible ────────────────────────────────────────────────────

func chatOpenAI(ctx context.Context, e Endpoint, system string, msgs []Message, onDelta func(string), t *tracker) (string, Usage, error) {
	all := make([]Message, 0, len(msgs)+1)
	if strings.TrimSpace(system) != "" {
		all = append(all, Message{Role: "system", Content: system})
	}
	all = append(all, msgs...)
	payload := map[string]any{"model": e.Model, "messages": all, "stream": e.Stream}
	lm := strings.ToLower(e.Model)
	if isOSeries(lm) || strings.HasPrefix(lm, "gpt-5") {
		payload["max_completion_tokens"] = e.MaxTokens
	} else {
		payload["max_tokens"] = e.MaxTokens
	}
	if Lookup(ProviderOpenAI, lm, nil).Temperature {
		payload["temperature"] = e.Temperature
	}
	if e.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	resp, err := post(ctx, e, e.base()+"/chat/completions", payload, t)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	if !e.Stream || !isEventStream(resp) {
		if e.Stream {
			t.h.Log("WARN", "请求了流式，但上游返回的不是 event-stream，按普通响应读取")
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if rerr != nil {
			return "", Usage{}, rerr
		}
		var r struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			// Maybe it is SSE after all, sent with the wrong content type.
			if bytes.Contains(body, []byte("data:")) {
				t.h.Log("WARN", "响应体看起来是 SSE，尝试按流解析")
				return parseOpenAIStream(bytes.NewReader(body), onDelta, t)
			}
			t.h.Log("ERROR", "无法解析的响应体："+snippet(body))
			return "", Usage{}, fmt.Errorf("无法解析返回：%s", snippet(body))
		}
		if r.Error != nil {
			return "", Usage{}, upstreamError(200, body)
		}
		if len(r.Choices) == 0 {
			t.h.Log("ERROR", "choices 为空："+snippet(body))
			return "", Usage{}, fmt.Errorf("返回为空：%s", snippet(body))
		}
		c := r.Choices[0]
		think := c.Message.ReasoningContent + c.Message.Reasoning
		if think != "" {
			t.h.Think(think)
		}
		t.add(c.Message.Content, think)
		t.h.Log("INFO", "finish_reason="+c.FinishReason)
		if c.FinishReason == "length" {
			t.h.Log("WARN", "输出被最大输出长度截断")
		}
		if onDelta != nil && c.Message.Content != "" {
			onDelta(c.Message.Content)
		}
		return c.Message.Content, Usage{r.Usage.Prompt, r.Usage.Completion}, nil
	}
	return parseOpenAIStream(resp.Body, onDelta, t)
}

func parseOpenAIStream(r io.Reader, onDelta func(string), t *tracker) (string, Usage, error) {
	var sb strings.Builder
	var usage Usage
	var streamErr error
	bad := 0
	sawDone := false
	err := readSSE(r, func(event, data string) bool {
		if data == "[DONE]" {
			sawDone = true
			return false
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			bad++
			if bad <= 3 {
				t.h.Log("WARN", "无法解析的数据行："+snippet([]byte(data)))
			}
			return true
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			streamErr = upstreamError(200, []byte(`{"error":`+string(chunk.Error)+`}`))
			t.h.Log("ERROR", "流中返回错误："+snippet(chunk.Error))
			return false
		}
		if chunk.Usage != nil {
			usage = Usage{chunk.Usage.Prompt, chunk.Usage.Completion}
		}
		for _, c := range chunk.Choices {
			think := c.Delta.ReasoningContent + c.Delta.Reasoning
			if think != "" {
				t.h.Think(think)
			}
			if c.Delta.Content != "" || think != "" {
				t.add(c.Delta.Content, think)
			}
			if c.Delta.Content != "" {
				sb.WriteString(c.Delta.Content)
				if onDelta != nil {
					onDelta(c.Delta.Content)
				}
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				t.mu.Lock()
				t.finished = true
				t.mu.Unlock()
				t.h.Log("INFO", "finish_reason="+*c.FinishReason)
				if *c.FinishReason == "length" {
					t.h.Log("WARN", "输出被最大输出长度截断")
				}
			}
		}
		return true
	})
	if streamErr != nil {
		return sb.String(), usage, streamErr
	}
	if !sawDone {
		t.h.Log("INFO", "流结束（没有收到 [DONE]，连接已关闭）")
	}
	if bad > 3 {
		t.h.Log("WARN", fmt.Sprintf("共 %d 行数据无法解析", bad))
	}
	return sb.String(), usage, err
}

// ── Anthropic ────────────────────────────────────────────────────────────

func chatAnthropic(ctx context.Context, e Endpoint, system string, msgs []Message, onDelta func(string), t *tracker) (string, Usage, error) {
	clean := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			system = strings.TrimSpace(system + "\n\n" + m.Content)
			continue
		}
		clean = append(clean, m)
	}
	payload := map[string]any{
		"model":       e.Model,
		"max_tokens":  e.MaxTokens,
		"messages":    clean,
		"stream":      e.Stream,
		"temperature": clampF(e.Temperature, 0, 1),
	}
	if strings.TrimSpace(system) != "" {
		payload["system"] = system
	}
	resp, err := post(ctx, e, e.base()+"/v1/messages", payload, t)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	if !e.Stream || !isEventStream(resp) {
		if e.Stream {
			t.h.Log("WARN", "请求了流式，但上游返回的不是 event-stream，按普通响应读取")
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if rerr != nil {
			return "", Usage{}, rerr
		}
		var r struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				In  int `json:"input_tokens"`
				Out int `json:"output_tokens"`
			} `json:"usage"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			if bytes.Contains(body, []byte("data:")) {
				t.h.Log("WARN", "响应体看起来是 SSE，尝试按流解析")
				return parseAnthropicStream(bytes.NewReader(body), onDelta, t)
			}
			t.h.Log("ERROR", "无法解析的响应体："+snippet(body))
			return "", Usage{}, fmt.Errorf("无法解析返回：%s", snippet(body))
		}
		if r.Error != nil {
			return "", Usage{}, upstreamError(200, body)
		}
		var sb, th strings.Builder
		for _, c := range r.Content {
			switch c.Type {
			case "text":
				sb.WriteString(c.Text)
			case "thinking":
				th.WriteString(c.Thinking)
			}
		}
		if th.Len() > 0 {
			t.h.Think(th.String())
		}
		t.add(sb.String(), th.String())
		t.h.Log("INFO", "stop_reason="+r.StopReason)
		if r.StopReason == "max_tokens" {
			t.h.Log("WARN", "输出被最大输出长度截断")
		}
		if onDelta != nil && sb.Len() > 0 {
			onDelta(sb.String())
		}
		return sb.String(), Usage{r.Usage.In, r.Usage.Out}, nil
	}
	return parseAnthropicStream(resp.Body, onDelta, t)
}

func parseAnthropicStream(r io.Reader, onDelta func(string), t *tracker) (string, Usage, error) {
	var sb strings.Builder
	var usage Usage
	var streamErr error
	bad := 0
	err := readSSE(r, func(event, data string) bool {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				Thinking   string `json:"thinking"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					In int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				Out int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			bad++
			if bad <= 3 {
				t.h.Log("WARN", "无法解析的数据行："+snippet([]byte(data)))
			}
			return true
		}
		switch ev.Type {
		case "message_start":
			usage.InputTokens = ev.Message.Usage.In
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					sb.WriteString(ev.Delta.Text)
					t.add(ev.Delta.Text, "")
					if onDelta != nil {
						onDelta(ev.Delta.Text)
					}
				}
			case "thinking_delta":
				if ev.Delta.Thinking != "" {
					t.h.Think(ev.Delta.Thinking)
					t.add("", ev.Delta.Thinking)
				}
			}
		case "message_delta":
			usage.OutputTokens = ev.Usage.Out
			if ev.Delta.StopReason != "" {
				t.mu.Lock()
				t.finished = true
				t.mu.Unlock()
				t.h.Log("INFO", "stop_reason="+ev.Delta.StopReason)
				if ev.Delta.StopReason == "max_tokens" {
					t.h.Log("WARN", "输出被最大输出长度截断")
				}
			}
		case "message_stop":
			return false
		case "error":
			streamErr = fmt.Errorf("上游错误（%s）：%s", ev.Error.Type, ev.Error.Message)
			t.h.Log("ERROR", "流中返回错误："+snippet([]byte(data)))
			return false
		}
		return true
	})
	if streamErr != nil {
		return sb.String(), usage, streamErr
	}
	return sb.String(), usage, err
}

// ── SSE ──────────────────────────────────────────────────────────────────

// readSSE parses a server-sent event stream. It is lenient: a data line that
// is already a complete JSON value (or [DONE]) is dispatched at once, so
// relays that separate events with a single newline still stream.
func readSSE(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	var event string
	var data strings.Builder
	flush := func() bool {
		if data.Len() == 0 {
			event = ""
			return true
		}
		d := data.String()
		data.Reset()
		ok := fn(event, d)
		event = ""
		return ok
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line[5:], " ")
			if data.Len() == 0 && (d == "[DONE]" || json.Valid([]byte(d))) {
				data.WriteString(d)
				if !flush() {
					return nil
				}
				continue
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(d)
		case strings.HasPrefix(line, "{") && json.Valid([]byte(line)):
			// bare JSON lines (NDJSON-style relays)
			data.WriteString(line)
			if !flush() {
				return nil
			}
		}
	}
	flush()
	return sc.Err()
}

// Logf writes one line through the context's logger.
func Logf(ctx context.Context, level, format string, args ...any) {
	getHooks(ctx).Log(level, fmt.Sprintf(format, args...))
}
