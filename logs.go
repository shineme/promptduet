package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"promptduet/internal/llm"
)

// LogLine is one entry, written to %AppData%\PromptDuet\logs\YYYY-MM-DD.log
// and pushed live to the UI on the "log" event.
type LogLine struct {
	T     string `json:"t"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type logger struct {
	mu  sync.Mutex
	dir string
	day string
	f   *os.File
}

func (l *logger) write(line LogLine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if l.f == nil || l.day != day {
		if l.f != nil {
			l.f.Close()
		}
		_ = os.MkdirAll(l.dir, 0o700)
		f, err := os.OpenFile(filepath.Join(l.dir, day+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			l.f = nil
			return
		}
		l.f, l.day = f, day
	}
	fmt.Fprintf(l.f, "%s [%s] %s\n", line.T, line.Level, strings.ReplaceAll(line.Msg, "\n", "⏎"))
}

func (a *App) log(level, msg string) {
	line := LogLine{T: time.Now().Format("2006-01-02 15:04:05.000"), Level: level, Msg: msg}
	if a.logs != nil {
		a.logs.write(line)
	}
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "log", line)
	}
}

func (a *App) logCtx(ctx context.Context) context.Context {
	return llm.WithHooks(ctx, llm.Hooks{Log: a.log})
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return "（空）"
	}
	if len(k) <= 8 {
		return fmt.Sprintf("****（%d 位）", len(k))
	}
	return fmt.Sprintf("%s…%s（%d 位）", k[:3], k[len(k)-4:], len(k))
}

func describeEndpoint(role string, e llm.Endpoint) string {
	to, st := e.TimeoutSec, e.StallSec
	if to <= 0 {
		to = 900
	}
	if st <= 0 {
		st = 180
	}
	return fmt.Sprintf("%s · %s · %s · 模型 %s · 流式 %v · 最大输出 %d · 温度 %.1f · 请求超时 %ds · 无数据超时 %ds · Key %s",
		role, e.Provider, e.BaseURL, e.Model, e.Stream, e.MaxTokens, e.Temperature, to, st, maskKey(e.APIKey))
}

// LogDir is where the log files live.
func (a *App) LogDir() string { return filepath.Join(a.dir, "logs") }

// OpenLogDir opens the log folder in Explorer.
func (a *App) OpenLogDir() error {
	dir := a.LogDir()
	_ = os.MkdirAll(dir, 0o700)
	if goruntime.GOOS == "windows" {
		return exec.Command("explorer", dir).Start()
	}
	return exec.Command("xdg-open", dir).Start()
}

// RecentLog returns the last n lines of today's log, for the log drawer.
func (a *App) RecentLog(n int) []LogLine {
	f, err := os.Open(filepath.Join(a.LogDir(), time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		all = append(all, sc.Text())
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]LogLine, 0, len(all))
	for _, s := range all {
		ll := LogLine{Msg: s, Level: "INFO"}
		if len(s) > 24 {
			ll.T = s[:23]
			rest := s[24:]
			if i := strings.Index(rest, "] "); strings.HasPrefix(rest, "[") && i > 0 {
				ll.Level, ll.Msg = rest[1:i], strings.ReplaceAll(rest[i+2:], "⏎", "\n")
			}
		}
		out = append(out, ll)
	}
	return out
}
