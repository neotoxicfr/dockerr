// dockerr — page behaviour shared by every page. No framework: the server
// renders HTML, this file wires actions, live refresh and operation progress.
"use strict";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];

const OP_LABELS = {
  start: "Démarrage", stop: "Arrêt", restart: "Redémarrage", update: "Mise à jour",
  pull: "Téléchargement", recreate: "Recréation", down: "Suppression", repair: "Réparation réseau",
};

// ---- toasts ----
function toast(msg, bad = false) {
  const t = document.createElement("div");
  t.className = "toast" + (bad ? " bad" : "");
  t.textContent = msg;
  $("#toasts").append(t);
  setTimeout(() => t.remove(), bad ? 6000 : 3000);
}

// ---- confirm dialog ----
function confirmAsk(title, text, yes) {
  const d = $("#confirm");
  $("h2", d).textContent = title;
  $("p", d).textContent = text || "";
  $("p", d).hidden = !text;
  $('button[value="yes"]', d).textContent = yes;
  d.returnValue = "";
  d.showModal();
  return new Promise((res) => d.addEventListener("close", () => res(d.returnValue === "yes"), { once: true }));
}

// ---- fetch helpers ----
async function post(url, data, json = false) {
  const opts = { method: "POST", headers: {} };
  if (json) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(data);
  } else if (data) {
    opts.body = new URLSearchParams(data);
  }
  let r;
  try {
    r = await fetch(url, opts);
  } catch (e) {
    // An expired session redirects to the login page on another origin.
    location.reload();
    return { ok: false, status: 0, body: { error: "Session expirée" } };
  }
  let body = {};
  try { body = await r.json(); } catch (e) {}
  return { ok: r.ok, status: r.status, body };
}

// ---- menus ----
function closeMenus() {
  $$(".menu").forEach((m) => (m.hidden = true));
  $$(".menu-scrim").forEach((s) => s.remove());
  $$("[data-menu][aria-expanded=true]").forEach((b) => b.setAttribute("aria-expanded", "false"));
}
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-menu]");
  if (btn) {
    e.preventDefault();
    const menu = btn.nextElementSibling;
    const open = menu.hidden;
    closeMenus();
    if (open) {
      menu.hidden = false;
      btn.setAttribute("aria-expanded", "true");
      const r = menu.getBoundingClientRect();
      menu.classList.toggle("up", r.bottom > window.innerHeight - 8 && matchMedia("(min-width: 761px)").matches);
      if (matchMedia("(max-width: 760px)").matches) {
        const s = document.createElement("div");
        s.className = "menu-scrim";
        s.addEventListener("click", closeMenus);
        document.body.append(s);
      }
      menu.querySelector("button, a")?.focus({ preventScroll: true });
    }
    return;
  }
  if (!e.target.closest(".menu")) closeMenus();
});
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeMenus(); });

// ---- operations ----
const CONFIRM_OPS = {
  stop: (s, svc) => [`Arrêter ${svc || s} ?`, svc ? "" : "Tous les services de la pile seront arrêtés.", "Arrêter"],
  down: (s) => [`Supprimer les conteneurs de ${s} ?`, "Les volumes et le fichier compose sont gardés.", "Supprimer"],
  recreate: (s, svc) => [`Recréer ${svc || s} ?`, "Les conteneurs sont supprimés puis recréés.", "Recréer"],
};

document.addEventListener("click", async (e) => {
  const b = e.target.closest("[data-op]");
  if (!b || b.getAttribute("aria-disabled") === "true") return;
  e.preventDefault();
  closeMenus();
  const { op, stack, service } = b.dataset;
  if (CONFIRM_OPS[op]) {
    const [t, x, y] = CONFIRM_OPS[op](stack, service);
    if (!(await confirmAsk(t, x, y))) return;
  }
  const data = { kind: op };
  if (service) data.service = service;
  const trigger = b.closest(".menu") ? b.closest(".menu-wrap").querySelector("[data-menu]") : b;
  trigger.classList.add("busy");
  trigger.setAttribute("aria-disabled", "true");
  const r = await post(`/s/${encodeURIComponent(stack)}/op`, data);
  trigger.classList.remove("busy");
  trigger.removeAttribute("aria-disabled");
  if (!r.ok) return toast(r.body.error || "Échec", true);
  follow(r.body.id, stack, op, service);
});

let current = null;
function follow(id, stack, op, service) {
  if (current) current.close();
  const panel = $("#activity");
  panel.hidden = false;
  panel.innerHTML = "";
  const head = document.createElement("header");
  head.innerHTML = `<svg class="i st-working"><use href="#i-loader"/></svg><div class="t"><strong></strong><span></span></div>
    <button class="btn icon ghost" aria-label="Fermer"><svg class="i"><use href="#i-x"/></svg></button>`;
  $("strong", head).textContent = `${OP_LABELS[op] || op} · ${service || stack}`;
  $("span", head).textContent = "En cours";
  const tasks = document.createElement("div");
  tasks.className = "tasks";
  panel.append(head, tasks);
  let pinned = false;
  panel.onpointerdown = () => (pinned = true);
  $("button", head).onclick = () => { panel.hidden = true; es.close(); };

  const es = new EventSource(`/ops/${id}`);
  current = es;
  es.addEventListener("op", (ev) => {
    const s = JSON.parse(ev.data);
    renderTasks(tasks, s.Tasks || [], s.Done);
    if (!s.Done) return;
    es.close();
    const icon = $("svg", head);
    icon.classList.remove("st-working");
    if (s.Err) {
      icon.classList.add("st-error");
      icon.innerHTML = '<use href="#i-alert"/>';
      $("span", head).textContent = "Échec";
      const err = document.createElement("div");
      err.className = "err";
      err.textContent = shortIDs(s.Err);
      panel.append(err);
    } else {
      icon.classList.add("st-done");
      icon.innerHTML = '<use href="#i-check"/>';
      $("span", head).textContent = "Terminé";
      setTimeout(() => { if (!pinned) panel.hidden = true; }, 2500);
    }
    refreshLive();
  });
  es.onerror = () => { es.close(); };
  refreshLive();
}

const ST_ICON = { done: "check", error: "alert", warning: "alert", working: "loader", stopped: "x" };
const shortIDs = (s) => s.replace(/\b([0-9a-f]{12})[0-9a-f]{52}\b/g, "$1");
function renderTasks(el, list, done) {
  el.innerHTML = "";
  for (const t of list) {
    if (done && t.Status === "working") t.Status = "stopped";
    const row = document.createElement("div");
    row.className = "task";
    row.innerHTML = `<svg class="i s st-${t.Status}"><use href="#i-${ST_ICON[t.Status] || "loader"}"/></svg><span class="id"></span><span class="tx"></span>`;
    $(".id", row).textContent = t.ID.replace(/^(Container|Image|Network|Volume) /, "");
    $(".id", row).title = t.ID;
    $(".tx", row).textContent = t.Text + (t.Status === "working" && t.Percent ? ` ${t.Percent} %` : "");
    if (t.Status === "working" && t.Percent) {
      const bar = document.createElement("div");
      bar.className = "bar-p";
      const fill = document.createElement("i");
      fill.style.width = `${t.Percent}%`;
      bar.append(fill);
      row.append(bar);
    }
    if (t.Details && t.Status === "error") {
      const d = document.createElement("div");
      d.className = "det";
      d.textContent = shortIDs(t.Details);
      row.append(d);
    }
    el.append(row);
  }
  el.scrollTop = el.scrollHeight;
}

// ---- live refresh ----
// Elements with data-live are re-fetched when Docker reports a change, and
// every 10 s for resource figures. Open menus pause the refresh.
let liveTimer = null;
function refreshLive() {
  clearTimeout(liveTimer);
  liveTimer = setTimeout(async () => {
    for (const el of $$("[data-live]")) {
      if ($(".menu:not([hidden])", el) || document.hidden) continue;
      try {
        const r = await fetch(el.dataset.live, { headers: { Accept: "text/html" } });
        if (!r.ok) continue;
        const html = await r.text();
        if ($(".menu:not([hidden])", el)) continue;
        el.innerHTML = html;
        applyFilter();
      } catch (e) {}
    }
  }, 400);
}
if ($("[data-live]")) {
  const es = new EventSource("/events");
  es.addEventListener("change", refreshLive);
  setInterval(refreshLive, 10000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshLive(); });
}

// ---- stack list filters and search ----
let filter = "all";
function applyFilter() {
  const q = ($("#q")?.value || "").trim().toLowerCase();
  for (const row of $$("#stack-list .row")) {
    const st = row.dataset.state;
    const okF = filter === "all" ||
      (filter === "attention" && (st === "problem" || st === "partial")) ||
      (filter === "updates" && row.dataset.upd) || st === filter;
    row.hidden = !okF || (q && !row.dataset.name.includes(q));
  }
  for (const tr of $$("table[data-filterable] tr[data-text], table[data-filterable] tbody[data-text]")) {
    tr.hidden = q && !tr.dataset.text.toLowerCase().includes(q);
  }
}
$("#filters")?.addEventListener("click", (e) => {
  const b = e.target.closest("[data-filter]");
  if (!b) return;
  filter = b.dataset.filter;
  $$("#filters [data-filter]").forEach((x) => x.setAttribute("aria-selected", x === b));
  applyFilter();
});
$("#q")?.addEventListener("input", applyFilter);
document.addEventListener("keydown", (e) => {
  if (e.key === "/" && !e.target.closest("input, textarea, .cm-editor") && $("#q")) {
    e.preventDefault();
    $("#q").focus();
  }
});

// ---- misc actions ----
document.addEventListener("click", async (e) => {
  const chk = e.target.closest("[data-check-updates]");
  if (chk) {
    chk.classList.add("busy");
    const r = await post("/updates/check");
    toast(r.ok ? "Vérification lancée" : "Échec", !r.ok);
    setTimeout(() => chk.classList.remove("busy"), 3000);
    return;
  }
  const pr = e.target.closest("[data-prune]");
  if (pr) {
    if (!(await confirmAsk(pr.dataset.confirm, "", "Supprimer"))) return;
    const r = await post("/resources/prune", { kind: pr.dataset.prune });
    if (!r.ok) return toast(r.body.error || "Échec", true);
    toast(`${r.body.removed} supprimé(s)${r.body.freed && r.body.freed !== "0 o" ? ", " + r.body.freed + " libérés" : ""}`);
    setTimeout(() => location.reload(), 600);
    return;
  }
  const im = e.target.closest("[data-image-remove]");
  if (im) {
    if (!(await confirmAsk("Supprimer cette image ?", "", "Supprimer"))) return;
    const r = await post(`/images/${encodeURIComponent(im.dataset.imageRemove)}/remove`);
    if (!r.ok) return toast(r.body.error || "Échec", true);
    im.closest("tr").remove();
    return;
  }
  const ca = e.target.closest("[data-ctr-action]");
  if (ca) {
    if (ca.dataset.confirm && !(await confirmAsk(ca.dataset.confirm, "", "Confirmer"))) return;
    ca.classList.add("busy");
    const r = await post(`/c/${ca.dataset.id}/action`, { action: ca.dataset.ctrAction });
    ca.classList.remove("busy");
    if (!r.ok) return toast(r.body.error || "Échec", true);
    if (ca.dataset.ctrAction === "remove") location.href = "/resources";
    else location.reload();
    return;
  }
  const rev = e.target.closest("[data-reveal]");
  if (rev) {
    const r = await fetch(`/c/${rev.dataset.id}/env?key=${encodeURIComponent(rev.dataset.reveal)}`);
    const b = await r.json().catch(() => ({}));
    if (!r.ok) return toast(b.error || "Lecture impossible", true);
    const span = document.createElement("span");
    span.textContent = b.value;
    rev.parentElement.replaceWith(span);
    return;
  }
  const th = e.target.closest("[data-theme-set]");
  if (th) setTheme(th.dataset.themeSet);
});

$$("[data-autosubmit]").forEach((el) => el.addEventListener("change", () => el.form.requestSubmit()));

function setTheme(t) {
  try { t === "auto" ? localStorage.removeItem("theme") : localStorage.setItem("theme", t); } catch (e) {}
  if (t === "auto") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
  $$("[data-theme-set]").forEach((b) => b.setAttribute("aria-selected", b.dataset.themeSet === t));
}
if ($("#theme")) setTheme(document.documentElement.dataset.theme || "auto");

// An operation started elsewhere (e.g. "save and apply") hands over its id.
const pending = sessionStorage.getItem("dockerr-op");
if (pending) {
  sessionStorage.removeItem("dockerr-op");
  try { const p = JSON.parse(pending); follow(p.id, p.stack, p.op); } catch (e) {}
}

// Expandable table rows: the summary row toggles its detail row.
document.addEventListener("click", (e) => {
  const sum = e.target.closest("tr[data-toggle]");
  if (!sum || e.target.closest("a")) return;
  const det = document.getElementById(sum.dataset.toggle);
  const open = det.hidden;
  det.hidden = !open;
  sum.classList.toggle("open", open);
  $("[aria-expanded]", sum)?.setAttribute("aria-expanded", String(open));
});

// Network map: hovering a bus or a container highlights its links.
const map = $("#netmap");
if (map) {
  const clear = () => {
    map.classList.remove("hl");
    $$(".hl-col, .hl-row", map).forEach((x) => x.classList.remove("hl-col", "hl-row"));
  };
  map.addEventListener("pointerover", (e) => {
    const cell = e.target.closest("[data-col]");
    const row = e.target.closest("tbody tr:not(.grp)");
    clear();
    if (cell && cell.tagName === "TH") {
      map.classList.add("hl");
      $$(`[data-col="${cell.dataset.col}"]`, map).forEach((x) => x.classList.add("hl-col"));
      $$("tbody tr", map).forEach((tr) => tr.classList.add("hl-row"));
    } else if (row) {
      map.classList.add("hl");
      row.classList.add("hl-row");
      $$("td.bus.on", row).forEach((td) => $$(`[data-col="${td.dataset.col}"]`, map).forEach((x) => x.classList.add("hl-col")));
    }
  });
  map.addEventListener("pointerleave", clear);
}

if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});

window.dockerr = { toast, confirmAsk, post, follow };
