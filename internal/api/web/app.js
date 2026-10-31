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

// Render a diff with added, removed and hunk lines marked. Text only, never HTML.
function renderDetail(pre, text, isDiff) {
  pre.textContent = "";
  if (!isDiff) { pre.textContent = text; return; }
  for (const line of text.split("\n")) {
    const span = document.createElement("span");
    if (line.startsWith("+") && !line.startsWith("+++")) span.className = "add";
    else if (line.startsWith("-") && !line.startsWith("---")) span.className = "del";
    else if (line.startsWith("@@")) span.className = "hunk";
    span.textContent = line + "\n";
    pre.appendChild(span);
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

let tab = "queue";
function showTab(name) {
  tab = name;
  for (const b of document.querySelectorAll("#tabs button")) b.classList.toggle("on", b.dataset.tab === name);
  for (const s of ["queue", "agents", "inbox"]) $(s).hidden = s !== name;
}

// ----- queue

const open = new Set(); // approval ids whose details are expanded
const drafts = new Map(); // approval id -> note text, kept across refreshes

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
  document.title = items.length ? `(${items.length}) Sheaf` : "Sheaf";

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
      renderDetail(wrap.querySelector(".detail"), a.detail, a.kind === "diff");
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

function renderAgents(agents, tasks) {
  const list = $("alist");
  list.textContent = "";
  $("aempty").hidden = agents.length > 0;
  for (const a of agents) {
    const card = clone("tpl-agent");
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
    list.appendChild(card);
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
    call("task.list", { active: true }),
    call("message.inbox", { all: true, peek: true }),
  ]).then(([q, a, t, m]) => { messages = m || []; return [q, a, t]; });
  renderQueue(queue || []);
  renderAgents(agents || [], tasks || []);
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
  for (const s of ["queue", "agents", "inbox"]) $(s).hidden = true;
  $("pair").hidden = false;
  setConn("down", "not paired");
  if (msg) { $("pairerr").textContent = msg; $("pairerr").hidden = false; }
}

function init() {
  for (const b of document.querySelectorAll("#tabs button")) {
    b.addEventListener("click", () => {
      showTab(b.dataset.tab);
      if (b.dataset.tab === "inbox") markInboxRead();
    });
  }
  start();
}

async function start() {
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
