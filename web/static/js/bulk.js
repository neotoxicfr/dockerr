// Multi-selection: stacks on the home page, containers on the resources
// page. The selection survives the live refresh of the rows.
const { toast, confirmAsk, post } = window.dockerr;
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

const bar = $("#bulk");
const kind = bar.dataset.bulk; // "stacks" or "containers"
const picked = new Map(); // key → label
let running = false;

const LABELS = { start: "Démarrer", stop: "Arrêter", restart: "Redémarrer", update: "Mettre à jour",
  pause: "Mettre en pause", unpause: "Reprendre", remove: "Supprimer" };
const CONFIRM = new Set(["stop", "remove", "update"]);

function sync() {
  for (const cb of $$("input[data-sel]")) cb.checked = picked.has(cb.dataset.sel);
  bar.hidden = picked.size === 0;
  const n = picked.size;
  $(".bulk-n", bar).textContent = `${n} ${kind === "stacks" ? (n > 1 ? "piles" : "pile") : (n > 1 ? "conteneurs" : "conteneur")}`;
}

document.addEventListener("change", (e) => {
  const cb = e.target.closest("input[data-sel]");
  if (!cb) return;
  if (cb.checked) picked.set(cb.dataset.sel, cb.dataset.label || cb.dataset.sel);
  else picked.delete(cb.dataset.sel);
  sync();
});

// Rows are redrawn by the live refresh: put the ticks back.
new MutationObserver(() => {
  for (const cb of $$("input[data-sel]")) cb.checked = picked.has(cb.dataset.sel);
}).observe(document.body, { childList: true, subtree: true });

bar.addEventListener("click", async (e) => {
  if (e.target.closest("[data-bulk-clear]")) { picked.clear(); sync(); return; }
  const b = e.target.closest("[data-bulk-op]");
  if (!b || running) return;
  const op = b.dataset.bulkOp;
  // The selection as it is now: it may change while the requests run.
  const snap = new Map(picked);
  const keys = [...snap.keys()];
  if (CONFIRM.has(op)) {
    const names = [...snap.values()].join(", ");
    if (!(await confirmAsk(`${LABELS[op]} ${keys.length} ${kind === "stacks" ? "pile(s)" : "conteneur(s)"} ?`, names, LABELS[op]))) return;
  }
  running = true;
  b.classList.add("busy");
  // One request per item, all at once: each stack has its own operation.
  const results = await Promise.all(keys.map((k) => kind === "stacks"
    ? post(`/s/${encodeURIComponent(k)}/op`, { kind: op })
    : post(`/c/${encodeURIComponent(k)}/action`, { action: op })));
  b.classList.remove("busy");
  running = false;
  const failed = results.map((r, i) => (r.ok ? null : `${snap.get(keys[i])} : ${r.body.error || "échec"}`)).filter(Boolean);
  const done = results.length - failed.length;
  if (done) toast(`${done} ${kind === "stacks" ? "opération(s) lancée(s)" : "action(s) faite(s)"}`);
  if (failed.length) toast(failed.join(" · "), true);
  for (const k of keys) picked.delete(k);
  sync();
  if (kind === "containers") setTimeout(() => location.reload(), 800);
});

sync();
