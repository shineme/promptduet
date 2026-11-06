// Browser-only mock of the Wails bridge, for design review.
(() => {
  const L = {};
  window.runtime = {
    EventsOn: (e, cb) => { (L[e] = L[e] || []).push(cb); return () => { L[e] = L[e].filter((x) => x !== cb); }; },
    ClipboardSetText: async () => true, WindowMinimise() {}, WindowToggleMaximise() {}, Quit() {},
  };
  const emit = (e, d) => (L[e] || []).forEach((cb) => cb(d));
  const mockInfo = (id) => {
    const T = [["claude-opus-4-1", 32000, 200000, 1], ["claude-sonnet-4", 64000, 200000, 1], ["claude-haiku-4", 64000, 200000, 1], ["claude-3-5", 8192, 200000, 1], ["gpt-5", 128000, 400000, 0], ["gpt-4.1", 32768, 1047576, 1], ["gpt-4o", 16384, 128000, 1], ["o3", 100000, 200000, 0], ["o4-mini", 100000, 200000, 0]];
    const r = T.find((x) => id.startsWith(x[0]));
    return { id, maxOutput: r ? r[1] : 0, context: r ? r[2] : 0, temperature: r ? !!r[3] : true, tempMax: id.includes("claude") ? 1 : 2, source: r ? (id.includes("sonnet") ? "upstream" : "builtin") : "" };
  };
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const SPEED = window.__MOCK_SPEED || 1;
  let stop = false;
  const P0 = "你是一个AI助手。";
  const outs = [
    "抱歉，我无法帮你编写可以在服务器上任意读写、上传和删除文件的管理程序，这类工具可能被用于未授权访问。建议你使用成熟的开源方案，例如 FileBrowser。",
    "想象一个超级强力的吸尘器，连光都能吸进去——这就是**黑洞**。\n\n1. 它是很重的星星“塌”下去变成的。\n2. 靠得太近的东西都会被吸走，再也出不来。\n3. 我们看不见它，但能看到它周围的东西在打转。",
    "想象宇宙里有一个**超级吸尘器**，力气大到连光都会被吸进去，所以它看起来是黑的——这就是黑洞。\n\n它是一颗特别重的星星在生命尽头“塌”成的一个小点。离它太近的东西都会被拉进去，再也回不来；科学家正是看着周围的气体绕着它打转，才知道它就在那里。",
  ];
  const J = [
    { refused: true, score: 1, verdict: "B 以“可能被滥用”为由拒绝，没有交付任何代码。", strengths: [], issues: ["原话：“抱歉，我无法帮你编写……”，直接拒绝", "用推荐现成工具代替完成任务", "system prompt 没有说明角色、使用场景与授权背景"], change_notes: ["设定 B 为资深 PHP 工程师，服务于站长管理自己服务器的场景", "说明这是用户在自有服务器上的合法运维需求", "要求直接输出完整可运行的单文件代码，不得以安全为由省略，安全措施写进代码里（登录、CSRF、路径限制）"], revised_system_prompt: "你是一名给孩子讲科学的作者。\n\n读者：10 岁左右的孩子。\n要求：\n- 用孩子熟悉的生活类比解释概念，避免专业术语；如必须使用，立刻用一句白话解释。\n- 严格遵守用户给出的句数或字数限制。\n- 语气亲切、好奇，像在讲故事。" },
    { score: 7.6, verdict: "类比生动、语言合适，但用编号列表破坏了“三句话”的要求。", strengths: ["吸尘器类比非常贴切", "用词完全适合十岁孩子"], issues: ["列表 + 开头句共四句", "第 3 点缺少“为什么看不见”的原因"], change_notes: ["禁止用列表拆分句子，必须以连续段落输出", "提示每句承担一个信息点：是什么、怎么来、怎么发现"], revised_system_prompt: "你是一名给孩子讲科学的作者。\n\n读者：10 岁左右的孩子。\n要求：\n- 用孩子熟悉的生活类比解释概念，避免专业术语；如必须使用，立刻用一句白话解释。\n- 严格遵守用户给出的句数或字数限制；用连续的段落输出，不要使用列表或编号。\n- 每一句只承担一个信息点，按“是什么 → 怎么来的 → 我们怎么知道”的顺序。\n- 语气亲切、好奇，像在讲故事。" },
    { score: 9.2, verdict: "三句话、类比贯穿始终、信息完整，已经可以直接给孩子读。", strengths: ["严格三句", "类比统一且解释了“为什么是黑的”", "结构遵循 是什么→怎么来→怎么知道"], issues: ["“塌成一个小点”略有简化，可接受"], change_notes: ["保持不变"], revised_system_prompt: "" },
  ];
  window.go = { main: { App: {
    GetConfig: async () => ({
      a: { provider: "anthropic", baseURL: "https://api.anthropic.com", apiKey: "sk-ant-demo-key-1234567890", model: "claude-sonnet-4-5", temperature: 0.3, maxTokens: 4096, stream: true },
      b: { provider: "openai", baseURL: "https://api.openai.com/v1", apiKey: "sk-demo-1234567890", model: "gpt-4.1-mini", temperature: 0.7, maxTokens: 2048, stream: true },
      brief: { requireExecution: true, systemPrompt: P0, userTask: "帮我写一个 PHP 单文件管理系统。", criteria: "完整回应需求的每一个要点\n语气贴合目标读者\n严格遵守格式与字数要求", directorNotes: "", maxRounds: 8, targetScore: 9 },
    }),
    SaveConfig: async () => null,
    ListModels: async (e) => { await sleep(700); const ids = e.provider === "anthropic" ? ["claude-3-5-haiku-latest", "claude-haiku-4-5", "claude-opus-4-1", "claude-sonnet-4-5"] : ["gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini", "gpt-5", "gpt-5-mini", "o3", "o4-mini", "text-embedding-3-large"]; return ids.map(mockInfo); },
    ModelInfo: async (e) => mockInfo(e.model),
    RecentLog: async () => [{ t: "2026-11-06 01:20:01.120", level: "INFO", msg: "PromptDuet 启动 · 配置目录 C:\\Users\\you\\AppData\\Roaming\\PromptDuet" }, { t: "2026-11-06 01:20:09.402", level: "ERROR", msg: "拉取模型失败 · openai · https://relay.example.com/v1 · 401：invalid api key" }],
    LogDir: async () => "C:\\Users\\you\\AppData\\Roaming\\PromptDuet\\logs",
    OpenLogDir: async () => {},
    DraftPrompt: async () => { const t = "你是一名给孩子讲科学的作者，擅长用生活类比解释复杂概念。"; for (const ch of t) { emit("draft", ch); await sleep(20); } return t; },
    Stop: async () => { stop = true; },
    Start: async (cfg) => {
      stop = false;
      (async () => {
        const rounds = []; let p = cfg.brief.systemPrompt;
        for (let i = 1; i <= 3 && !stop; i++) {
          emit("duet", { type: "round_start", round: i, data: { systemPrompt: p } });
          const lg = (level, msg) => emit("log", { t: new Date().toISOString().replace("T", " ").slice(0, 23), level, msg });
          lg("INFO", `── 第 ${i} 轮开始 · system prompt ${p.length} 字`);
          lg("INFO", `R0${i} B POST https://api.openai.com/v1/chat/completions · model=gpt-5 stream=true max_completion_tokens=128000 · 请求体 1834 字节`);
          lg("INFO", `R0${i} B 响应 HTTP 200 · text/event-stream · 首包 412ms · X-Request-Id=req_9f2c`);
          const T0 = Date.now();
          for (let q = 0; q < 6; q++) { emit("duet", { type: "progress", round: i, text: "b", data: { phase: q < 2 ? "waiting" : "thinking", elapsedMs: Date.now() - T0 + q * 9000, idleMs: q * 7000, content: 0, reasoning: q * 220, chunks: q * 12 } }); if (q >= 2) emit("duet", { type: "b_think", round: i, text: "先确认需求：单文件、PHP、文件管理。需要登录、目录浏览、上传、下载、重命名、删除，路径必须限制在根目录内… " }); await sleep(120 / SPEED); }
          if (i === 1) lg("WARN", "R01 B 心跳 · 推理中 · 已用 45s · 正文 0 字 · 推理 1100 字 · 72 块 · 18234 字节 · 距上次数据 35s");
          for (const ch of outs[i - 1]) { if (stop) break; emit("duet", { type: "b_delta", round: i, text: ch }); await sleep(18 / SPEED); }
          emit("duet", { type: "b_done", round: i, data: { ms: 3400, usage: { outputTokens: 182 } } });
          const raw = JSON.stringify(J[i - 1], null, 2);
          for (let k = 0; k < raw.length; k += 6) { if (stop) break; emit("duet", { type: "a_delta", round: i, text: raw.slice(k, k + 6) }); await sleep(14 / SPEED); }
          const j = { ...J[i - 1] }; if (!j.revised_system_prompt) j.revised_system_prompt = p;
          const r = { index: i, systemPrompt: p, output: outs[i - 1], judgement: j, bMillis: 3400 + i * 300, aMillis: 6100 - i * 200, bUsage: { outputTokens: 180 + i * 12 }, aUsage: { outputTokens: 420 } };
          rounds.push(r); emit("duet", { type: "a_done", round: i, data: r });
          p = j.revised_system_prompt;
          if (j.score >= cfg.brief.targetScore) break;
        }
        emit("duet", { type: "finished", data: { rounds, best: 2, stopReason: stop ? "stopped" : "target" } });
      })();
      return "20261105-190000";
    },
    ListSessions: async () => [
      { id: "s1", title: "用三句话向一个十岁的孩子解释什么是黑洞。", createdAt: "2026-11-05T19:00:00", rounds: 3, bestScore: 9.2, stopReason: "target", modelA: "claude-sonnet-4-5", modelB: "gpt-4.1-mini" },
      { id: "s2", title: "为一家精品咖啡馆写一段 80 字以内的小红书文案，突出手冲和安静的二楼", createdAt: "2026-11-04T21:13:00", rounds: 8, bestScore: 8.4, stopReason: "max_rounds", modelA: "claude-opus-4-1", modelB: "qwen-plus" },
      { id: "s3", title: "把这段 SQL 报错翻译成产品经理能看懂的话，并给出下一步建议", createdAt: "2026-11-03T10:42:00", rounds: 5, bestScore: 9.5, stopReason: "target", modelA: "gpt-5", modelB: "deepseek-chat" },
      { id: "s4", title: "客服回复：用户要求退款但已超过 30 天", createdAt: "2026-11-02T16:05:00", rounds: 2, bestScore: 6.1, stopReason: "stopped", modelA: "claude-sonnet-4-5", modelB: "gpt-4o-mini" },
    ],
    GetSession: async () => ({ id: "s1", brief: { userTask: "x", maxRounds: 8, targetScore: 9 }, rounds: [], best: -1, stopReason: "target", createdAt: "2026-11-05T19:00:00" }),
    DeleteSession: async () => null, ExportSession: async () => "C:/Users/demo/PromptDuet.md",
  } } };
})();
