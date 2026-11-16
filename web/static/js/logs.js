// Logs of one container, or of a stack's containers merged: live, with
// search, a time window, errors only, pause and download.
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

const MAX = 10000; // lines kept in memory
const out = $("#lg-out");
const status = $("#lg-status");
const multi = $$(".lg-ctr input").length > 1;
let lines = []; // {c, t, e, m}
// frozen: the lines shown when paused; filters apply to it, not to what
// keeps coming.
let es = null, frozen = null, query = "", errOnly = false, retry = null;
let names = [];

// ---- ANSI colours (SGR) → classes; other escapes dropped
const SGR = /\x1b\[([0-9;]*)m/g;
const OTHER = /\x1b\[[0-9;?]*[A-La-ln-z]|\x1b\][^\x07]*\x07/g;
function ansi(text) {
  text = text.replace(OTHER, "");
  const parts = [];
  let cls = { fg: "", b: false };
  let last = 0;
  for (const m of text.matchAll(SGR)) {
    if (m.index > last) parts.push([text.slice(last, m.index), { ...cls }]);
    for (const code of (m[1] || "0").split(";").map(Number)) {
      if (code === 0) cls = { fg: "", b: false };
      else if (code === 1) cls.b = true;
      else if (code === 22) cls.b = false;
      else if (code === 39) cls.fg = "";
      else if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97)) cls.fg = "a" + code;
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push([text.slice(last), { ...cls }]);
  return parts;
}

const plain = (m) => m.replace(SGR, "").replace(OTHER, "");
// One formatter for every line: toLocaleTimeString builds one per call.
const clock = new Intl.DateTimeFormat("fr-FR", { hour: "2-digit", minute: "2-digit", second: "2-digit" });

function lineEl(l) {
  const el = document.createElement("div");
  el.className = "ln" + (l.e ? " err" : "");
  const time = document.createElement("span");
  time.className = "t";
  time.textContent = l.t ? clock.format(new Date(l.t)) : "";
  el.append(time);
  if (multi) {
    const n = document.createElement("span");
    n.className = "n c" + (l.c % 8);
    n.textContent = names[l.c];
    el.append(n);
  }
  const msg = document.createElement("span");
  msg.className = "m";
  // Most lines have no colour and no search: their text, without a span each.
  if (!query && !l.m.includes("\x1b")) { msg.textContent = l.m; el.append(msg); return el; }
  for (const [txt, c] of ansi(l.m)) {
    const s = document.createElement("span");
    if (c.fg) s.classList.add(c.fg);
    if (c.b) s.classList.add("b");
    highlight(s, txt);
    msg.append(s);
  }
  el.append(msg);
  return el;
}

// highlight puts the text in s, the matches of the search in <mark>.
function highlight(s, txt) {
  if (!query) { s.textContent = txt; return; }
  const low = txt.toLowerCase();
  let i = 0, j;
  while ((j = low.indexOf(query, i)) >= 0) {
    if (j > i) s.append(txt.slice(i, j));
    const mk = document.createElement("mark");
    mk.textContent = txt.slice(j, j + query.length);
    s.append(mk);
    i = j + query.length;
  }
  if (i < txt.length) s.append(txt.slice(i));
}

const visible = (l) => (!errOnly || l.e) && (!query || plain(l.m).toLowerCase().includes(query));
const atBottom = () => out.scrollHeight - out.scrollTop - out.clientHeight < 40;

function append(batch) {
  const stick = atBottom();
  const frag = document.createDocumentFragment();
  for (const l of batch) if (visible(l)) frag.append(lineEl(l));
  out.append(frag);
  if (stick) out.scrollTop = out.scrollHeight;
}

function redraw() {
  out.replaceChildren();
  append(frozen || lines);
  out.scrollTop = out.scrollHeight;
  count();
}

function count() {
  const shown = out.childElementCount;
  status.textContent = `${shown} ligne${shown > 1 ? "s" : ""}` + (shown !== lines.length ? ` sur ${lines.length}` : "") +
    (frozen ? " · en pause" : es ? " · en direct" : "");
}

// On a phone the container list starts folded: the lines need the height.
const pick = $("#lg-pick");
if (pick && matchMedia("(max-width: 760px)").matches) pick.open = false;

function selected() {
  const boxes = $$(".lg-ctr input");
  const pn = $("#lg-pick-n");
  if (pn) pn.textContent = boxes.filter((x) => x.checked).length + "/" + boxes.length;
  const all = boxes.length ? boxes : $$(".lg-one");
  names = all.map((x) => x.dataset.name);
  return all.map((x, i) => ({ id: x.dataset.id, i, on: boxes.length ? x.checked : true })).filter((x) => x.on);
}

function connect() {
  if (es) es.close();
  es = null;
  clearTimeout(retry);
  lines = [];
  // Paused, the frozen view stays as it is, even through a reconnect.
  if (!frozen) out.replaceChildren();
  const sel = selected();
  if (!sel.length) { count(); return; }
  if (sel.length > 30) { status.textContent = "30 conteneurs au plus à la fois : décoche-en quelques-uns."; return; }
  const q = new URLSearchParams();
  for (const s of sel) q.append("c", s.id);
  const since = $("#lg-since").value;
  if (since === "all") q.set("tail", "all");
  else if (since) q.set("since", since);
  // A stack's history: about 1500 lines in all, not 300 per container.
  else if (sel.length > 1) q.set("tail", Math.max(50, Math.floor(1500 / sel.length)));
  // The server numbers containers in request order: map back to ours.
  const index = sel.map((s) => s.i);
  es = new EventSource("/logs/stream?" + q);
  es.addEventListener("lines", (e) => {
    // Containers' lines come mixed, each history in turn: sort each batch,
    // and put a batch older than what is shown back in time order.
    const byTime = (a, b) => (a.t < b.t ? -1 : a.t > b.t ? 1 : 0);
    const batch = JSON.parse(e.data).map((l) => ({ ...l, c: index[l.c] })).sort(byTime);
    const older = lines.length > 0 && batch.length > 0 && batch[0].t < lines[lines.length - 1].t;
    lines.push(...batch);
    if (older) lines.sort(byTime);
    // Trim by steps, and redraw then: the page shows what is kept.
    const trimmed = lines.length > MAX + 1000;
    if (trimmed) lines = lines.slice(-MAX);
    if (!frozen) {
      if (older || trimmed) redraw();
      else append(batch);
    }
    count();
  });
  es.addEventListener("done", () => { es.close(); es = null; count(); });
  // EventSource would reconnect to the same URL and resend the history on
  // top of what is shown: start over instead.
  es.onerror = () => {
    es.close();
    es = null;
    status.textContent = "Connexion perdue, nouvelle tentative…";
    retry = setTimeout(connect, 3000);
  };
  count();
}

let qTimer;
$("#lg-q").addEventListener("input", (e) => {
  clearTimeout(qTimer);
  qTimer = setTimeout(() => { query = e.target.value.trim().toLowerCase(); redraw(); }, 150);
});
$("#lg-err").addEventListener("change", (e) => { errOnly = e.target.checked; redraw(); });
$("#lg-since").addEventListener("change", connect);
$$(".lg-ctr input").forEach((x) => x.addEventListener("change", connect));
$("#lg-pause").addEventListener("click", (e) => {
  frozen = frozen ? null : lines.slice();
  const b = e.currentTarget;
  $("use", b).setAttribute("href", frozen ? "#i-play" : "#i-pause");
  $("span", b).textContent = frozen ? "Reprendre" : "Pause";
  if (!frozen) redraw(); // in time order, nothing twice
  count();
});
$("#lg-dl").addEventListener("click", () => {
  const text = lines.filter(visible).map((l) =>
    `${l.t || ""} ${multi ? names[l.c] + " " : ""}${l.e ? "[err] " : ""}${plain(l.m)}`).join("\n");
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
  const stamp = new Date().toISOString().slice(0, 19).replace(/:/g, "");
  a.download = `logs-${$("h1 .name").textContent}-${stamp}.log`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
});
addEventListener("pagehide", () => es && es.close());
connect();
