// History tab: show a version's diff, restore it.
const $ = (s, el = document) => el.querySelector(s);
const { toast, post, confirmAsk } = window.dockerr;

const root = $(".history");
const stack = root.dataset.stack;
const view = $("#version-view");

async function show(btn) {
  for (const b of root.querySelectorAll("[data-version]")) b.setAttribute("aria-selected", b === btn);
  const r = await fetch(`/s/${encodeURIComponent(stack)}/history/${btn.dataset.version}`);
  view.innerHTML = r.ok ? await r.text() : "";
  if (matchMedia("(max-width: 860px)").matches) view.scrollIntoView({ behavior: "smooth", block: "start" });
}

root.addEventListener("click", async (e) => {
  const v = e.target.closest("[data-version]");
  if (v) return show(v);
  const rs = e.target.closest("[data-restore]");
  if (!rs) return;
  if (!(await confirmAsk(rs.dataset.confirm, "Le compose n'est pas relancé : utilise Démarrer ou Enregistrer et appliquer ensuite.", "Restaurer"))) return;
  const r = await post(`/s/${encodeURIComponent(stack)}/history/${rs.dataset.restore}/restore`);
  if (r.status === 422) return toast((r.body.issues?.[0]?.message) || "Version invalide", true);
  if (!r.ok) return toast(r.body.error || "Échec", true);
  toast("Version restaurée");
  setTimeout(() => location.reload(), 500);
});

const first = root.querySelector("[data-version]");
if (first && matchMedia("(min-width: 861px)").matches) show(first);
