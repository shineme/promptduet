/* PromptDuet — front end (vanilla, no build step) */
(() => {
"use strict";

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
const DEF = { openai: "https://api.openai.com/v1", anthropic: "https://api.anthropic.com" };
const AGENTS = {
  a: { letter: "A", role: "Director", desc: "导演 · 出题、评判、改写 Prompt" },
  b: { letter: "B", role: "Performer", desc: "演员 · 按 System Prompt 作答" },
};

/* ───────── bridge ───────── */
const api = (name, ...args) => {
  const fn = window.go && window.go.main && window.go.main.App && window.go.main.App[name];
  if (!fn) return Promise.reject(new Error("后端未就绪"));
  return fn(...args);
};
const on = (evt, cb) => window.runtime && window.runtime.EventsOn(evt, cb);
const copyText = async (t) => {
  try { if (window.runtime && window.runtime.ClipboardSetText) await window.runtime.ClipboardSetText(t); else await navigator.clipboard.writeText(t); return true; }
  catch { return false; }
};

/* ───────── state ───────── */
const S = {
  cfg: null,
  models: { a: [], b: [] }, minfo: { a: {}, b: {} }, cfgLoaded: false,
  view: "studio",
  session: null,          // { id, brief, rounds, live, best, stopReason, running, t0, t1 }
  sel: null,              // selected round index (1-based) or null = follow live
  diffMode: "diff",
};

/* ───────── utils ───────── */
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const pad2 = (n) => String(n).padStart(2, "0");
const fmt1 = (n) => (Math.round(n * 10) / 10).toFixed(1);
const debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };
const raf = (fn) => { let q = false; return () => { if (q) return; q = true; requestAnimationFrame(() => { q = false; fn(); }); }; };
const errMsg = (e) => (e && (e.message || e)) + "";

function toast(msg, kind = "ok", ms = 3200) {
  const el = document.createElement("div");
  el.className = "toast" + (kind === "err" ? " err" : "");
  el.innerHTML = `<i></i><span>${esc(msg)}</span>`;
  $("#toasts").appendChild(el);
  setTimeout(() => { el.classList.add("out"); setTimeout(() => el.remove(), 380); }, ms);
}

function countTo(el, to, { dur = 900, dec = 1 } = {}) {
  const from = parseFloat(el.dataset.v || "0") || 0;
  el.dataset.v = to;
  const id = (el._anim = (el._anim || 0) + 1);
  if (reduce || from === to) { el.textContent = to.toFixed(dec); return; }
  const t0 = performance.now();
  const step = (t) => {
    if (el._anim !== id) return;
    const k = Math.min(1, (t - t0) / dur), e = 1 - Math.pow(1 - k, 4);
    el.textContent = (from + (to - from) * e).toFixed(dec);
    if (k < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

function setRangeFill(r) {
  const p = ((r.value - r.min) / (r.max - r.min)) * 100;
  r.style.setProperty("--p", p + "%");
}

function autosize(ta) {
  ta.style.height = "auto";
  ta.style.height = Math.min(ta.scrollHeight + 2, 640) + "px";
  ta.style.overflowY = ta.scrollHeight > 640 ? "auto" : "hidden";
}

/* sliding ink for segmented controls & nav */
const EVOLVE_DESC = {
  chain: "A 在上一轮的 Prompt 上逐条修改，没点名的原文一字不动。",
  best: "上一轮退步了，就回到目前最高分的版本接着改。",
  free: "A 每轮可以整篇重写，变化大，也容易丢掉已经有效的写法。",
};
function setEvolve(mode, animate) {
  const seg = $("#f-evolve");
  $$("button", seg).forEach((b) => b.setAttribute("aria-checked", String(b.dataset.evolve === mode)));
  $("#evolve-desc").textContent = EVOLVE_DESC[mode] || "";
  const ink = $(".seg-ink", seg);
  if (!animate) ink.style.transition = "none";
  requestAnimationFrame(() => { placeInk(seg, `[data-evolve="${mode}"]`); if (!animate) requestAnimationFrame(() => (ink.style.transition = "")); });
}
function syncUntil() {
  const on = !!S.cfg.brief.untilDone, o = $("#o-rounds");
  o.closest(".ctl").classList.toggle("is-until", on);
  o.textContent = on ? "∞" : S.cfg.brief.maxRounds || 10;
}
function placeInk(container, sel) {
  const ink = container.querySelector(".seg-ink, .nav-ink");
  const btn = container.querySelector(sel);
  if (!ink || !btn) return;
  ink.style.width = btn.offsetWidth + "px";
  ink.style.transform = `translateX(${btn.offsetLeft}px)`;
}

/* pointer-follow glow */
function glow(el) {
  el.addEventListener("pointermove", (e) => {
    const r = el.getBoundingClientRect();
    el.style.setProperty("--mx", e.clientX - r.left + "px");
    el.style.setProperty("--my", e.clientY - r.top + "px");
  });
}

/* ───────── markdown (small, safe) ───────── */
function inline(s) {
  return esc(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>")
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, "<u>$1</u>");
}
function md(src) {
  const lines = String(src || "").replace(/\r/g, "").split("\n");
  let out = "", i = 0;
  while (i < lines.length) {
    let l = lines[i];
    if (/^```/.test(l)) {
      const buf = []; i++;
      while (i < lines.length && !/^```/.test(lines[i])) buf.push(lines[i++]);
      i++; out += `<pre><code>${esc(buf.join("\n"))}</code></pre>`; continue;
    }
    if (/^\s*$/.test(l)) { i++; continue; }
    let m;
    if ((m = l.match(/^(#{1,4})\s+(.*)$/))) { out += `<h${m[1].length}>${inline(m[2])}</h${m[1].length}>`; i++; continue; }
    if (/^(-{3,}|\*{3,})\s*$/.test(l)) { out += "<hr>"; i++; continue; }
    if (/^>\s?/.test(l)) { const b = []; while (i < lines.length && /^>\s?/.test(lines[i])) b.push(lines[i++].replace(/^>\s?/, "")); out += `<blockquote>${inline(b.join(" "))}</blockquote>`; continue; }
    if (/^\|.*\|\s*$/.test(l) && i + 1 < lines.length && /^\|?[\s:-]+\|/.test(lines[i + 1])) {
      const row = (r) => r.trim().replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
      const head = row(l); i += 2; let body = "";
      while (i < lines.length && /^\|.*\|\s*$/.test(lines[i])) body += "<tr>" + row(lines[i++]).map((c) => `<td>${inline(c)}</td>`).join("") + "</tr>";
      out += `<table><thead><tr>${head.map((c) => `<th>${inline(c)}</th>`).join("")}</tr></thead><tbody>${body}</tbody></table>`; continue;
    }
    if (/^\s*([-*+]|\d+[.)])\s+/.test(l)) {
      const ordered = /^\s*\d/.test(l); const items = [];
      while (i < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*([-*+]|\d+[.)])\s+/, ""));
      out += `<${ordered ? "ol" : "ul"}>${items.map((x) => `<li>${inline(x)}</li>`).join("")}</${ordered ? "ol" : "ul"}>`; continue;
    }
    const p = [];
    while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^(#{1,4}\s|```|>|\s*([-*+]|\d+[.)])\s)/.test(lines[i])) p.push(lines[i++]);
    if (!p.length) p.push(lines[i++]);
    out += `<p>${p.map(inline).join("<br>")}</p>`;
  }
  return out;
}

/* ───────── diff (token LCS) ───────── */
function tokens(s) { return String(s || "").match(/[\u3400-\u9fff\uf900-\ufaff]|[A-Za-z0-9_]+|\s+|[^\sA-Za-z0-9_\u3400-\u9fff]/g) || []; }
function diff(a, b) {
  let A = tokens(a), B = tokens(b);
  if (A.length * B.length > 4e6) { A = String(a).split(/(?<=\n)/); B = String(b).split(/(?<=\n)/); }
  const n = A.length, m = B.length, W = m + 1;
  const T = new Uint16Array((n + 1) * W);
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--)
    T[i * W + j] = A[i] === B[j] ? T[(i + 1) * W + j + 1] + 1 : Math.max(T[(i + 1) * W + j], T[i * W + j + 1]);
  const ops = []; let i = 0, j = 0;
  const push = (t, s) => { const l = ops[ops.length - 1]; if (l && l.t === t) l.s += s; else ops.push({ t, s }); };
  while (i < n && j < m) {
    if (A[i] === B[j]) { push("=", A[i]); i++; j++; }
    else if (T[(i + 1) * W + j] >= T[i * W + j + 1]) push("-", A[i++]);
    else push("+", B[j++]);
  }
  while (i < n) push("-", A[i++]);
  while (j < m) push("+", B[j++]);
  return ops.map((o) => o.t === "=" ? esc(o.s) : o.t === "+" ? `<ins>${esc(o.s)}</ins>` : `<del>${esc(o.s)}</del>`).join("");
}

/* ═════════ STUDIO ═════════ */
function buildAgents() {
  const tpl = $("#tpl-agent");
  for (const k of ["a", "b"]) {
    const node = tpl.content.firstElementChild.cloneNode(true);
    node.dataset.agent = k;
    if (k === "b") node.classList.add("is-b");
    $(".agent-letter", node).textContent = AGENTS[k].letter;
    $(".agent-role", node).textContent = AGENTS[k].role;
    $(".agent-desc", node).textContent = AGENTS[k].desc;
    $("#slot-" + k).appendChild(node);
    glow(node);
  }
}

function agentEl(k) { return $(`.agent[data-agent="${k}"]`); }

function fillAgent(k) {
  const e = S.cfg[k], el = agentEl(k);
  $$("[data-provider]", el).forEach((b) => b.setAttribute("aria-checked", String(b.dataset.provider === e.provider)));
  $('[data-k="baseURL"]', el).value = e.baseURL || "";
  $('[data-k="baseURL"]', el).placeholder = DEF[e.provider] || DEF.openai;
  $('[data-k="apiKey"]', el).value = e.apiKey || "";
  $('[data-k="model"]', el).value = e.model || "";
  const t = $('[data-k="temperature"]', el);
  t.max = e.provider === "anthropic" ? 1 : 2;
  t.value = e.temperature ?? 0.7; setRangeFill(t);
  $('[data-out="temperature"]', el).textContent = fmt1(+t.value);
  $('[data-k="maxTokens"]', el).value = e.maxTokens || 4096;
  $('[data-k="stream"]', el).checked = e.stream !== false;
  $('[data-k="timeoutSec"]', el).value = e.timeoutSec || 900;
  $('[data-k="stallSec"]', el).value = e.stallSec || 180;
  requestAnimationFrame(() => placeInk($(".seg", el), '[aria-checked="true"]'));
  setConn(k, e.model ? "on" : "off", e.model ? e.model : "未连接");
  applyModel(k, false);
}

function setConn(k, state, text) {
  const c = $(".conn", agentEl(k));
  c.dataset.state = state;
  $("span", c).textContent = text;
}

function bindAgent(k) {
  const el = agentEl(k);
  const e = () => S.cfg[k];

  $$("[data-provider]", el).forEach((b) => b.addEventListener("click", () => {
    const p = b.dataset.provider; if (e().provider === p) return;
    const old = e().provider;
    if (!e().baseURL || e().baseURL === DEF[old]) e().baseURL = DEF[p];
    e().provider = p;
    if (p === "anthropic" && e().temperature > 1) e().temperature = 1;
    S.models[k] = []; S.minfo[k] = {}; e().model = "";
    fillAgent(k); save();
  }));

  $$("[data-k]", el).forEach((inp) => {
    const key = inp.dataset.k;
    const ev = inp.type === "checkbox" || inp.type === "range" ? "input" : "input";
    inp.addEventListener(ev, () => {
      let v = inp.type === "checkbox" ? inp.checked : inp.value;
      if (key === "temperature") { v = +v; setRangeFill(inp); $('[data-out="temperature"]', el).textContent = fmt1(v); }
      if (key === "maxTokens" || key === "timeoutSec" || key === "stallSec") v = parseInt(v, 10) || 0;
      if (key === "baseURL" || key === "apiKey") { v = String(v).trim(); }
      e()[key] = v;
      if (key === "apiKey" || key === "baseURL") setConn(k, "off", "未连接");
      save();
    });
  });

  // reveal key
  const rv = $('[data-act="reveal"]', el), key = $('[data-k="apiKey"]', el);
  rv.addEventListener("click", () => {
    const show = key.type === "password";
    key.type = show ? "text" : "password";
    $("use", rv).setAttribute("href", show ? "#i-eye-off" : "#i-eye");
    rv.setAttribute("aria-label", show ? "隐藏密钥" : "显示密钥");
  });

  // fetch models
  const fb = $('[data-act="fetch"]', el);
  fb.addEventListener("click", () => fetchModels(k, true));

  combo(k);
  $('[data-k="model"]', el).addEventListener("change", () => { applyModel(k, true).then(save); });
}

/* model limits: upstream first, reference table second */
const kfmt = (n) => (n >= 1000 ? (n % 1000 === 0 ? n / 1000 + "K" : (n / 1000).toFixed(1) + "K") : String(n));
async function applyModel(k, adopt) {
  const e = S.cfg[k], el = agentEl(k), hint = $(".param-hint", el);
  const t = $('[data-k="temperature"]', el), mt = $('[data-k="maxTokens"]', el);
  if (!e.model) { hint.hidden = true; t.disabled = false; return; }
  let info = S.minfo[k][e.model];
  if (!info) { try { info = await api("ModelInfo", e); } catch { info = null; } }
  if (!info || e.model !== $('[data-k="model"]', el).value) return;
  // max output
  if (info.maxOutput > 0) {
    mt.max = info.maxOutput;
    if (adopt || e.maxTokens > info.maxOutput) e.maxTokens = info.maxOutput;
  } else mt.max = 200000;
  mt.value = e.maxTokens;
  // temperature
  t.max = info.tempMax || (e.provider === "anthropic" ? 1 : 2);
  if (e.temperature > +t.max) e.temperature = +t.max;
  t.disabled = !info.temperature;
  t.value = e.temperature; setRangeFill(t);
  $('[data-out="temperature"]', el).textContent = info.temperature ? fmt1(e.temperature) : "—";
  // hint
  const src = info.source === "upstream" ? "上游返回" : info.source === "builtin" ? "参考值，上游未提供" : "";
  const parts = [];
  if (info.maxOutput > 0) parts.push(`输出上限 ${kfmt(info.maxOutput)}`);
  if (info.context > 0) parts.push(`上下文 ${kfmt(info.context)}`);
  let txt = parts.length ? parts.join(" · ") + (src ? `（${src}）` : "") : "上游没给这个模型的参数，按你填的值发送";
  if (!info.temperature) txt += "。此模型不接受温度，发送时自动省略";
  hint.textContent = txt;
  hint.dataset.src = info.source || "none";
  hint.hidden = false;
}

async function fetchModels(k, open) {
  const el = agentEl(k), fb = $('[data-act="fetch"]', el);
  if (fb.classList.contains("is-busy")) return;
  fb.classList.add("is-busy"); setConn(k, "busy", "连接中…");
  try {
    const list = (await api("ListModels", S.cfg[k])) || [];
    S.models[k] = list.map((m) => (typeof m === "string" ? m : m.id));
    S.minfo[k] = Object.fromEntries(list.filter((m) => typeof m === "object").map((m) => [m.id, m]));
    const withLimits = list.filter((m) => m.source === "upstream").length;
    setConn(k, "on", `已连接 · ${S.models[k].length} 个模型`);
    if (!S.cfg[k].model && S.models[k].length) {
      S.cfg[k].model = pickDefault(S.models[k], S.cfg[k].provider);
      $('[data-k="model"]', el).value = S.cfg[k].model;
      await applyModel(k, true); save();
    } else if (S.cfg[k].model) await applyModel(k, false);
    if (open) openCombo(k, "");
    toast(`${AGENTS[k].letter} 拉取到 ${S.models[k].length} 个模型` + (withLimits ? `，其中 ${withLimits} 个带上游参数` : ""));
  } catch (err) {
    setConn(k, "err", "连接失败");
    toast(`${AGENTS[k].letter}：${errMsg(err)}`, "err", 5200);
  } finally { fb.classList.remove("is-busy"); }
}

function pickDefault(list, provider) {
  const pref = provider === "anthropic" ? [/sonnet/, /opus/, /claude/] : [/^gpt-4\.1$/, /^gpt-4o$/, /gpt-4/, /gpt/];
  for (const re of pref) { const m = list.find((x) => re.test(x)); if (m) return m; }
  return list[0];
}

/* combobox */
function combo(k) {
  const el = agentEl(k), input = $('[data-k="model"]', el), list = $(".combo-list", el);
  let active = -1;
  input.addEventListener("focus", () => { if (S.models[k].length) openCombo(k, ""); });
  input.addEventListener("input", () => openCombo(k, input.value));
  input.addEventListener("keydown", (ev) => {
    const opts = $$(".opt", list);
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      if (list.hidden) { openCombo(k, input.value); return; }
      ev.preventDefault();
      active = (active + (ev.key === "ArrowDown" ? 1 : -1) + opts.length) % opts.length;
      opts.forEach((o, i) => o.setAttribute("aria-selected", String(i === active)));
      const o = opts[active];
      if (o) { if (o.offsetTop < list.scrollTop) list.scrollTop = o.offsetTop - 6; else if (o.offsetTop + o.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = o.offsetTop + o.offsetHeight - list.clientHeight + 6; }
    } else if (ev.key === "Enter") {
      if (!list.hidden && opts[active]) { ev.preventDefault(); choose(k, opts[active].dataset.v); }
      else closeCombo(k);
    } else if (ev.key === "Escape") { closeCombo(k); }
  });
  input.addEventListener("blur", () => setTimeout(() => closeCombo(k), 120));
  list.addEventListener("pointerdown", (ev) => { const o = ev.target.closest(".opt"); if (o) { ev.preventDefault(); choose(k, o.dataset.v); } });
  list._reset = () => { active = -1; };
}
function openCombo(k, q) {
  const el = agentEl(k), list = $(".combo-list", el), input = $('[data-k="model"]', el);
  const all = S.models[k];
  if (!all.length) { closeCombo(k); return; }
  const qq = q.trim().toLowerCase();
  const hit = qq && all.includes(q.trim()) ? all : all.filter((m) => !qq || m.toLowerCase().includes(qq));
  list._reset && list._reset();
  list.innerHTML = `<div class="head">${hit.length} / ${all.length} 个模型</div>` + (hit.length
    ? hit.slice(0, 400).map((m) => {
        const label = qq && !all.includes(q.trim()) ? esc(m).replace(new RegExp(qq.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "ig"), (x) => `<mark>${x}</mark>`) : esc(m);
        return `<button type="button" class="opt${m === S.cfg[k].model ? " is-current" : ""}" role="option" data-v="${esc(m)}">${label}</button>`;
      }).join("")
    : `<div class="empty">没有匹配，回车即使用「${esc(q)}」</div>`);
  list.hidden = false; input.setAttribute("aria-expanded", "true");
}
function closeCombo(k) {
  const el = agentEl(k);
  $(".combo-list", el).hidden = true;
  $('[data-k="model"]', el).setAttribute("aria-expanded", "false");
}
function choose(k, v) {
  const input = $('[data-k="model"]', agentEl(k));
  input.value = v; S.cfg[k].model = v; closeCombo(k);
  setConn(k, "on", v);
  applyModel(k, true).then(save);
}

/* brief */
const F = {
  system: () => $("#f-system"), task: () => $("#f-task"), criteria: () => $("#f-criteria"), notes: () => $("#f-notes"), usecase: () => $("#f-usecase"),
};
function fillBrief() {
  const b = S.cfg.brief;
  F.system().value = b.systemPrompt || "";
  F.task().value = b.userTask || "";
  F.criteria().value = b.criteria || "";
  F.notes().value = b.directorNotes || "";
  F.usecase().value = b.useCase || "";
  if (b.directorNotes) $("#fold-notes").open = true;
  $("#f-until").checked = !!b.untilDone; syncUntil();
  $("#f-exec").checked = b.requireExecution !== false;
  setEvolve(b.evolve || "chain", false);
  const t = $("#f-target"); t.value = b.targetScore || 9; setRangeFill(t); $("#o-target").textContent = fmt1(+t.value);
  $$("textarea").forEach((ta) => { autosize(ta); updCount(ta); });
  syncChips();
}
function updCount(ta) {
  const c = $(`[data-count-for="${ta.id}"]`);
  if (c) c.textContent = [...ta.value].length.toLocaleString();
}
function syncChips() {
  const v = F.criteria().value;
  $$("#chips button").forEach((b) => b.classList.toggle("is-on", v.split("\n").map((s) => s.trim()).includes(b.dataset.chip)));
}
function bindBrief() {
  const map = { "f-system": "systemPrompt", "f-task": "userTask", "f-criteria": "criteria", "f-notes": "directorNotes", "f-usecase": "useCase" };
  for (const [id, key] of Object.entries(map)) {
    const ta = $("#" + id);
    ta.addEventListener("input", () => { S.cfg.brief[key] = ta.value; autosize(ta); updCount(ta); if (id === "f-criteria") syncChips(); save(); });
  }
  $$("#chips button").forEach((b) => b.addEventListener("click", () => {
    const ta = F.criteria(); let lines = ta.value.split("\n").map((s) => s.trim()).filter(Boolean);
    if (lines.includes(b.dataset.chip)) lines = lines.filter((l) => l !== b.dataset.chip); else lines.push(b.dataset.chip);
    ta.value = lines.join("\n"); S.cfg.brief.criteria = ta.value; autosize(ta); syncChips(); save();
  }));
  $("#f-until").addEventListener("change", (e) => {
    S.cfg.brief.untilDone = e.target.checked; syncUntil(); save();
    toast(e.target.checked ? "不限轮数：一直改到达标为止，失败会自动重试，可随时停止" : `最多 ${S.cfg.brief.maxRounds || 10} 轮`);
  });
  $$("[data-step]").forEach((b) => b.addEventListener("click", () => {
    const n = Math.max(1, Math.min(50, (S.cfg.brief.maxRounds || 10) + +b.dataset.step));
    if (S.cfg.brief.untilDone) return;
    S.cfg.brief.maxRounds = n; const o = $("#o-rounds"); o.textContent = n;
    o.animate([{ transform: `translateY(${b.dataset.step > 0 ? 8 : -8}px)`, opacity: 0 }, { transform: "none", opacity: 1 }], { duration: 320, easing: "cubic-bezier(.16,1,.3,1)" });
    save();
  }));
  const t = $("#f-target");
  t.addEventListener("input", () => { S.cfg.brief.targetScore = +t.value; setRangeFill(t); $("#o-target").textContent = fmt1(+t.value); save(); });

  $("#f-evolve").addEventListener("click", (e) => {
    const bt = e.target.closest("button[data-evolve]"); if (!bt) return;
    S.cfg.brief.evolve = bt.dataset.evolve; save(); setEvolve(bt.dataset.evolve, true);
  });
  $("#f-exec").addEventListener("change", (e) => { S.cfg.brief.requireExecution = e.target.checked; save(); toast(e.target.checked ? "B 拒绝或只交付一部分时，A 会继续改写" : "已关闭：只看分数是否达标"); });
  $("#btn-draft").addEventListener("click", draft);
  $("#btn-start").addEventListener("click", () => (S.session && S.session.running ? stop() : start()));

  // magnetic CTA
  const cta = $("#btn-start");
  if (!reduce) {
    cta.addEventListener("pointermove", (e) => {
      const r = cta.getBoundingClientRect();
      const x = (e.clientX - r.left - r.width / 2) / r.width, y = (e.clientY - r.top - r.height / 2) / r.height;
      cta.style.transform = `translate(${x * 10}px, ${y * 8}px)`;
    });
    cta.addEventListener("pointerleave", () => { cta.style.transform = ""; });
  }
}

let saveTimer = 0, saveErrShown = false;
function saveNow() {
  clearTimeout(saveTimer);
  if (!S.cfgLoaded) return Promise.resolve(); // never overwrite a config we failed to read
  return api("SaveConfig", S.cfg).then(() => { saveErrShown = false; }).catch((err) => {
    if (!saveErrShown) { saveErrShown = true; toast("设置没能保存：" + errMsg(err), "err", 6000); }
  });
}
function save() { clearTimeout(saveTimer); saveTimer = setTimeout(saveNow, 300); }

async function draft() {
  const btn = $("#btn-draft");
  if (btn.classList.contains("is-busy")) return;
  if (!S.cfg.brief.userTask.trim()) { toast("先写下用户需求，A 才知道要为什么起草", "err"); F.task().focus(); return; }
  if (!S.cfg.a.model) { toast("先给 A 选一个模型", "err"); return; }
  btn.classList.add("is-busy"); $("span", btn).textContent = "起草中…";
  const ta = F.system(), prev = ta.value; ta.value = "";
  let buf = "";
  const paint = raf(() => { ta.value = buf; autosize(ta); updCount(ta); });
  const off = on("draft", (d) => { buf += d; paint(); });
  try {
    const out = await api("DraftPrompt", S.cfg);
    ta.value = out; S.cfg.brief.systemPrompt = out; autosize(ta); updCount(ta); save();
    ta.animate([{ boxShadow: "0 0 0 4px rgba(255,90,31,.35)" }, { boxShadow: "0 0 0 4px rgba(255,90,31,0)" }], { duration: 900 });
    toast("A 已起草 System Prompt");
  } catch (err) {
    ta.value = prev; autosize(ta); toast(errMsg(err), "err", 5200);
  } finally {
    off && off(); btn.classList.remove("is-busy"); $("span", btn).textContent = "A 起草";
  }
}

/* ═════════ RUN ═════════ */
async function start() {
  const b = S.cfg.brief;
  if (!b.userTask.trim()) { toast("用户需求不能为空", "err"); F.task().focus(); return; }
  for (const k of ["a", "b"]) if (!S.cfg[k].model) { toast(`${AGENTS[k].letter} 还没有选择模型`, "err"); $('[data-k="model"]', agentEl(k)).focus(); return; }
  // Create the session before calling Start: the backend may emit round_start
  // before the Start promise resolves, and those events must not be dropped.
  const prev = S.session;
  S.session = { id: "", brief: JSON.parse(JSON.stringify(b)), rounds: [], live: null, best: -1, stopReason: "", running: true, t0: Date.now(), t1: 0 };
  S.sel = null;
  try {
    S.session.id = await api("Start", S.cfg);
    setRunning(true);
    go("arena");
    renderArena(true);
  } catch (err) { S.session = prev; toast(errMsg(err), "err", 5200); }
}
function stop() { api("Stop").catch(() => {}); setStatus("idle", "正在停止…"); }

function setRunning(r) {
  $("#app").dataset.running = r ? "1" : "0";
  const cta = $("#btn-start");
  cta.classList.toggle("is-running", r);
  $(".cta-label", cta).textContent = r ? "停止对弈" : "开始对弈";
  $("#btn-stop").hidden = !r;
  $("#btn-draft").disabled = r;
  if (r) tick();
  requestAnimationFrame(() => placeInk($(".nav"), `[data-go="${S.view}"]`));
}
let tickT;
function tick() {
  clearTimeout(tickT);
  const s = S.session; if (!s) return;
  const ms = (s.t1 || Date.now()) - s.t0;
  $("#elapsed").textContent = `${pad2(Math.floor(ms / 60000))}:${pad2(Math.floor(ms / 1000) % 60)} 用时`;
  if (s.running) tickT = setTimeout(tick, 500);
}

function setStatus(state, text) {
  const st = $("#status"); st.dataset.state = state; $("span", st).textContent = text;
  $("#app").dataset.phase = state === "a" || state === "b" ? state : "";
  const c = $("#court"); c.dataset.phase = state === "a" || state === "b" ? state : "idle";
}
function caption(text) {
  const c = $("#court-caption"); if (c.textContent === text) return;
  c.textContent = text; c.classList.remove("swap"); void c.offsetWidth; c.classList.add("swap");
}

function onDuet(ev) {
  const s = S.session; if (!s) return;
  // Self-heal if a round_start was missed: any live event opens the round.
  if (ev.round && !s.live && /_delta|_think|progress|b_done/.test(ev.type) && !s.rounds.some((r) => r.index === ev.round)) {
    s.live = { index: ev.round, phase: ev.type[0] === "a" || ev.text === "a" ? "a" : "b", bText: "", aText: "", bThink: "", aThink: "", prog: null, systemPrompt: "" };
    renderBoard(); renderRail(); if (S.sel == null) renderStage();
  }
  switch (ev.type) {
    case "round_start":
      s.live = { index: ev.round, phase: "b", bText: "", aText: "", bThink: "", aThink: "", prog: null, systemPrompt: (ev.data && ev.data.systemPrompt) || "" };
      setStatus("b", `B 作答中 · R${pad2(ev.round)}`);
      caption(`第 ${ev.round} 轮 · B 正在作答`);
      if (S.sel == null) renderStage();
      renderBoard(); renderRail();
      break;
    case "retry_reset":
      // a retried request: drop what the failed attempt streamed
      if (s.live) { const k = ev.text === "a" ? "a" : "b"; s.live[k + "Text"] = ""; s.live[k + "Think"] = ""; paintLive(); }
      break;
    case "b_delta":
      if (s.live) { s.live.bText += ev.text; paintLive(); }
      break;
    case "b_done":
      if (s.live) { s.live.phase = "a"; s.live.bMeta = ev.data; }
      setStatus("a", `A 评判中 · R${pad2(ev.round)}`);
      caption(`A 正在评判第 ${ev.round} 轮，并改写 Prompt`);
      renderRail(); if (S.sel == null) renderStage();
      break;
    case "a_delta":
      if (s.live) { s.live.aText += ev.text; paintLive(); }
      break;
    case "b_think":
    case "a_think":
      if (s.live) { s.live[ev.type[0] + "Think"] += ev.text; paintLive(); }
      break;
    case "progress":
      if (s.live) { s.live.prog = ev.data; s.live.progRole = ev.text; showProgress(); }
      break;
    case "a_done": {
      const r = ev.data; s.rounds.push(r); s.live = null;
      s.best = bestIdx(s.rounds);
      renderBoard(); renderRail(); renderChart();
      if (S.sel == null) renderStage(true);
      if (refused(r)) caption(`第 ${r.index} 轮 B 拒绝执行 · A 换个说法再来`);
      else caption(`第 ${r.index} 轮 ${fmt1(r.judgement.score)} 分 · ${r.judgement.verdict || ""}`.slice(0, 60));
      break;
    }
    case "finished":
    case "error": {
      const d = ev.data || {};
      if (d.rounds) s.rounds = d.rounds;
      s.best = typeof d.best === "number" ? d.best : bestIdx(s.rounds);
      s.stopReason = d.stopReason || (ev.type === "error" ? "error: " + ev.text : "stopped");
      s.running = false; s.live = null; s.t1 = Date.now();
      setRunning(false); tick();
      if (ev.type === "error") { setStatus("error", "出错"); caption("对弈中断 · 详情见运行日志"); toast(ev.text, "err", 9000); openLog(); }
      else { setStatus("done", "已收官"); caption(reasonText(s.stopReason)); }
      renderArena(); loadArchive();
      break;
    }
  }
}
function refused(r) { return !!(r && r.judgement && r.judgement.refused === true); }
function bestIdx(rounds) {
  let b = -1;
  rounds.forEach((r, i) => {
    if (b < 0) { b = i; return; }
    const rb = rounds[b];
    if (refused(rb) && !refused(r)) { b = i; return; }
    if (refused(rb) === refused(r) && r.judgement.score > rb.judgement.score) b = i;
  });
  return b;
}
function reasonText(r) {
  if (!r) return "";
  if (r === "target") return "达到目标分，收官";
  if (r === "max_rounds") return S.session && S.session.brief.untilDone ? "已到安全上限 200 轮" : "已跑满轮数";
  if (r === "stopped") return "已手动停止";
  return "出错：" + r.replace(/^error:\s*/, "");
}

/* ═════════ ARENA RENDER ═════════ */
function renderArena(fresh) {
  const s = S.session;
  $("#arena-empty").hidden = !!s; $("#arena").hidden = !s;
  if (!s) return;
  $("#arena-target").textContent = fmt1(s.brief.targetScore || 9);
  $("#round-max").textContent = s.brief.untilDone ? "∞" : s.brief.maxRounds || 10;
  if (fresh) { $("#best-score").dataset.v = "0"; $("#best-score").textContent = "—"; setStatus("b", "开局"); caption("开局 · A 把 Prompt 交给 B"); $("#elapsed").textContent = "00:00 用时"; }
  if (!s.running && !fresh) { setStatus(s.stopReason.startsWith("error") ? "error" : "done", s.stopReason.startsWith("error") ? "出错" : "已收官"); caption(reasonText(s.stopReason)); }
  renderBoard(); renderRail(); renderChart(); renderStage(true); renderCurtain();
}

function renderBoard() {
  const s = S.session;
  const best = s.best >= 0 ? s.rounds[s.best] : null;
  const bs = $("#best-score");
  if (best) countTo(bs, best.judgement.score); else { bs._anim = (bs._anim || 0) + 1; bs.textContent = "—"; bs.dataset.v = "0"; }
  $("#best-label").textContent = best ? `最佳得分 · R${pad2(best.index)}` : "最佳得分";
  const n = s.live ? s.live.index : s.rounds.length;
  $("#round-now").textContent = pad2(n);
  const rs = s.rounds;
  if (rs.length && !s.running) {
    const avg = (f) => (rs.reduce((t, r) => t + (f(r) || 0), 0) / rs.length / 1000).toFixed(1) + "s";
    const tok = rs.reduce((t, r) => t + ((r.aUsage && r.aUsage.inputTokens) || 0) + ((r.aUsage && r.aUsage.outputTokens) || 0) + ((r.bUsage && r.bUsage.inputTokens) || 0) + ((r.bUsage && r.bUsage.outputTokens) || 0), 0);
    const gain = rs.length > 1 ? rs[s.best].judgement.score - rs[0].judgement.score : 0;
    $("#stats").innerHTML = `<dt>B 平均作答</dt><dd>${avg((r) => r.bMillis)}</dd><dt>A 平均评判</dt><dd>${avg((r) => r.aMillis)}</dd>` +
      (tok ? `<dt>总 Tokens</dt><dd>${tok.toLocaleString()}</dd>` : "") + (rs.some(refused) ? `<dt>B 拒绝轮数</dt><dd>${rs.filter(refused).length}</dd>` : "") + (rs.length > 1 ? `<dt>较首轮提升</dt><dd>${gain >= 0 ? "+" : ""}${fmt1(gain)}</dd>` : "");
  } else $("#stats").innerHTML = "";
}

function renderRail() {
  const s = S.session, rail = $("#rail");
  const items = s.rounds.map((r, i) => {
    const cur = S.sel === r.index || (S.sel == null && !s.live && i === s.rounds.length - 1);
    const rf = refused(r);
    return `<button class="rail-item${rf ? " is-refused" : ""}" data-r="${r.index}" aria-current="${cur}" style="animation-delay:${Math.min(i, 8) * 40}ms">
      <span class="r-no">${pad2(r.index)}</span>
      <span class="r-verdict">${esc(r.judgement.verdict || "—")}</span>
      ${rf ? '<span class="r-refused">拒绝</span>' : i === s.best ? '<span class="r-best">BEST</span>' : `<span class="r-score num">${fmt1(r.judgement.score)}</span>`}
      <span class="r-bar"><i style="--w:${r.judgement.score * 10}%"></i></span>
    </button>`;
  });
  if (s.live) items.push(`<button class="rail-item is-live" data-r="live" data-phase="${s.live.phase}" aria-current="${S.sel == null}">
      <span class="r-no">${pad2(s.live.index)}</span><span class="r-verdict">${s.live.phase === "b" ? "B 作答中…" : "A 评判中…"}</span>
      <span class="r-score num">··</span><span class="r-bar"><i></i></span></button>`);
  rail.innerHTML = items.join("");
  const cur = $('[aria-current="true"]', rail);
  if (cur && rail.scrollWidth > rail.clientWidth) {
    const l = cur.offsetLeft - rail.offsetLeft, r = l + cur.offsetWidth;
    if (l < rail.scrollLeft) rail.scrollLeft = l; else if (r > rail.scrollLeft + rail.clientWidth) rail.scrollLeft = r - rail.clientWidth;
  }
}

function selectRound(v) {
  const s = S.session;
  S.sel = v === "live" || (s.live == null && +v === (s.rounds[s.rounds.length - 1] || {}).index && s.running) ? null : +v;
  if (v === "live") S.sel = null;
  renderRail(); renderChart(); renderStage(true);
}

function currentRound() {
  const s = S.session;
  if (S.sel != null) return s.rounds.find((r) => r.index === S.sel) || null;
  if (s.live) return null;
  return s.rounds[s.rounds.length - 1] || null;
}

function setRing(score) {
  const ring = $("#stage-ring"), C = 2 * Math.PI * 19;
  ring.style.strokeDashoffset = score == null ? C : C * (1 - score / 10);
  const r = currentRound();
  ring.style.stroke = score == null ? "var(--surface-3)" : refused(r) ? "var(--danger)" : score >= (S.session.brief.targetScore || 9) ? "var(--b)" : "var(--a)";
}

function renderStage(full) {
  const s = S.session; if (!s) return;
  const r = currentRound();
  const live = !r && s.live;
  const idx = r ? r.index : live ? live.index : 1;
  $("#stage-round").textContent = `Round ${pad2(idx)}`;
  $("#stage-eyebrow").textContent = live ? "Live · 进行中" : r && s.best >= 0 && s.rounds[s.best] === r ? "Best round · 最佳轮" : "Round · 第 " + idx + " 轮";
  const sn = $("#stage-score-num");
  if (r) { countTo(sn, r.judgement.score, { dur: 700 }); setRing(r.judgement.score); } else { sn._anim = (sn._anim || 0) + 1; sn.textContent = "—"; sn.dataset.v = "0"; setRing(null); }
  $("#stage-verdict").textContent = r ? r.judgement.verdict || "" : "";
  const banner = $("#refusal-banner");
  if (r && refused(r)) {
    banner.hidden = false;
    $("span", banner).textContent = r.index < (s.rounds[s.rounds.length - 1] || {}).index || s.live
      ? "B 这一轮拒绝或没有完整执行，A 已据此改写 System Prompt，进入下一轮。"
      : "B 这一轮拒绝或没有完整执行。" + (s.running ? "A 正在改写 System Prompt。" : "");
  } else banner.hidden = true;
  if (r && refused(r)) $("#stage-eyebrow").textContent = "Refused · B 拒绝执行";

  // B pane
  const bOut = $("#b-out");
  if (r) {
    bOut.classList.remove("is-stream"); bOut.innerHTML = r.output ? md(r.output) : '<p class="placeholder">（空回答）</p>';
    $("#b-meta").textContent = metaText(r.bMillis, r.bUsage && r.bUsage.outputTokens);
  } else if (live) paintLive(true);
  // A pane
  const aOut = $("#a-out");
  if (r) {
    aOut.classList.remove("is-stream");
    const j = r.judgement, list = (xs) => (xs || []).map((x) => `<li>${esc(x)}</li>`).join("");
    aOut.innerHTML = `<div class="judge">
      ${j.strengths && j.strengths.length ? `<section class="good"><h5>做得好</h5><ul>${list(j.strengths)}</ul></section>` : ""}
      ${j.issues && j.issues.length ? `<section class="bad"><h5>待改进</h5><ul>${list(j.issues)}</ul></section>` : ""}
      ${!(j.strengths || []).length && !(j.issues || []).length ? `<p class="placeholder">A 没有给出条目</p>` : ""}
    </div>`;
    $("#a-meta").textContent = metaText(r.aMillis, r.aUsage && r.aUsage.outputTokens);
  }
  renderPrompt();
  if (full && !reduce) $$(".pane", $("#stage")).forEach((p, i) => p.animate([{ opacity: 0, transform: "translateY(10px)" }, { opacity: 1, transform: "none" }], { duration: 600, delay: i * 60, easing: "cubic-bezier(.16,1,.3,1)", fill: "backwards" }));
}

function metaText(ms, tok) {
  const parts = [];
  if (ms) parts.push((ms / 1000).toFixed(1) + "s");
  if (tok) parts.push(tok + " tok");
  return parts.join(" · ");
}

const paintLive = (() => {
  let pending = false;
  const doPaint = () => {
    pending = false;
    const s = S.session; if (!s || !s.live || S.sel != null) return;
    const L = s.live, bOut = $("#b-out"), aOut = $("#a-out");
    const stick = (el) => el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    const bs = stick(bOut), as = stick(aOut);
    bOut.innerHTML = thinkBlock(L.bThink, L.phase === "b" && !L.bText) + (L.bText ? md(L.bText) : L.phase === "b" ? waitLine("b") : "");
    bOut.classList.toggle("is-stream", L.phase === "b");
    $("#b-meta").textContent = L.phase === "b" ? `${[...L.bText].length} 字` : metaText(L.bMeta && L.bMeta.ms, L.bMeta && L.bMeta.usage && L.bMeta.usage.outputTokens);
    if (L.phase === "b") { aOut.innerHTML = '<p class="placeholder">等 B 交卷后，A 再落笔。</p>'; aOut.classList.remove("is-stream"); $("#a-meta").textContent = ""; }
    else { aOut.innerHTML = thinkBlock(L.aThink, !L.aText) + (L.aText ? `<div class="judge-raw">${esc(L.aText)}</div>` : waitLine("a")); aOut.classList.add("is-stream"); $("#a-meta").textContent = `${[...L.aText].length} 字`; }
    if (bs) bOut.scrollTop = bOut.scrollHeight;
    if (as) aOut.scrollTop = aOut.scrollHeight;
  };
  let last = 0;
  return (now) => {
    if (now) return doPaint();
    if (pending) return; pending = true;
    const wait = Math.max(0, 120 - (performance.now() - last));
    setTimeout(() => requestAnimationFrame(() => { last = performance.now(); doPaint(); }), wait);
  };
})();

const PHASE_CN = { connecting: "连接中", waiting: "等待首字", thinking: "推理中", streaming: "输出中", done: "完成", error: "出错" };
const secs = (ms) => { const s = Math.floor((ms || 0) / 1000); return s >= 60 ? `${Math.floor(s / 60)}分${pad2(s % 60)}秒` : `${s}秒`; };
function thinkBlock(text, active) {
  if (!text) return "";
  const tail = text.length > 1600 ? "…" + text.slice(-1600) : text;
  return `<div class="think"><b>${active ? "<i></i>" : ""}THINKING · ${[...text].length} 字</b><div><span>${esc(tail)}</span></div></div>`;
}
function waitLine(role) {
  const L = S.session && S.session.live, p = L && L.progRole === role ? L.prog : null;
  if (!p) return `<p class="placeholder">${role === "b" ? "等 B 开口…" : "A 正在审读…"}</p>`;
  const idle = p.idleMs > 30000;
  return `<p class="wait-line${idle ? " warn" : ""}">${PHASE_CN[p.phase] || p.phase} · 已等 ${secs(p.elapsedMs)}${idle ? ` · 已 ${secs(p.idleMs)} 没有收到数据` : ""}</p>`;
}
function showProgress() {
  const L = S.session && S.session.live; if (!L || !L.prog) return;
  const p = L.prog, who = L.progRole === "a" ? "A" : "B";
  const extra = p.phase === "thinking" ? ` · 推理 ${p.reasoning} 字` : p.phase === "streaming" ? ` · ${p.content} 字` : "";
  setStatus(L.progRole, `${who} ${PHASE_CN[p.phase] || p.phase} · ${secs(p.elapsedMs)}${extra}`);
  if (p.phase !== "done" && p.phase !== "error") caption(`第 ${L.index} 轮 · ${who} ${PHASE_CN[p.phase] || p.phase}${p.idleMs > 30000 ? ` · ${secs(p.idleMs)}没有数据` : ""}`);
  if (!L[L.progRole + "Text"]) paintLive();
  renderLogLive();
}

function renderPrompt() {
  const s = S.session, r = currentRound(), out = $("#prompt-out"), notes = $("#change-notes");
  const seg = $("#diff-mode");
  const live = !r && s.live;
  $$("button", seg).forEach((b) => { b.disabled = !!live && b.dataset.mode !== "used"; });
  let mode = live ? "used" : S.diffMode;
  $$("button", seg).forEach((b) => b.setAttribute("aria-checked", String(b.dataset.mode === mode)));
  placeInk(seg, `[data-mode="${mode}"]`);
  if (live) { out.textContent = live.systemPrompt || "（空 System Prompt）"; notes.innerHTML = ""; return; }
  if (!r) { out.textContent = ""; notes.innerHTML = ""; return; }
  const J = r.judgement, next = J.revised_system_prompt || r.systemPrompt;
  // The revision may be built on an earlier, better round ("best" mode): diff against that.
  const baseR = J.baseRound && J.baseRound !== r.index ? s.rounds.find((x) => x.index === J.baseRound) : null;
  const basePrompt = baseR ? baseR.systemPrompt : r.systemPrompt;
  if (mode === "diff") out.innerHTML = next === basePrompt ? esc(next) + "\n\n<span class=\"placeholder\">（A 没有改动）</span>" : diff(basePrompt, next);
  else if (mode === "next") out.textContent = next;
  else out.textContent = r.systemPrompt || "（空 System Prompt）";
  const edits = J.edits || [];
  const head = [];
  if (baseR) head.push(`<li class="note-base"><span>↺</span>本轮没有超过第 ${baseR.index} 轮的 ${fmt1(baseR.judgement.score)} 分，下一版在第 ${baseR.index} 轮的 Prompt 上接着改</li>`);
  else if (edits.length) head.push(`<li class="note-base"><span>→</span>在本轮 Prompt 上接着改 · ${edits.filter((e) => e.applied).length}/${edits.length} 处修改已应用</li>`);
  const failed = edits.filter((e) => !e.applied).map((e) => `<li class="note-fail"><span>✕</span>未应用：${esc(e.note || "")}「${esc((e.find || e.after || "").slice(0, 40))}」</li>`);
  notes.innerHTML = head.join("") + (J.change_notes || []).map((n, i) => `<li><span>${pad2(i + 1)}</span>${esc(n)}</li>`).join("") + failed.join("");
}

function renderCurtain() {
  const s = S.session, c = $("#curtain");
  if (!s || s.running || !s.rounds.length) { c.hidden = true; return; }
  const b = s.rounds[s.best];
  c.hidden = false;
  if (refused(b)) {
    $("#curtain-eyebrow").textContent = "Curtain call · " + reasonText(s.stopReason);
    $("#curtain-title").innerHTML = `B 每一轮都拒绝了。可以加大轮数、换一个 B 模型，或在「导演附加要求」里补充使用场景`;
    return;
  }
  $("#curtain-eyebrow").textContent = "Curtain call · " + reasonText(s.stopReason);
  $("#curtain-title").innerHTML = `最佳 Prompt 出自 <em>Round ${pad2(b.index)}</em>，得分 <b>${fmt1(b.judgement.score)}</b>`;
}

/* chart */
function renderChart() {
  const s = S.session, box = $("#chart"); if (!s) return;
  const W = Math.max(200, box.clientWidth - 52), H = 104, padT = 14, padB = 22;
  const max = s.brief.untilDone ? Math.max(6, s.rounds.length + 2) : Math.max(s.brief.maxRounds || 10, s.rounds.length, 2);
  const x = (i) => ((i - 1) / (max - 1)) * W;
  const y = (v) => padT + (1 - v / 10) * (H - padT - padB);
  const tgt = s.brief.targetScore || 9;
  const pts = s.rounds.map((r) => [x(r.index), y(r.judgement.score), r]);
  const line = pts.map((p, i) => (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1)).join(" ");
  const area = pts.length ? `${line} L${pts[pts.length - 1][0].toFixed(1)} ${H - padB} L${pts[0][0].toFixed(1)} ${H - padB} Z` : "";
  const sel = currentRound();
  const grid = [0, 5, 10].map((v) => `<line x1="0" x2="${W}" y1="${y(v)}" y2="${y(v)}"/>`).join("");
  const xl = Array.from({ length: max }, (_, i) => i + 1).filter((i) => max <= 12 || i === 1 || i === max || i % 5 === 0)
    .map((i) => `<text class="x-lbl" x="${x(i)}" y="${H - 4}">${pad2(i)}</text>`).join("");
  box.innerHTML = `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none">
    <defs>
      <linearGradient id="chartStroke" x1="0" x2="1"><stop offset="0" stop-color="#ff5a1f"/><stop offset="1" stop-color="#8fd8ff"/></linearGradient>
      <linearGradient id="chartFill" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="#8fd8ff" stop-opacity=".16"/><stop offset="1" stop-color="#8fd8ff" stop-opacity="0"/></linearGradient>
    </defs>
    <g class="grid">${grid}</g>
    <line class="tgt" x1="0" x2="${W}" y1="${y(tgt)}" y2="${y(tgt)}"/>
    <text class="tgt-lbl" x="${W}" y="${y(tgt) - 5}" text-anchor="end">TARGET ${fmt1(tgt)}</text>
    ${area ? `<path class="area" d="${area}"/>` : ""}
    ${line ? `<path class="line" d="${line}"/>` : ""}
    ${pts.map((p, i) => `<circle class="pt${i === s.best && !refused(p[2]) ? " is-best" : ""}${refused(p[2]) ? " is-refused" : ""}${sel && sel.index === p[2].index ? " is-sel" : ""}" cx="${p[0].toFixed(1)}" cy="${p[1].toFixed(1)}" r="4" data-r="${p[2].index}" data-s="${fmt1(p[2].judgement.score)}"/>`).join("")}
    ${xl}
  </svg><div class="chart-tip"></div>`;
  box.querySelector("svg").style.marginLeft = "0";
  const path = $(".line", box);
  if (path && !reduce && box._n !== pts.length) {
    const len = path.getTotalLength();
    const prev = box._len || 0;
    path.style.strokeDasharray = len; path.style.strokeDashoffset = len - prev;
    path.getBoundingClientRect();
    path.style.transition = "stroke-dashoffset 1s cubic-bezier(.16,1,.3,1)"; path.style.strokeDashoffset = 0;
    box._len = len;
  }
  box._n = pts.length;
}

/* ═════════ ARCHIVE ═════════ */
async function loadArchive() {
  let list = [];
  try { list = (await api("ListSessions")) || []; } catch {}
  $("#archive-count").textContent = list.length ? `共 ${list.length} 场对弈` : "";
  const g = $("#archive-grid");
  if (!list.length) { g.innerHTML = '<div class="archive-empty">还没有存档。第一场对弈结束后，它会出现在这里。</div>'; return; }
  g.innerHTML = list.map((m, i) => {
    const d = new Date(m.createdAt);
    const ok = m.stopReason === "target";
    return `<div class="card" role="button" tabindex="0" data-id="${esc(m.id)}" style="--i:${Math.min(i, 12)}">
      <div class="card-top"><span>${d.getFullYear()}.${pad2(d.getMonth() + 1)}.${pad2(d.getDate())} · ${pad2(d.getHours())}:${pad2(d.getMinutes())}</span>
        <span class="card-reason${ok ? " ok" : ""}">${esc(ok ? "达标" : m.stopReason === "max_rounds" ? "跑满" : m.stopReason === "stopped" ? "停止" : m.stopReason ? "中断" : "进行中")}</span></div>
      <h3 class="card-title">${esc(m.title || "未命名")}</h3>
      <div class="card-foot">
        <div class="card-score">${m.rounds ? fmt1(m.bestScore) : "—"}<small>${m.rounds} 轮</small></div>
        <div class="card-models"><span class="ma"><b>A</b>${esc(m.modelA || "—")}</span><span class="mb"><b>B</b>${esc(m.modelB || "—")}</span></div>
      </div>
      <button class="hold" type="button" data-hold="${esc(m.id)}" aria-label="按住删除" style="position:absolute;right:16px;top:14px"><svg><use href="#i-trash"/></svg><span>按住删除</span></button>
    </div>`;
  }).join("");
  $$(".card", g).forEach(glow);
}

async function openSession(id) {
  if (S.session && S.session.running) { toast("对弈进行中，结束后再翻档案", "err"); return; }
  try {
    const s = await api("GetSession", id);
    S.session = { id: s.id, brief: s.brief, rounds: s.rounds || [], live: null, best: s.best, stopReason: s.stopReason || "stopped", running: false,
      t0: new Date(s.createdAt).getTime(), t1: new Date(s.finishedAt || s.createdAt).getTime() };
    if (S.session.best < 0) S.session.best = bestIdx(S.session.rounds);
    S.sel = S.session.best >= 0 ? S.session.rounds[S.session.best].index : null;
    $("#best-score").dataset.v = "0";
    go("arena"); renderArena(); tick();
  } catch (err) { toast(errMsg(err), "err"); }
}

function bindArchive() {
  const g = $("#archive-grid");
  g.addEventListener("click", (e) => {
    if (e.target.closest(".hold")) return;
    const c = e.target.closest(".card"); if (c) openSession(c.dataset.id);
  });
  g.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.classList.contains("card")) openSession(e.target.dataset.id); });
  let holdT;
  const cancel = (b) => { clearTimeout(holdT); b && b.classList.remove("is-holding"); };
  g.addEventListener("pointerdown", (e) => {
    const b = e.target.closest(".hold"); if (!b) return;
    e.preventDefault(); b.classList.add("is-holding");
    holdT = setTimeout(async () => {
      const card = b.closest(".card");
      try { await api("DeleteSession", b.dataset.hold); card.classList.add("is-leaving"); setTimeout(loadArchive, 480); toast("已删除"); }
      catch (err) { toast(errMsg(err), "err"); cancel(b); }
    }, 900);
    const up = () => { cancel(b); window.removeEventListener("pointerup", up); };
    window.addEventListener("pointerup", up);
  });
}

/* ═════════ NAV ═════════ */
function go(view) {
  S.view = view;
  $("#app").dataset.view = view;
  $$(".view").forEach((v) => v.classList.toggle("is-active", v.id === "view-" + view));
  $$(".nav [data-go]").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.go === view)));
  placeInk($(".nav"), `[data-go="${view}"]`);
  if (view === "archive") loadArchive();
  if (view === "arena" && S.session) requestAnimationFrame(renderChart);
}

/* run log */
const LOG = { lines: [], filter: "all", open: false, loaded: false };
const LV_RANK = { INFO: 0, WARN: 1, ERROR: 2 };
function logLi(l) {
  const t = (l.t || "").slice(11, 19), sep = /^(═|──)/.test(l.msg);
  return `<li data-lv="${esc(l.level)}"${sep ? ' class="sep"' : ""}><time>${esc(t)}</time><span class="lv">${esc(l.level)}</span><span>${esc(l.msg)}</span></li>`;
}
function logVisible(l) { return LOG.filter === "all" || (LV_RANK[l.level] || 0) >= LV_RANK[LOG.filter]; }
function renderLog() {
  const ol = $("#ld-list"), vis = LOG.lines.filter(logVisible);
  ol.innerHTML = vis.length ? vis.slice(-1500).map(logLi).join("") : '<li class="empty">还没有日志。开始一次对弈或拉取模型后，这里会实时记录每一次请求。</li>';
  ol.scrollTop = ol.scrollHeight;
}
function pushLog(l) {
  LOG.lines.push(l); if (LOG.lines.length > 4000) LOG.lines.splice(0, 1000);
  if (l.level === "ERROR" && !LOG.open) $(".log-badge").hidden = false;
  if (!LOG.open || !logVisible(l)) return;
  const ol = $("#ld-list"), stick = ol.scrollHeight - ol.scrollTop - ol.clientHeight < 60;
  const em = $(".empty", ol); if (em) em.remove();
  ol.insertAdjacentHTML("beforeend", logLi(l));
  if (stick) ol.scrollTop = ol.scrollHeight;
}
function renderLogLive() {
  const box = $("#ld-live"), L = S.session && S.session.live;
  if (!LOG.open) return;
  if (!L || !L.prog || !(S.session && S.session.running)) { box.hidden = true; return; }
  const p = L.prog, idle = p.idleMs > 30000;
  box.hidden = false;
  box.innerHTML = `<dt>当前</dt><dd>第 ${L.index} 轮 · ${L.progRole === "a" ? "A 评判" : "B 作答"} · ${PHASE_CN[p.phase] || p.phase}</dd>
    <dt>已用</dt><dd>${secs(p.elapsedMs)}</dd>
    <dt>收到</dt><dd>正文 ${p.content} 字 · 推理 ${p.reasoning} 字 · ${p.chunks} 块</dd>
    <dt>空闲</dt><dd class="${idle ? "warn" : ""}">${secs(p.idleMs)}${idle ? "（上游暂时没有数据）" : ""}</dd>`;
}
async function openLog() {
  LOG.open = true; $("#logdrawer").hidden = false; $("#btn-log").setAttribute("aria-expanded", "true"); $(".log-badge").hidden = true;
  if (!LOG.loaded) {
    LOG.loaded = true;
    try { const old = (await api("RecentLog", 400)) || []; const seen = new Set(LOG.lines.map((l) => l.t + l.msg)); LOG.lines = old.filter((l) => !seen.has(l.t + l.msg)).concat(LOG.lines); } catch {}
    try { $("#ld-path").textContent = await api("LogDir"); } catch {}
  }
  renderLog(); renderLogLive();
}
function closeLog() { LOG.open = false; $("#logdrawer").hidden = true; $("#btn-log").setAttribute("aria-expanded", "false"); }
function bindLog() {
  on("log", pushLog);
  $("#btn-log").addEventListener("click", () => (LOG.open ? closeLog() : openLog()));
  $("#ld-close").addEventListener("click", closeLog);
  $(".ld-filter").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-lv]"); if (!b) return;
    LOG.filter = b.dataset.lv; $$(".ld-filter button").forEach((x) => x.setAttribute("aria-pressed", String(x === b))); renderLog();
  });
  $("#ld-copy").addEventListener("click", (e) => {
    const txt = LOG.lines.filter(logVisible).map((l) => `${l.t} [${l.level}] ${l.msg}`).join("\n");
    copyText(txt).then((ok) => ok && (toast("日志已复制，可以直接发给我排查"), flipDone(e.currentTarget)));
  });
  $("#ld-open").addEventListener("click", () => api("OpenLogDir").catch((err) => toast(errMsg(err), "err")));
  document.addEventListener("keydown", (e) => {
    if (/INPUT|TEXTAREA/.test(document.activeElement.tagName) || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === "l" || e.key === "L") { e.preventDefault(); LOG.open ? closeLog() : openLog(); }
    if (e.key === "Escape" && LOG.open) closeLog();
  });
}

function bindGlobal() {
  document.addEventListener("click", (e) => {
    const g = e.target.closest("[data-go]"); if (g) go(g.dataset.go);
    const r = e.target.closest(".rail-item"); if (r) selectRound(r.dataset.r);
    const pt = e.target.closest(".chart .pt"); if (pt) selectRound(pt.dataset.r);
    const cp = e.target.closest("[data-copy]");
    if (cp) {
      const r2 = currentRound(), s = S.session; let t = "";
      if (cp.dataset.copy === "b") t = r2 ? r2.output : s && s.live ? s.live.bText : "";
      else t = $("#prompt-out").innerText;
      copyText(t).then((ok) => { if (!ok) return; flipDone(cp); });
    }
  });
  $("#diff-mode").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-mode]"); if (!b || b.disabled) return;
    S.diffMode = b.dataset.mode; renderPrompt();
  });
  $("#btn-stop").addEventListener("click", stop);
  $("#btn-copy-best").addEventListener("click", (e) => {
    const s = S.session, b = s && s.rounds[s.best]; if (!b) return;
    copyText(b.systemPrompt).then((ok) => ok && (toast(`已复制 Round ${pad2(b.index)} 的 System Prompt`), flipDone(e.currentTarget)));
  });
  $("#btn-apply-best").addEventListener("click", () => {
    const s = S.session, b = s && s.rounds[s.best]; if (!b) return;
    S.cfg.brief.systemPrompt = b.systemPrompt; F.system().value = b.systemPrompt; autosize(F.system()); updCount(F.system()); save();
    go("studio"); toast("最佳 Prompt 已带回工作台");
    setTimeout(() => F.system().animate([{ boxShadow: "0 0 0 4px rgba(143,216,255,.4)" }, { boxShadow: "0 0 0 4px rgba(143,216,255,0)" }], { duration: 1200 }), 400);
  });
  $("#btn-export").addEventListener("click", async () => {
    try { const p = await api("ExportSession", S.session.id); if (p) toast("已导出到 " + p); } catch (err) { toast(errMsg(err), "err"); }
  });

  document.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); S.session && S.session.running ? stop() : start(); }
    if ((e.ctrlKey || e.metaKey) && ["1", "2", "3"].includes(e.key)) { e.preventDefault(); go(["studio", "arena", "archive"][+e.key - 1]); }
    if (S.view === "arena" && S.session && !/INPUT|TEXTAREA/.test(document.activeElement.tagName) && (e.key === "ArrowUp" || e.key === "ArrowDown" || e.key === "j" || e.key === "k")) {
      const rs = S.session.rounds; if (!rs.length) return;
      const cur = currentRound(); let i = cur ? rs.indexOf(cur) : rs.length;
      i += e.key === "ArrowDown" || e.key === "j" ? 1 : -1;
      e.preventDefault();
      if (i >= rs.length) selectRound(S.session.live ? "live" : rs[rs.length - 1].index); else selectRound(rs[Math.max(0, i)].index);
    }
  });

  window.addEventListener("resize", raf(() => {
    placeInk($(".nav"), `[data-go="${S.view}"]`);
    $$(".agent .seg").forEach((s) => placeInk(s, '[aria-checked="true"]'));
    placeInk($("#diff-mode"), '[aria-checked="true"]');
    $$("textarea").forEach(autosize);
    if (S.session && S.view === "arena") renderChart();
  }));
}

function flipDone(btn) {
  const use = $("use", btn); if (!use) return;
  const old = use.getAttribute("href");
  use.setAttribute("href", "#i-check"); btn.classList.add("is-done");
  setTimeout(() => { use.setAttribute("href", old); btn.classList.remove("is-done"); }, 1400);
}

/* ═════════ BOOT ═════════ */
async function boot() {
  buildAgents();
  // stagger index for reveal
  $$("#view-studio [data-reveal]").forEach((el, i) => el.style.setProperty("--i", i));
  $$("#view-archive [data-reveal]").forEach((el, i) => el.style.setProperty("--i", i));
  let cfg;
  for (let i = 0; i < 100 && !cfg; i++) {
    try { cfg = await api("GetConfig"); } catch { await new Promise((r) => setTimeout(r, 50)); }
  }
  S.cfgLoaded = !!cfg;
  if (!cfg) setTimeout(() => toast("没能读取已保存的设置，本次修改不会覆盖原文件", "err", 7000), 600);
  S.cfg = cfg || {
    a: { provider: "anthropic", baseURL: DEF.anthropic, apiKey: "", model: "", temperature: 0.3, maxTokens: 4096, stream: true, timeoutSec: 900, stallSec: 180 },
    b: { provider: "openai", baseURL: DEF.openai, apiKey: "", model: "", temperature: 0.7, maxTokens: 4096, stream: true, timeoutSec: 900, stallSec: 180 },
    brief: { systemPrompt: "", userTask: "", criteria: "", directorNotes: "", useCase: "", maxRounds: 10, targetScore: 9, requireExecution: true, evolve: "chain", untilDone: false },
  };
  for (const k of ["a", "b"]) { fillAgent(k); bindAgent(k); }
  fillBrief(); bindBrief(); bindGlobal(); bindArchive(); bindLog();
  on("duet", onDuet);
  on("app_closing", async () => { await saveNow(); window.runtime && window.runtime.Quit(); });
  document.addEventListener("visibilitychange", () => { if (document.hidden) saveNow(); });
  on("sessions_changed", () => { if (S.view === "archive") loadArchive(); });
  setRunning(false);
  go("studio");
  document.fonts && document.fonts.ready.then(() => {
    placeInk($(".nav"), '[aria-selected="true"]');
    $$(".agent .seg").forEach((s) => placeInk(s, '[aria-checked="true"]'));
  });
}

if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot); else boot();
})();
