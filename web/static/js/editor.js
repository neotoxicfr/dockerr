// Compose editor: stack page (edit, validate, diff, save) and new stack page.
import { mount } from "/static/vendor/cm.js";

const $ = (s, el = document) => el.querySelector(s);
const { toast, post } = window.dockerr;

const root = $("#compose-editor") || $("#new-stack");
const isNew = root.id === "new-stack";
const stack = root.dataset.stack;
const editable = isNew || root.dataset.editable === "1";
const status = $("#ed-status");

const NEW_TEMPLATE = "services:\n  app:\n    image: \n    restart: unless-stopped\n";
let saved = { compose: "", env: "" };
let cur = { compose: "", env: "" };
let file = "compose";
let ed;
let issues = [];

function setStatus(kind, text) {
  status.className = "status" + (kind ? " " + kind : "");
  const icon = kind === "ok" ? "check" : kind === "bad" ? "alert" : "";
  status.innerHTML = icon ? `<svg class="i s"><use href="#i-${icon}"/></svg><span></span>` : "<span></span>";
  status.lastElementChild.textContent = text;
  status.title = text;
}

function dirty() { return cur.compose !== saved.compose || cur.env !== saved.env; }

function updateButtons() {
  if (isNew) return;
  if (!editable) return;
  const d = dirty();
  $("#ed-reset").disabled = !d;
  $("#ed-save").disabled = !d || issues.length > 0;
  $("#ed-apply").disabled = issues.length > 0 || root.dataset.composeOk !== "1";
  $("#ed-apply").textContent = d ? "Enregistrer et appliquer" : "Appliquer";
}

let vTimer;
function scheduleValidate() {
  clearTimeout(vTimer);
  vTimer = setTimeout(validate, 450);
}

async function validate() {
  const url = isNew ? null : `/s/${encodeURIComponent(stack)}/validate`;
  let r;
  if (url) {
    r = await post(url, { compose: cur.compose, env: cur.env }, true);
  } else {
    r = await post("/new/validate", { compose: cur.compose, env: cur.env, name: $("#ns-name").value }, true);
  }
  issues = (r.body && r.body.issues) || [];
  showIssues();
  updateButtons();
}

function showIssues() {
  if (issues.length) {
    const p = issues[0];
    setStatus("bad", (p.line ? `Ligne ${p.line} : ` : "") + p.message);
  } else if (!isNew && !dirty()) {
    setStatus("", "");
  } else {
    setStatus("ok", "Valide");
  }
  if (ed) ed.mark(file === "compose" ? issues : []);
}

function switchFile(f) {
  file = f;
  for (const b of root.querySelectorAll("[data-file]")) b.setAttribute("aria-selected", b.dataset.file === f);
  if (ed) ed.destroy();
  ed = mount($("#editor"), cur[f], {
    nonce: document.querySelector('meta[name="csp-nonce"]')?.content,
    lang: f === "compose" ? "yaml" : "env",
    readOnly: !editable,
    onChange: (v) => { cur[file] = v; updateButtons(); scheduleValidate(); },
  });
  showIssues();
}

root.addEventListener("click", (e) => {
  const b = e.target.closest("[data-file]");
  if (b) switchFile(b.dataset.file);
});

function renderDiff(d) {
  const box = $("#diff-body");
  box.innerHTML = "";
  for (const [name, lines] of [["compose", d.compose], [".env", d.env]]) {
    if (!lines || !lines.some((l) => l && l.Op !== 32)) continue;
    const t = document.createElement("div");
    t.className = "diff-title";
    t.textContent = name;
    const pre = document.createElement("pre");
    pre.className = "diff";
    for (const l of lines) {
      const row = document.createElement("div");
      if (!l) { row.className = "gap"; row.textContent = "⋯"; pre.append(row); continue; }
      const op = String.fromCharCode(l.Op);
      row.className = op === "+" ? "add" : op === "-" ? "del" : "";
      const ln = document.createElement("span");
      ln.className = "ln";
      ln.textContent = l.New || l.Old;
      row.append(ln, `${op} ${l.Text}`);
      pre.append(row);
    }
    box.append(t, pre);
  }
}

async function save(apply) {
  if (dirty()) {
    const d = await post(`/s/${encodeURIComponent(stack)}/diff`, cur, true);
    if (!d.ok) return toast(d.body.error || "Échec", true);
    renderDiff(d.body);
    const dlg = $("#diff-dialog");
    $("#diff-ok").textContent = apply ? "Enregistrer et appliquer" : "Enregistrer";
    dlg.returnValue = "";
    dlg.showModal();
    const ok = await new Promise((res) => dlg.addEventListener("close", () => res(dlg.returnValue === "yes"), { once: true }));
    if (!ok) return;
  }
  const r = await post(`/s/${encodeURIComponent(stack)}/save`, { ...cur, apply }, true);
  if (r.status === 422) { issues = r.body.issues || []; showIssues(); return; }
  if (!r.ok) return toast(r.body.error || "Échec", true);
  saved = { ...cur };
  updateButtons();
  showIssues();
  if (r.body.id) {
    sessionStorage.setItem("dockerr-op", JSON.stringify({ id: r.body.id, stack, op: "start" }));
    location.href = `/s/${encodeURIComponent(stack)}`;
  } else {
    toast("Enregistré");
  }
}

async function init() {
  if (isNew) {
    saved = { compose: "", env: "" };
    cur = { compose: NEW_TEMPLATE, env: "" };
    switchFile("compose");
    $("#ns-create").onclick = () => create(false);
    $("#ns-start").onclick = () => create(true);
    if (root.dataset.composeOk !== "1") $("#ns-start").disabled = true;
    $("#ns-name").focus();
    return;
  }
  const r = await fetch(`/s/${encodeURIComponent(stack)}/files`);
  const body = await r.json();
  if (!r.ok) { setStatus("bad", body.error || "Lecture impossible"); return; }
  saved = { compose: body.compose, env: body.env };
  cur = { ...saved };
  switchFile("compose");
  updateButtons();
  if (!editable) return;
  $("#ed-reset").onclick = () => { cur = { ...saved }; issues = []; switchFile(file); updateButtons(); };
  $("#ed-save").onclick = () => save(false);
  $("#ed-apply").onclick = () => save(true);
  addEventListener("beforeunload", (e) => { if (dirty()) e.preventDefault(); });
  document.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") { e.preventDefault(); if (!$("#ed-save").disabled) save(false); }
  });
}

async function create(start) {
  const name = $("#ns-name").value.trim();
  const err = $("#ns-name-err");
  err.hidden = true;
  const r = await post("/new", { name, compose: cur.compose, env: cur.env, start }, true);
  if (r.status === 422) {
    if (r.body.field === "name") { err.textContent = r.body.error; err.hidden = false; $("#ns-name").focus(); }
    issues = r.body.issues || [];
    showIssues();
    return;
  }
  if (!r.ok) return toast(r.body.error || "Échec", true);
  if (r.body.id) sessionStorage.setItem("dockerr-op", JSON.stringify({ id: r.body.id, stack: name, op: "start" }));
  location.href = r.body.url;
}

init();
