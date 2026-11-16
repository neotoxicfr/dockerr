// Network management on the resources page: create, change (remove and
// create again), connect and disconnect containers, remove.
const { toast, confirmAsk, post } = window.dockerr;
const $ = (s, r = document) => r.querySelector(s);

const dlg = $("#net-dlg");
const form = $("#net-form");
let editing = null; // network id being changed, null when creating

const syncFields = () => {
  $("[data-for-parent]", form).hidden = form.driver.value === "bridge";
  $("[data-for-ipv6]", form).hidden = !form.ipv6.checked;
};
form.driver.addEventListener("change", syncFields);
form.ipv6.addEventListener("change", syncFields);

function openForm(d) {
  editing = d ? d.netEdit : null;
  form.reset();
  if (d) {
    form.name.value = d.name;
    form.driver.value = d.driver === "macvlan" || d.driver === "ipvlan" ? d.driver : "bridge";
    form.parent.value = d.parent || "";
    form.subnet.value = d.subnet || "";
    form.gateway.value = d.gateway || "";
    form.ipv6.checked = d.ipv6 === "1";
    form.subnet6.value = d.subnet6 || "";
    form.internal.checked = d.internal === "1";
  }
  $("h2", dlg).textContent = d ? `Modifier ${d.name}` : "Nouveau réseau";
  $(".net-warn", dlg).hidden = !d;
  $('button[value="ok"]', dlg).textContent = d ? "Recréer" : "Créer";
  syncFields();
  dlg.showModal();
}

dlg.addEventListener("close", async () => {
  if (dlg.returnValue !== "ok") return;
  const spec = {
    name: form.name.value.trim(), driver: form.driver.value, parent: form.parent.value.trim(),
    subnet: form.subnet.value.trim(), gateway: form.gateway.value.trim(),
    ipv6: form.ipv6.checked, subnet6: form.subnet6.value.trim(), internal: form.internal.checked,
  };
  const url = editing ? `/networks/${encodeURIComponent(editing)}/recreate` : "/networks";
  const r = await post(url, spec, true);
  if (!r.ok) {
    toast(r.body.error || "Échec", true);
    return dlg.showModal(); // keep what was typed
  }
  toast(editing ? `${spec.name} recréé` : `${spec.name} créé`);
  setTimeout(() => location.reload(), 500);
});

const conn = $("#net-conn");
const connForm = $("#net-conn-form");
conn.addEventListener("close", async () => {
  if (conn.returnValue !== "ok") return;
  const r = await post(`/networks/${encodeURIComponent(conn.dataset.id)}/connect`, {
    container: connForm.container.value, ipv4: connForm.ipv4.value.trim(),
    aliases: connForm.aliases.value.split(",").map((s) => s.trim()).filter(Boolean),
  }, true);
  if (!r.ok) {
    toast(r.body.error || "Échec", true);
    return conn.showModal();
  }
  toast(`${connForm.container.value} connecté`);
  setTimeout(() => location.reload(), 500);
});

document.addEventListener("click", async (e) => {
  const t = e.target.closest("[data-net-new], [data-net-edit], [data-net-remove], [data-net-connect], [data-net-disconnect]");
  if (!t) return;
  const d = t.dataset;
  if ("netNew" in d) return openForm(null);
  if (d.netEdit) return openForm(d);
  if (d.netConnect) {
    connForm.reset();
    conn.dataset.id = d.netConnect;
    $(".net-name", conn).textContent = d.name;
    return conn.showModal();
  }
  if (d.netRemove) {
    if (!(await confirmAsk(`Supprimer le réseau ${d.name} ?`, "", "Supprimer"))) return;
    const r = await post(`/networks/${encodeURIComponent(d.netRemove)}/remove`, {}, true);
    if (!r.ok) return toast(r.body.error || "Échec", true);
    return location.reload();
  }
  if (d.netDisconnect) {
    if (!(await confirmAsk(`Déconnecter ${d.container} ?`, "Le conteneur perd ce réseau jusqu'à sa prochaine recréation.", "Déconnecter"))) return;
    const r = await post(`/networks/${encodeURIComponent(d.netDisconnect)}/disconnect`, { container: d.container }, true);
    if (!r.ok) return toast(r.body.error || "Échec", true);
    location.reload();
  }
});
