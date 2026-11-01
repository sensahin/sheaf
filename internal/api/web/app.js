// Sheaf web queue. Talks to the daemon's /v1 API with this device's token.
"use strict";

const KEY = "sheaf.token";
const $ = (id) => document.getElementById(id);

function loadToken() {
  const m = location.hash.match(/[#&]t=([^&]+)/);
  if (m) {
    const t = decodeURIComponent(m[1]);
    try { localStorage.setItem(KEY, t); } catch (_) {}
    history.replaceState(null, "", location.pathname);
    return t;
  }
  try { return localStorage.getItem(KEY) || ""; } catch (_) { return ""; }
}

let token = loadToken();

// In the desktop app the page is served inside the app, which hands over its
// own device token, so no pairing link is needed.
async function desktopApp() {
  for (let i = 0; i < 40 && window.WailsInvoke && !window.go?.main?.App; i++) {
    await new Promise((r) => setTimeout(r, 50));
  }
  return window.go?.main?.App || null;
}
let desktop = null;

function setTitle(t) {
  if (document.title === t) return;
  document.title = t;
  desktop?.SetTitle?.(t);
}

class APIError extends Error {
  constructor(status, msg) { super(msg); this.status = status; }
}

async function call(method, body) {
  const r = await fetch("/v1/" + method, {
    method: "POST",
    headers: { "Authorization": "Bearer " + token, "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
    cache: "no-store",
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new APIError(r.status, data.error || r.statusText);
  return data;
}

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 172800) return Math.floor(s / 3600) + "h ago";
  return new Date(iso).toLocaleDateString();
}

function clone(id) { return $(id).content.firstElementChild.cloneNode(true); }

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// Split a unified diff into files. Each file has a path, its header lines and
// its hunk lines; a diff without "diff --git" headers is one unnamed file.
function parseDiff(text) {
  const files = [];
  let cur = null, inHunk = false;
  for (const line of text.replace(/\n$/, "").split("\n")) {
    if (/^# untracked files:?$/.test(line)) {
      // Sheaf lists new files git does not track yet after the diff.
      cur = { path: "", note: "New files not yet added to git", lines: [], add: 0, del: 0 };
      files.push(cur);
      inHunk = true;
      continue;
    }
    if (cur?.note) { if (line.startsWith("# ")) cur.lines.push(line.slice(2)); continue; }
    const m = line.match(/^diff --git a\/(.+?) b\/(.+)$/);
    if (m || !cur) {
      cur = { path: m ? m[2] : "", lines: [], add: 0, del: 0 };
      files.push(cur);
      inHunk = false;
      if (m) continue;
    }
    if (line.startsWith("@@")) inHunk = true;
    if (!inHunk) {
      // File headers: take the path from them when there was no git header.
      const h = line.match(/^(?:\+\+\+ b\/|--- a\/|\+\+\+ |--- )(.+)$/);
      if (h && h[1] !== "/dev/null") cur.path = cur.path || h[1];
      if (h || /^(index |new file mode|deleted file mode|old mode|new mode|similarity index|rename from|rename to|Binary files)/.test(line)) continue;
    }
    if (line.startsWith("+")) cur.add++;
    else if (line.startsWith("-")) cur.del++;
    cur.lines.push(line);
  }
  return files.filter((f) => f.lines.length || f.path);
}

// Render a diff file by file, with old and new line numbers. Text only,
// never HTML.
function renderDiff(box, text, openFiles) {
  box.textContent = "";
  const files = parseDiff(text);
  const changed = files.filter((f) => !f.note);
  const total = changed.reduce((n, f) => [n[0] + f.add, n[1] + f.del], [0, 0]);
  if (changed.length > 1) {
    box.appendChild(el("p", "diffstat", `${changed.length} files changed, +${total[0]} −${total[1]}`));
  }
  for (const f of files) {
    const d = el("details", "file");
    d.open = files.length <= 8 || openFiles.has(f.path);
    d.addEventListener("toggle", () => d.open ? openFiles.add(f.path) : openFiles.delete(f.path));
    const sum = el("summary");
    if (f.note) {
      sum.append(el("span", "path", f.note), el("span", "stat", String(f.lines.length)));
      d.appendChild(sum);
      const ul = el("ul", "untracked");
      for (const p of f.lines) ul.appendChild(el("li")).appendChild(el("code", "", p));
      d.appendChild(ul);
      box.appendChild(d);
      continue;
    }
    sum.append(el("code", "path", f.path || "changes"), el("span", "stat add", "+" + f.add), el("span", "stat del", "−" + f.del));
    d.appendChild(sum);
    const body = el("div", "diff");
    let o = 0, n = 0;
    for (const line of f.lines) {
      const h = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      let cls = "ctx", a = "", b = "";
      if (h) { o = +h[1]; n = +h[2]; cls = "hunk"; }
      else if (line.startsWith("+")) { cls = "add"; b = n++; }
      else if (line.startsWith("-")) { cls = "del"; a = o++; }
      else if (line.startsWith("\\")) { cls = "hunk"; }
      else { a = o++; b = n++; }
      const row = el("div", "line " + cls);
      row.append(el("span", "ln", String(a)), el("span", "ln", String(b)), el("span", "code", line));
      body.appendChild(row);
    }
    d.appendChild(body);
    box.appendChild(d);
  }
}

function setConn(state, label) {
  const c = $("conn");
  c.className = "conn " + state;
  c.textContent = label;
  c.title = label;
}

function showError(card, err) {
  const e = card.querySelector(".err");
  e.textContent = err.message;
  e.hidden = false;
}

// ----- tabs

const SECTIONS = ["queue", "agents", "tasks", "inbox"];
let tab = "queue";
function showTab(name) {
  tab = name;
  for (const b of document.querySelectorAll("#tabs button")) b.classList.toggle("on", b.dataset.tab === name);
  for (const s of SECTIONS) $(s).hidden = s !== name;
}

// ----- queue

const open = new Set(); // approval ids whose details are expanded
const drafts = new Map(); // approval id -> note text, kept across refreshes
const openFiles = new Set(); // diff files expanded by hand

let queueKey = "";
function renderQueue(items) {
  // Re-render only when the queue changed, so typing a note is never
  // interrupted by unrelated activity.
  const key = items.map((a) => a.id).join(",");
  if (key === queueKey) return;
  queueKey = key;
  const focused = document.activeElement?.closest?.(".approval")?.dataset.id;
  const list = $("qlist");
  list.textContent = "";
  $("qempty").hidden = items.length > 0;
  $("qcount").hidden = items.length === 0;
  $("qcount").textContent = items.length;
  setTitle(items.length ? `(${items.length}) Sheaf` : "Sheaf");

  for (const a of items) {
    const card = clone("tpl-approval");
    card.dataset.id = a.id;
    card.querySelector(".kind").textContent = a.kind;
    card.querySelector(".who").textContent = a.agent + (a.task ? " · " + a.task : "");
    card.querySelector(".age").textContent = ago(a.created_at);
    card.querySelector(".summary").textContent = a.summary;

    if (a.detail && a.detail !== a.summary) {
      const wrap = card.querySelector(".detail-wrap");
      wrap.hidden = false;
      wrap.querySelector("summary").textContent = a.kind === "diff" ? "Diff" : "Details";
      if (a.kind === "diff") {
        wrap.querySelector(".detail").hidden = true;
        renderDiff(wrap.querySelector(".files"), a.detail, openFiles);
      } else {
        wrap.querySelector(".detail").textContent = a.detail;
      }
      wrap.open = open.has(a.id) || a.kind === "diff";
      wrap.addEventListener("toggle", () => wrap.open ? open.add(a.id) : open.delete(a.id));
    }

    const note = card.querySelector(".note");
    note.value = drafts.get(a.id) || "";
    note.addEventListener("input", () => drafts.set(a.id, note.value));

    const buttons = card.querySelectorAll("button");
    const decide = async (status) => {
      if (status === "answered" && !note.value.trim()) { showError(card, new Error("Write an answer first.")); return; }
      buttons.forEach((b) => (b.disabled = true));
      try {
        await call("approval.decide", { id: a.id, status, answer: note.value.trim() });
        drafts.delete(a.id);
        refresh();
      } catch (err) {
        buttons.forEach((b) => (b.disabled = false));
        showError(card, err);
      }
    };
    if (a.kind === "question") {
      card.querySelector(".approve").hidden = true;
      card.querySelector(".deny").hidden = true;
      card.querySelector(".answer").hidden = false;
      note.placeholder = "Your answer";
    }
    card.querySelector(".approve").addEventListener("click", () => decide("approved"));
    card.querySelector(".deny").addEventListener("click", () => decide("denied"));
    card.querySelector(".answer").addEventListener("click", () => decide("answered"));
    list.appendChild(card);
    if (a.id === focused) note.focus();
  }
}

// ----- agents

let running = {}; // agent name -> its tmux window names
const openTerms = new Set(); // agents whose terminal is expanded
const typed = new Map(); // agent name -> text being typed
const termWindow = new Map(); // agent name -> the window shown, when it has several

// The task of the window shown for an agent, if it has several.
function termTask(name) {
  const w = termWindow.get(name) || "";
  return w.includes(":") ? w.slice(w.indexOf(":") + 1) : undefined;
}
let agentsKey = "";

function renderAgents(agents, tasks) {
  const key = JSON.stringify([agents.map((a) => [a.name, a.state, a.note]), tasks.map((t) => [t.id, t.owner, t.status, t.title]), running]);
  if (key === agentsKey) return;
  agentsKey = key;
  const list = $("alist");
  list.textContent = "";
  $("aempty").hidden = agents.length > 0;
  for (const a of agents) {
    const card = clone("tpl-agent");
    card.dataset.name = a.name;
    card.querySelector(".dot").classList.add(a.state);
    card.querySelector(".dot").title = a.state;
    card.querySelector(".name").textContent = a.name;
    card.querySelector(".kind").textContent = a.state;
    card.querySelector(".note").textContent = a.note || "";
    const ul = card.querySelector(".tasks");
    for (const t of tasks.filter((t) => t.owner === a.name)) {
      const li = document.createElement("li");
      const id = document.createElement("code");
      id.textContent = t.id;
      const title = document.createElement("span");
      title.textContent = t.title + " (" + t.status + ")";
      li.append(id, title);
      ul.appendChild(li);
    }
    const isRunning = (running[a.name] || []).length > 0;
    card.querySelector(".run").hidden = !isRunning;
    const term = card.querySelector(".term");
    term.hidden = !isRunning;
    if (isRunning) wireTerminal(card, a.name, term);
    list.appendChild(card);
  }
}

function wireTerminal(card, name, term) {
  const wins = running[name] || [];
  if (!wins.includes(termWindow.get(name))) termWindow.delete(name);
  if (wins.length > 1) {
    const sel = el("select", "win");
    for (const w of wins) sel.appendChild(el("option", "", w)).value = w;
    sel.value = termWindow.get(name) || wins[0];
    termWindow.set(name, sel.value);
    sel.addEventListener("change", () => { termWindow.set(name, sel.value); loadScreen(name); });
    term.querySelector(".screen").before(sel);
  }
  const input = term.querySelector("input");
  input.value = typed.get(name) || "";
  input.addEventListener("input", () => typed.set(name, input.value));
  term.open = openTerms.has(name);
  term.addEventListener("toggle", () => {
    if (term.open) { openTerms.add(name); pollScreen(name); } else openTerms.delete(name);
  });
  const send = async (text, enter) => {
    term.querySelector(".err").hidden = true;
    try {
      await call("agent.type", { name, task: termTask(name), text, enter });
      if (text) { input.value = ""; typed.delete(name); }
      setTimeout(() => loadScreen(name), 600);
    } catch (err) {
      showError(term, err);
    }
  };
  term.querySelector("form").addEventListener("submit", (e) => { e.preventDefault(); send(input.value, true); });
  term.querySelector(".enter").addEventListener("click", () => send("", true));
  if (term.open) loadScreen(name);
}

async function loadScreen(name) {
  const pre = document.querySelector(`#alist .card[data-name="${CSS.escape(name)}"] .screen`);
  if (!pre) return;
  try {
    const s = await call("agent.screen", { name, task: termTask(name), lines: 80 });
    const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
    if (pre.textContent !== s.text) {
      pre.textContent = s.text;
      if (atBottom || !pre.dataset.seen) pre.scrollTop = pre.scrollHeight;
      pre.dataset.seen = "1";
    }
  } catch (err) {
    pre.textContent = err.message;
  }
}

const polling = new Set();
async function pollScreen(name) {
  if (polling.has(name)) return;
  polling.add(name);
  while (openTerms.has(name) && tab === "agents" && !document.hidden) {
    await loadScreen(name);
    await new Promise((r) => setTimeout(r, 2000));
  }
  polling.delete(name);
}

// ----- tasks

let allTasks = [];
let tasksKey = "";
function renderTasks() {
  const activeOnly = $("tactive").checked;
  const items = allTasks.filter((t) => !activeOnly || ["open", "working", "blocked"].includes(t.status));
  const key = activeOnly + ":" + items.map((t) => t.id + t.status + t.owner + t.updated_at).join(",") + JSON.stringify(running);
  if (key === tasksKey) return;
  tasksKey = key;
  const list = $("tlist");
  list.textContent = "";
  $("tempty").hidden = items.length > 0;
  for (const t of items) {
    const card = clone("tpl-task");
    const st = card.querySelector(".status");
    st.textContent = t.status;
    st.classList.add(t.status);
    card.querySelector(".who").textContent = (t.owner || "unassigned") + " · " + t.id;
    card.querySelector(".age").textContent = ago(t.updated_at);
    card.querySelector(".title").textContent = t.title;
    card.querySelector(".detail-text").textContent = t.detail || "";
    const dl = card.querySelector(".facts");
    for (const [k, v] of [["Branch", t.branch], ["Worktree", t.worktree], ["Repo", t.repo], ["Parent", t.parent]]) {
      if (!v) continue;
      dl.append(el("dt", "", k), el("dd"));
      dl.lastChild.appendChild(el("code", "", v));
    }
    const finished = t.status === "done" || t.status === "cancelled";
    const start = card.querySelector(".start");
    start.hidden = finished || !t.owner;
    if ((running[t.owner] || []).includes(t.owner + ":" + t.id)) start.textContent = "Running";
    start.disabled = start.textContent === "Running";
    start.addEventListener("click", async () => {
      start.disabled = true;
      try {
        await call("task.start", { id: t.id });
        tasksKey = "";
        refresh();
      } catch (err) {
        start.disabled = false;
        showError(card, err);
      }
    });
    card.querySelector(".done").hidden = finished;
    card.querySelector(".cancel").hidden = finished;
    card.querySelector(".reopen").hidden = !finished;
    const buttons = card.querySelectorAll("button");
    const move = async (status) => {
      buttons.forEach((b) => (b.disabled = true));
      try {
        await call("task.set_status", { id: t.id, status });
        refresh();
      } catch (err) {
        buttons.forEach((b) => (b.disabled = false));
        showError(card, err);
      }
    };
    card.querySelector(".done").addEventListener("click", () => move("done"));
    card.querySelector(".cancel").addEventListener("click", () => move("cancelled"));
    card.querySelector(".reopen").addEventListener("click", () => move("open"));
    list.appendChild(card);
  }
}

// ----- new task

let repos = [];
function renderNewTask(agents) {
  const owner = $("towner");
  const names = agents.map((a) => a.name);
  if ([...owner.options].map((o) => o.value).join() !== ["", ...names].join()) {
    const keep = owner.value;
    owner.textContent = "";
    owner.appendChild(el("option", "", "Nobody yet")).value = "";
    for (const n of names) owner.appendChild(el("option", "", n)).value = n;
    owner.value = names.includes(keep) ? keep : (names[0] || "");
  }
  const repo = $("trepo");
  const paths = repos.map((r) => r.path);
  if ([...repo.options].map((o) => o.value).join() !== ["", ...paths].join()) {
    const keep = repo.value;
    repo.textContent = "";
    repo.appendChild(el("option", "", "None")).value = "";
    for (const r of repos) {
      const o = el("option", "", r.name);
      o.value = r.path;
      o.title = r.path;
      repo.appendChild(o);
    }
    repo.value = paths.includes(keep) ? keep : (paths[0] || "");
  }
  const hint = $("thint");
  hint.hidden = repos.length > 0;
  hint.textContent = "No repositories yet. On the server, run `sheaf repo add DIR` to offer one here.";
  syncNewTask();
}

function syncNewTask() {
  const hasRepo = !!$("trepo").value;
  $("twt").disabled = !hasRepo;
  if (!hasRepo) $("twt").checked = false;
  $("tstart").disabled = !$("towner").value;
  if (!$("towner").value) $("tstart").checked = false;
}

async function createTask(e) {
  e.preventDefault();
  const form = $("tform");
  const err = form.querySelector(".err");
  err.hidden = true;
  const title = $("ttitle").value.trim();
  if (!title) return;
  const btn = form.querySelector("button[type=submit]");
  btn.disabled = true;
  try {
    const t = await call("task.create", {
      title,
      detail: $("tdetail").value.trim() || undefined,
      owner: $("towner").value || undefined,
      repo: $("trepo").value || undefined,
      worktree: $("twt").checked,
      start: $("tstart").checked,
    });
    $("ttitle").value = "";
    $("tdetail").value = "";
    if (t.start_error) showError(form, new Error(`Created ${t.id}, but ${t.owner} did not start: ${t.start_error}`));
    else $("newtask").open = false;
    tasksKey = "";
    refresh();
  } catch (e2) {
    showError(form, e2);
  } finally {
    btn.disabled = false;
  }
}

// ----- compose

function renderCompose(agents) {
  const sel = $("cto");
  const names = agents.map((a) => a.name);
  $("compose").hidden = names.length === 0;
  if ([...sel.options].map((o) => o.value).join() === names.join()) return;
  const keep = sel.value;
  sel.textContent = "";
  for (const n of names) sel.appendChild(el("option", "", n));
  if (names.includes(keep)) sel.value = keep;
}

async function sendCompose(e) {
  e.preventDefault();
  const form = $("compose");
  const body = $("cbody").value.trim();
  form.querySelector(".err").hidden = true;
  if (!body || !$("cto").value) return;
  const btn = form.querySelector("button");
  btn.disabled = true;
  try {
    await call("message.send", { to: $("cto").value, body });
    $("cbody").value = "";
  } catch (err) {
    showError(form, err);
  } finally {
    btn.disabled = false;
  }
}

// ----- inbox

// The page shows recent messages without consuming them, and marks them read
// only while the Inbox tab is open, so the CLI inbox still sees what you have
// not looked at.
let messages = [];

let inboxKey = "";
const replies = new Map(); // message id -> reply draft
function renderInbox() {
  const key = messages.map((m) => m.id + (m.read_at ? "r" : "")).join(",");
  if (key === inboxKey) return;
  inboxKey = key;
  const list = $("mlist");
  list.textContent = "";
  $("mempty").hidden = messages.length > 0;
  const unread = messages.filter((m) => !m.read_at).length;
  $("icount").hidden = unread === 0;
  $("icount").textContent = unread;
  for (const m of [...messages].reverse()) {
    const card = clone("tpl-message");
    card.querySelector(".kind").textContent = m.kind;
    card.querySelector(".who").textContent = "from " + m.from + (m.task ? " · " + m.task : "");
    card.querySelector(".age").textContent = ago(m.created_at);
    card.querySelector(".body").textContent = m.body;
    const form = card.querySelector(".reply");
    if (m.from === "human") form.hidden = true;
    const draft = form.querySelector("textarea");
    draft.value = replies.get(m.id) || "";
    draft.addEventListener("input", () => replies.set(m.id, draft.value));
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const ta = form.querySelector("textarea");
      const body = ta.value.trim();
      if (!body) return;
      form.querySelector("button").disabled = true;
      try {
        await call("message.send", { to: m.from, body, task: m.task || undefined });
        ta.value = "";
        replies.delete(m.id);
        form.hidden = true;
      } catch (err) {
        showError(card, err);
      } finally {
        form.querySelector("button").disabled = false;
      }
    });
    list.appendChild(card);
  }
}

async function markInboxRead() {
  if (messages.some((m) => !m.read_at)) await call("message.inbox", {});
}

// ----- refresh loop

async function refresh() {
  const [queue, agents, tasks] = await Promise.all([
    call("approval.list", {}),
    call("agent.list", {}),
    call("task.list", {}),
    call("message.inbox", { all: true, peek: true }),
    call("agent.running", {}).catch(() => ({})),
    call("repo.list", {}).catch(() => []),
  ]).then(([q, a, t, m, r, rp]) => { messages = m || []; running = r || {}; repos = rp || []; return [q, a || [], t || []]; });
  allTasks = tasks;
  renderNewTask(agents);
  renderQueue(queue || []);
  renderAgents(agents, tasks.filter((t) => ["open", "working", "blocked"].includes(t.status)));
  renderTasks();
  renderCompose(agents);
  renderInbox();
  if (tab === "inbox") markInboxRead();
}

let lastEvent = 0;
async function follow() {
  for (;;) {
    try {
      const evs = await call("events.list", { after: lastEvent, limit: 500, wait_seconds: 25 });
      setConn("live", "live");
      if (evs && evs.length) {
        lastEvent = evs[evs.length - 1].id;
        await refresh();
      }
    } catch (err) {
      if (err instanceof APIError && err.status === 403) return unpaired("This device is no longer paired.");
      setConn("down", "offline");
      await new Promise((r) => setTimeout(r, 3000));
    }
  }
}

function unpaired(msg) {
  try { localStorage.removeItem(KEY); } catch (_) {}
  token = "";
  $("tabs").hidden = true;
  for (const s of SECTIONS) $(s).hidden = true;
  $("pair").hidden = false;
  setConn("down", "not paired");
  if (desktop && msg) msg += " Quit and reopen Sheaf to pair it again.";
  if (msg) { $("pairerr").textContent = msg; $("pairerr").hidden = false; }
}

function init() {
  for (const b of document.querySelectorAll("#tabs button")) {
    b.addEventListener("click", () => {
      showTab(b.dataset.tab);
      if (b.dataset.tab === "inbox") markInboxRead();
      if (b.dataset.tab === "agents") for (const n of openTerms) pollScreen(n);
    });
  }
  $("tactive").addEventListener("change", renderTasks);
  $("tform").addEventListener("submit", createTask);
  $("trepo").addEventListener("change", () => { if ($("trepo").value) $("twt").checked = true; syncNewTask(); });
  $("towner").addEventListener("change", () => { if ($("towner").value) $("tstart").checked = true; syncNewTask(); });
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && tab === "agents") for (const n of openTerms) pollScreen(n);
  });
  $("compose").addEventListener("submit", sendCompose);
  $("cbody").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) sendCompose(e);
  });
  start();
}

async function start() {
  desktop = await desktopApp();
  if (desktop?.Token) {
    try { token = await desktop.Token(); } catch (_) {}
  }
  if (!token) return unpaired();
  try {
    const who = await call("whoami");
    if (who.name !== "human") return unpaired("That token belongs to an agent, not to you.");
    lastEvent = (await call("events.head")).id;
    $("tabs").hidden = false;
    showTab("queue");
    await refresh();
    setConn("live", "live");
    follow();
  } catch (err) {
    if (err instanceof APIError && err.status === 403) return unpaired("This pairing link has been revoked or is wrong.");
    setConn("down", "offline");
    setTimeout(start, 3000);
  }
}

document.addEventListener("DOMContentLoaded", init);
