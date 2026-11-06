package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"promptduet/internal/duet"
	"promptduet/internal/llm"
)

type Config struct {
	A     llm.Endpoint `json:"a"`
	B     llm.Endpoint `json:"b"`
	Brief duet.Brief   `json:"brief"`
}

type Session struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	CreatedAt  time.Time    `json:"createdAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	A          llm.Endpoint `json:"a"`
	B          llm.Endpoint `json:"b"`
	Brief      duet.Brief   `json:"brief"`
	Rounds     []duet.Round `json:"rounds"`
	Best       int          `json:"best"`
	StopReason string       `json:"stopReason"`
}

type SessionMeta struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"createdAt"`
	Rounds     int       `json:"rounds"`
	BestScore  float64   `json:"bestScore"`
	StopReason string    `json:"stopReason"`
	ModelA     string    `json:"modelA"`
	ModelB     string    `json:"modelB"`
}

type App struct {
	ctx    context.Context
	mu     sync.Mutex
	cancel context.CancelFunc
	dir    string

	cfgMu   sync.Mutex
	models  map[string]llm.ModelInfo
	closing atomic.Bool
	logs    *logger
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	a.dir = filepath.Join(base, "PromptDuet")
	_ = os.MkdirAll(filepath.Join(a.dir, "sessions"), 0o700)
	a.logs = &logger{dir: filepath.Join(a.dir, "logs")}
	a.log("INFO", "PromptDuet 启动 · 配置目录 "+a.dir)
}

func defaultConfig() Config {
	return Config{
		A:     llm.Endpoint{Provider: llm.ProviderAnthropic, BaseURL: llm.DefaultAnthropicBase, Temperature: 0.3, MaxTokens: 4096, Stream: true, TimeoutSec: 900, StallSec: 180},
		B:     llm.Endpoint{Provider: llm.ProviderOpenAI, BaseURL: llm.DefaultOpenAIBase, Temperature: 0.7, MaxTokens: 4096, Stream: true, TimeoutSec: 900, StallSec: 180},
		Brief: duet.Brief{MaxRounds: 10, TargetScore: 9, RequireExecution: true, Evolve: duet.EvolveChain},
	}
}

// GetConfig reads config.json, falling back to config.json.bak if the main
// file is missing or unreadable, so a crash mid-write never wipes settings.
func (a *App) GetConfig() Config {
	for _, name := range []string{"config.json", "config.json.bak"} {
		cfg := defaultConfig()
		b, err := os.ReadFile(filepath.Join(a.dir, name))
		if err != nil {
			continue
		}
		if json.Unmarshal(b, &cfg) == nil {
			return cfg
		}
	}
	return defaultConfig()
}

// ConfigPath tells the UI where settings live.
func (a *App) ConfigPath() string { return filepath.Join(a.dir, "config.json") }

// SaveConfig writes atomically: temp file, fsync, keep the previous file as
// .bak, then rename into place.
func (a *App) SaveConfig(cfg Config) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("无法创建配置目录：%w", err)
	}
	final := filepath.Join(a.dir, "config.json")
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("无法写入配置：%w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("无法写入配置：%w", err)
	}
	_ = f.Sync()
	f.Close()
	if old, err := os.ReadFile(final); err == nil && len(old) > 0 {
		_ = os.WriteFile(final+".bak", old, 0o600)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("无法保存配置：%w", err)
	}
	return nil
}

func (a *App) ListModels(e llm.Endpoint) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
	defer cancel()
	t0 := time.Now()
	list, err := llm.ListModels(ctx, e)
	if err != nil {
		a.log("ERROR", fmt.Sprintf("拉取模型失败 · %s · %s · %v", e.Provider, e.BaseURL, err))
	} else {
		a.log("INFO", fmt.Sprintf("拉取模型 · %s · %s · %d 个 · %s", e.Provider, e.BaseURL, len(list), time.Since(t0).Round(time.Millisecond)))
	}
	if err == nil {
		a.cfgMu.Lock()
		if a.models == nil {
			a.models = map[string]llm.ModelInfo{}
		}
		for _, m := range list {
			a.models[e.BaseURL+"|"+m.ID] = m
		}
		a.cfgMu.Unlock()
	}
	return list, err
}

// ModelInfo returns limits for a model id, typed by hand or picked from the
// list: upstream data from the last fetch if we have it, else the reference table.
func (a *App) ModelInfo(e llm.Endpoint) llm.ModelInfo {
	a.cfgMu.Lock()
	m, ok := a.models[e.BaseURL+"|"+e.Model]
	a.cfgMu.Unlock()
	if ok {
		return m
	}
	return llm.Lookup(e.Provider, e.Model, nil)
}

// beforeClose gives the UI one beat to flush pending settings, then quits.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.closing.Swap(true) {
		return false
	}
	runtime.EventsEmit(ctx, "app_closing")
	go func() {
		time.Sleep(1500 * time.Millisecond)
		runtime.Quit(ctx)
	}()
	return true
}

// DraftPrompt lets A write the first system prompt; text streams on "draft".
func (a *App) DraftPrompt(cfg Config) (string, error) {
	if strings.TrimSpace(cfg.Brief.UserTask) == "" {
		return "", errors.New("先写下用户需求")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		cancel()
		return "", errors.New("对弈进行中")
	}
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel()
	}()
	a.log("INFO", "A 起草 System Prompt · "+describeEndpoint("A", cfg.A))
	return duet.Draft(llm.SubHooks(a.logCtx(ctx), "起草 A", nil, nil), cfg.A, cfg.Brief, func(d string) {
		runtime.EventsEmit(a.ctx, "draft", d)
	})
}

// Start launches a session in the background. Progress arrives on "duet".
func (a *App) Start(cfg Config) (string, error) {
	if strings.TrimSpace(cfg.Brief.UserTask) == "" {
		return "", errors.New("用户需求不能为空")
	}
	if cfg.A.Model == "" || cfg.B.Model == "" {
		return "", errors.New("A 和 B 都需要选择模型")
	}
	a.mu.Lock()
	if a.cancel != nil {
		a.mu.Unlock()
		return "", errors.New("已有任务在运行")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	a.mu.Unlock()
	_ = a.SaveConfig(cfg)

	s := &Session{
		ID:        time.Now().Format("20060102-150405"),
		Title:     titleOf(cfg.Brief.UserTask),
		CreatedAt: time.Now(),
		A:         scrub(cfg.A),
		B:         scrub(cfg.B),
		Brief:     cfg.Brief,
		Best:      -1,
	}
	go func() {
		defer func() {
			a.mu.Lock()
			a.cancel = nil
			a.mu.Unlock()
			cancel()
		}()
		a.log("INFO", fmt.Sprintf("════ 对弈 %s 开始 · 最多 %d 轮 · 达标 %.1f · 拒绝即重来 %v · 改写方式 %s", s.ID, cfg.Brief.MaxRounds, cfg.Brief.TargetScore, cfg.Brief.RequireExecution, cfg.Brief.Evolve))
		a.log("INFO", describeEndpoint("A", cfg.A))
		a.log("INFO", describeEndpoint("B", cfg.B))
		a.log("INFO", fmt.Sprintf("用户需求 %d 字 · 评判标准 %d 字", len([]rune(cfg.Brief.UserTask)), len([]rune(cfg.Brief.Criteria))))
		res := duet.Run(a.logCtx(ctx), cfg.A, cfg.B, cfg.Brief, func(ev duet.Event) {
			if ev.Type == "a_done" {
				if r, ok := ev.Data.(duet.Round); ok {
					s.Rounds = append(s.Rounds, r)
					_ = a.saveSession(s)
				}
			}
			runtime.EventsEmit(a.ctx, "duet", ev)
		})
		s.Rounds, s.Best, s.StopReason = res.Rounds, res.Best, res.StopReason
		s.FinishedAt = time.Now()
		a.log("INFO", fmt.Sprintf("════ 对弈 %s 结束 · %d 轮 · %s · 用时 %s", s.ID, len(res.Rounds), res.StopReason, s.FinishedAt.Sub(s.CreatedAt).Round(time.Second)))
		_ = a.saveSession(s)
		runtime.EventsEmit(a.ctx, "sessions_changed")
	}()
	return s.ID, nil
}

func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) ListSessions() []SessionMeta {
	entries, _ := os.ReadDir(filepath.Join(a.dir, "sessions"))
	out := []SessionMeta{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := a.GetSession(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		m := SessionMeta{ID: s.ID, Title: s.Title, CreatedAt: s.CreatedAt, Rounds: len(s.Rounds), StopReason: s.StopReason, ModelA: s.A.Model, ModelB: s.B.Model}
		if s.Best >= 0 && s.Best < len(s.Rounds) {
			m.BestScore = s.Rounds[s.Best].Judgement.Score
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (a *App) GetSession(id string) (*Session, error) {
	if strings.ContainsAny(id, `/\.`) {
		return nil, errors.New("bad id")
	}
	b, err := os.ReadFile(filepath.Join(a.dir, "sessions", id+".json"))
	if err != nil {
		return nil, err
	}
	var s Session
	return &s, json.Unmarshal(b, &s)
}

func (a *App) DeleteSession(id string) error {
	if strings.ContainsAny(id, `/\.`) {
		return errors.New("bad id")
	}
	return os.Remove(filepath.Join(a.dir, "sessions", id+".json"))
}

// ExportSession writes a Markdown report chosen through a save dialog.
func (a *App) ExportSession(id string) (string, error) {
	s, err := a.GetSession(id)
	if err != nil {
		return "", err
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultFilename: "PromptDuet-" + s.ID + ".md",
		Filters:         []runtime.FileFilter{{DisplayName: "Markdown", Pattern: "*.md"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, []byte(renderMarkdown(s)), 0o644)
}

func (a *App) saveSession(s *Session) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.dir, "sessions", s.ID+".json"), b, 0o600)
}

func scrub(e llm.Endpoint) llm.Endpoint { e.APIKey = ""; return e }

func titleOf(task string) string {
	t := strings.Join(strings.Fields(task), " ")
	r := []rune(t)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return t
}

func renderMarkdown(s *Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PromptDuet · %s\n\n", s.Title)
	fmt.Fprintf(&b, "- 时间：%s\n- A：%s / %s\n- B：%s / %s\n- 结束原因：%s\n\n", s.CreatedAt.Format("2006-01-02 15:04"), s.A.Provider, s.A.Model, s.B.Provider, s.B.Model, s.StopReason)
	if s.Best >= 0 && s.Best < len(s.Rounds) {
		r := s.Rounds[s.Best]
		fmt.Fprintf(&b, "## 最佳 System Prompt（第 %d 轮，%.1f 分）\n\n```\n%s\n```\n\n", r.Index, r.Judgement.Score, r.SystemPrompt)
	}
	fmt.Fprintf(&b, "## 用户需求\n\n%s\n\n", s.Brief.UserTask)
	for _, r := range s.Rounds {
		state := ""
		if r.Judgement.Refused {
			state = " · ⚠ B 拒绝/未执行"
		}
		fmt.Fprintf(&b, "---\n\n## 第 %d 轮 · %.1f 分%s\n\n**总评**：%s\n\n", r.Index, r.Judgement.Score, state, r.Judgement.Verdict)
		fmt.Fprintf(&b, "### System Prompt\n\n```\n%s\n```\n\n### B 的回答\n\n%s\n\n", r.SystemPrompt, r.Output)
		list := func(h string, xs []string) {
			if len(xs) == 0 {
				return
			}
			fmt.Fprintf(&b, "### %s\n\n", h)
			for _, x := range xs {
				fmt.Fprintf(&b, "- %s\n", x)
			}
			b.WriteString("\n")
		}
		list("优点", r.Judgement.Strengths)
		list("问题", r.Judgement.Issues)
		list("改动", r.Judgement.ChangeNotes)
	}
	return b.String()
}
