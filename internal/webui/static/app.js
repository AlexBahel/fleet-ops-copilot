// Copyright 2026 Alex Bahel. Licensed under the Apache License, Version 2.0.
//
// Chat with fleetops-agent and list ApprovalRequests. Every value from the server
// is inserted as text, never as HTML. The page has no approve or reject
// action: it shows the command a person runs with their own kubeconfig.
"use strict";

const $ = (id) => document.getElementById(id);

function el(tag, props, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else e.setAttribute(k, v);
  }
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

function store(kind) {
  try { return window[kind]; } catch (_) { return null; }
}
function load(kind, key) { try { return store(kind)?.getItem(key) || ""; } catch (_) { return ""; } }
function save(kind, key, v) { try { store(kind)?.setItem(key, v); } catch (_) { /* storage blocked */ } }
function drop(kind, key) { try { store(kind)?.removeItem(key); } catch (_) { /* storage blocked */ } }

let sessionId = load("sessionStorage", "fleetops.session");
// The conversation id is kept in this page's memory only (D75): a reload
// starts a new conversation.
let conversationId = "";

function showSession() {
  $("session").textContent = sessionId ? "session " + sessionId : "new session";
}

function fmtTime(s) {
  const d = new Date(s);
  return isNaN(d) ? s : d.toLocaleString();
}

// ---- chat -----------------------------------------------------------------

function addMessage(cls, text) {
  const li = el("li", { class: "msg " + cls, text });
  $("log").append(li);
  li.scrollIntoView({ block: "end" });
  return li;
}

function chips(calls) {
  const foot = el("div", { class: "foot" });
  for (const c of calls || []) {
    let cls = "chip";
    let label = c.name;
    if (c.isError) cls += " err";
    if (c.policyCode) {
      label += " · " + c.policyCode;
      cls += c.refused ? " refused" : " approval";
    }
    if (c.created) label += " → " + c.created;
    foot.append(el("span", { class: cls, text: label }));
  }
  return foot;
}

// The server's words (webui.NameRefusal) for a missing or malformed name.
const NAME_REFUSAL = "Type your name in the field at the top (for example sam), then send again.";

// refuseName highlights the name field and says what to do; the message
// stays in its box to send again.
function refuseName() {
  const f = $("operator");
  f.classList.add("invalid");
  f.setAttribute("aria-invalid", "true");
  f.focus();
  addMessage("error", NAME_REFUSAL);
}

async function send(ev) {
  ev.preventDefault();
  const field = $("operator");
  field.value = field.value.trim();
  const operator = field.value;
  const message = $("message").value.trim();
  // The field's pattern is the server's rule; an empty value passes a
  // pattern, so it is checked on its own.
  if (!operator || !field.checkValidity()) { refuseName(); return; }
  if (!message) return;
  save("localStorage", "fleetops.operator", operator);
  addMessage("user", message);
  $("message").value = "";
  $("send").disabled = true;
  const wait = addMessage("pending", "Working…");
  try {
    const res = await fetch("api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operator, sessionId, conversationId, message }),
    });
    const out = await res.json();
    wait.remove();
    if (out.sessionId) { sessionId = out.sessionId; save("sessionStorage", "fleetops.session", sessionId); showSession(); }
    if (out.conversationId) conversationId = out.conversationId;
    if (out.notice) addMessage("pending", out.notice);
    if (out.requester) $("requester").textContent = "Requests from this page are recorded for " + out.requester + " and cannot be approved; a person can only reject them.";
    if (out.field === "operator") {
      $("message").value = message;
      refuseName();
    } else if (out.error) {
      const li = addMessage("error", out.error);
      if (out.toolCalls?.length) li.append(chips(out.toolCalls));
    } else {
      const li = addMessage("agent", out.answer || "(no answer)");
      const foot = chips(out.toolCalls);
      foot.append(el("span", { class: "hint", text: `${out.turns} model turn(s), ${Math.round(out.latencyMs)} ms` }));
      li.append(foot);
    }
    refresh();
  } catch (e) {
    wait.remove();
    addMessage("error", "Request failed: " + e);
  } finally {
    $("send").disabled = false;
  }
}

// ---- requests ---------------------------------------------------------------

function command(label, text) {
  const btn = el("button", { type: "button", class: "secondary", text: "Copy" });
  btn.addEventListener("click", async () => {
    try { await navigator.clipboard.writeText(text); btn.textContent = "Copied"; }
    catch (_) { btn.textContent = "Select and copy"; }
    setTimeout(() => { btn.textContent = "Copy"; }, 1500);
  });
  return el("div", {}, el("div", { class: "label", text: label }), el("div", { class: "cmd" }, el("code", { text }), btn));
}

function card(r) {
  const c = r.change || {};
  const meta = el("dl", {},
    el("dt", { text: "requested for" }), el("dd", { text: r.requestedFor }),
    el("dt", { text: "created" }), el("dd", { text: fmtTime(r.created) }),
    el("dt", { text: "expires" }), el("dd", { text: fmtTime(r.expiresAt) }),
    el("dt", { text: "policy" }), el("dd", { text: [r.policy?.code, r.policy?.message].filter(Boolean).join(": ") }),
    r.decidedBy ? el("dt", { text: "decided by" }) : null, r.decidedBy ? el("dd", { text: r.decidedBy }) : null,
    r.message ? el("dt", { text: "status" }) : null, r.message ? el("dd", { text: r.message }) : null,
  );
  const diff = el("pre", { class: "diff" });
  for (const l of c.diff || []) {
    const cls = l.op === "+" ? "add" : l.op === "-" ? "del" : "";
    diff.append(el("span", { class: cls, text: l.op + " " + l.text }));
  }
  const parts = [
    el("h3", {}, r.name, el("span", { class: "badge", text: r.type }), el("span", { class: "badge " + r.state, text: r.state })),
    meta,
    el("div", {},
      el("div", { class: "label", text: r.state === "Pending" ? "Change if approved" : "Change" }),
      el("div", { class: "hint", text: `${c.operation || ""} ${c.target || ""} — by ${c.writer || ""}` })),
    c.diff?.length ? diff : null,
    c.body ? el("details", {}, el("summary", { text: "request body" }), el("pre", { text: c.body })) : null,
  ];
  if (c.notes?.length) {
    const ul = el("ul", { class: "notes" });
    for (const n of c.notes) ul.append(el("li", { text: n }));
    parts.push(ul);
  }
  if (r.unapprovable) parts.push(el("p", { class: "notice", text: r.unapprovable }));
  if (r.approve) parts.push(command("Approve (run it yourself)", r.approve));
  if (r.reject) parts.push(command("Reject (run it yourself)", r.reject));
  if (r.instruction) parts.push(command("After approval, the approver runs", r.instruction));
  else if (c.command && r.state === "Pending" && r.approve) parts.push(command("After approval, the approver runs", c.command));
  return el("article", { class: "card" }, ...parts);
}

let refreshing = false;
let shown = "";
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    const res = await fetch("api/requests");
    const out = await res.json();
    if (out.error) { $("requests-status").textContent = "Cannot read ApprovalRequests: " + out.error; return; }
    const showAll = $("show-decided").checked;
    const items = (out.items || []).filter((r) => showAll || r.state === "Pending");
    const pending = (out.items || []).filter((r) => r.state === "Pending").length;
    $("requests-status").textContent = `${pending} pending in ${(out.namespaces || []).join(", ")} · updated ${new Date().toLocaleTimeString()}`;
    // Rebuild the cards only when something changed, so an open request
    // body or a selection survives the poll.
    const key = JSON.stringify([showAll, items]);
    if (key === shown) return;
    shown = key;
    const box = $("requests");
    box.replaceChildren(...items.map(card));
    if (!items.length) box.append(el("p", { class: "hint", text: showAll ? "No requests." : "No pending requests." }));
  } catch (e) {
    $("requests-status").textContent = "Cannot reach fleetops-agent: " + e;
  } finally {
    refreshing = false;
  }
}

// ---- start ------------------------------------------------------------------

document.addEventListener("DOMContentLoaded", async () => {
  $("operator").value = load("localStorage", "fleetops.operator");
  showSession();
  $("ask").addEventListener("submit", send);
  $("operator").addEventListener("input", () => {
    $("operator").classList.remove("invalid");
    $("operator").removeAttribute("aria-invalid");
  });
  // The field keeps the last name used; typing must replace it, not add to it.
  // Its whole text is selected when it gets focus, by Tab or by a click. A click
  // places the caret on mouseup, which would clear that selection, so the mouseup
  // of the click that gave the field focus is cancelled; later clicks place the
  // caret as usual.
  const name = $("operator");
  let focusingClick = false;
  name.addEventListener("mousedown", () => {
    focusingClick = document.activeElement !== name;
  });
  name.addEventListener("focus", () => {
    name.select();
  });
  name.addEventListener("mouseup", (e) => {
    if (focusingClick) { e.preventDefault(); focusingClick = false; }
  });
  $("message").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); $("ask").requestSubmit(); }
  });
  $("new-session").addEventListener("click", () => {
    sessionId = ""; drop("sessionStorage", "fleetops.session"); showSession();
    conversationId = "";
    $("log").replaceChildren();
  });
  $("show-decided").addEventListener("change", refresh);
  try {
    const info = await (await fetch("api/info")).json();
    $("info").textContent = `backend ${info.backend} · namespaces ${(info.namespaces || []).join(", ")}`;
  } catch (_) { /* header stays empty */ }
  refresh();
  setInterval(refresh, 5000);
});
