# PromptDuet · 提示词对弈

Go + Wails v2 的 Windows 桌面程序。A（导演）写 system prompt 和用户需求发给 B（演员），A 给 B 的回答打分、指出问题、改写 system prompt，一直循环到达标分或跑满轮数。

## 运行
- 双击 `PromptDuet.exe`。需要 WebView2 运行时（Win10/11 一般自带）。
- 配置和存档放在 `%AppData%\PromptDuet\`（config.json 里有 API Key 明文，权限 0600）。

## 功能
- A、B 各自选 OpenAI 兼容或 Claude 协议，填接口地址、Key，点「拉取」从上游 `/models` 或 `/v1/models` 拿模型列表（可搜索、方向键选择，也可以手动输入）。
- 温度、最大输出、流式开关都能单独设。推理模型（o1/o3/o4/gpt-5）会自动改用 max_completion_tokens，不发 temperature。
- 「拒绝即重来」（评判标准栏右上角，默认开）：B 明确拒绝、拿安全或合规当理由推脱、只给大纲或片段、只反问不交付时，A 判定为未执行（executed=false），分数最高 3 分，这一轮不算达标；A 会针对拒绝原因改写 System Prompt，补上角色、正当使用场景、完整交付要求等，而且每轮换一种思路。如果 A 漏掉 executed 字段，或者 B 的回答很短又带着“抱歉／无法”之类的措辞，程序会自动按拒绝处理。
- 「A 起草」：System Prompt 留空时让 A 先写一份。
- 对弈台：实时流式显示 B 的回答和 A 的评判，分数曲线，每轮 Prompt 差异对比（差异 / 改写后 / 本轮所用），收官后可复制最佳 Prompt、带回工作台、导出 Markdown。
- 档案：所有对弈自动存档（不存 Key），按住 0.9 秒删除。
- 快捷键：Ctrl+Enter 开始/停止，Ctrl+1/2/3 切换视图，对弈台里 ↑↓ 或 j/k 切换轮次。

## 构建
```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production -ldflags "-H windowsgui -s -w" -o build/PromptDuet.exe .
```
图标和 DPI 清单在 `rsrc_windows_amd64.syso`（用 `go-winres make --in winres/winres.json` 重新生成）。前端是 `frontend/dist` 里的纯 HTML/CSS/JS，没有构建步骤；`dev/mock.js` + `dev/shoot.py` 用来在浏览器里模拟后端做设计走查。
测试：`go test ./internal/...`

## 设置保存
- 位置：`%AppData%\PromptDuet\config.json`，重启后自动读回。每次修改约 0.3 秒后写入，写法是先写临时文件再替换，旧版本保留为 `config.json.bak`。主文件损坏时会自动读备份。
- 关闭窗口（包括关闭按钮、Alt+F4、任务栏关闭）前，会先把还没写入的修改保存下来再退出。
- 如果启动时没能读到配置，本次运行不会覆盖原文件，并会提示。
- 写入失败（比如目录没有权限）时会弹错误提示，不会静默丢失。

## 模型参数自动获取
- 拉取模型列表时，如果上游返回了参数字段（OpenRouter、LiteLLM、Gemini 风格的 `max_completion_tokens`、`context_length`、`outputTokenLimit`、`supported_parameters` 等），就直接采用，提示点为实心绿点。
- OpenAI 官方、Anthropic 和大多数中转站只返回模型 id。这种情况下按内置参考表（Claude、GPT、o 系列、DeepSeek、Gemini 的官方输出上限）填写，提示点为空心圆。表里没有的模型，保持你手动填的值。
- 选中模型后，最大输出会自动设为该模型的上限，之后仍可手动修改，但不能超过上限。
- 温度：没有任何上游会返回推荐温度，所以不自动填。A 默认 0.3（评分要稳定），B 默认 0.7。Claude 的温度上限是 1，其他模型是 2。o 系列、gpt-5、deepseek-reasoner 这类推理模型不接受温度参数，温度滑块会变灰，发送时自动省略。

## 运行日志与超时
- 每一次请求都会写入 `%AppData%\PromptDuet\logs\YYYY-MM-DD.log`。记录的内容有：请求地址、模型和参数、请求体大小、HTTP 状态、Content-Type、首包耗时、request-id、每 15 秒一次的心跳（阶段、正文和推理字数、块数、字节数、距上次收到数据多久）、finish_reason / stop_reason、是否被截断、token 用量、上游的完整错误响应体。Key 只记前 3 位和后 4 位。
- 标题栏的「日志」按钮（快捷键 L）打开实时日志面板，顶部显示当前请求的阶段和空闲时长，可以按级别筛选、复制，或打开日志文件夹。出错时面板会自动打开。
- 超时：每个 Agent 都可以设置「请求超时」（默认 900 秒）和「无数据超时」（默认 180 秒，流式时上游完全没有数据就断开）。正文已经结束、但上游迟迟不发结束标记时，15 秒后按完成处理。
- 会显示推理模型的思考过程（reasoning_content / reasoning / Claude thinking_delta），不会再出现长时间没有任何动静的情况。只有推理、没有正文时会明确报错。
- 兼容只用单个换行分隔 SSE 事件、不发 [DONE]、流里夹带 error 对象，以及 Content-Type 写错的中转站。

## 每轮怎么改 Prompt（工作台字段 01 下方）
- **接着上一轮改**（默认）：A 不再整篇重写，而是给出 edits（替换、插入、追加），程序把这些修改应用到上一轮的 Prompt 上。没被点名的原文一字不动，所以第 N 轮的 Prompt 一定是从第 N-1 轮改出来的。
- **从最高分接着改**：本轮分数如果没超过历史最高，下一版就回到最高分那一版的 Prompt 上接着改，避免越改越差。
- **允许整篇重写**：旧行为，A 每轮直接给出完整的新 Prompt。
- 找不到原文的 edit 会被跳过，并在日志和「System Prompt 演变」里标出。一条都没能应用时，如果 A 给了完整改写就用它，否则沿用上一版。差异视图对比的是实际的基准版本。
