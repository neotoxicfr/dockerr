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

let busy = false; // a request is running: no second form meanwhile

function openForm(d) {
  editing = d ? d.netEdit : null;
  form.reset();
  if (d) {
    form.name.value = d.name;
    form.driver.value = d.driver; // only drivers the form offers are editable
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

// submitted runs a dialog's request on an explicit submit only, keeps the
// dialog open (and what was typed) when it fails, and blocks other forms
// while it runs.
function submitted(dialog, formEl, run) {
  formEl.addEventListener("submit", async (e) => {
    if (e.submitter?.value !== "ok") return; // Annuler closes as usual
    e.preventDefault();
    if (busy) return;
    busy = true;
    e.submitter.classList.add("busy");
    try {
      if (await run()) {
        dialog.close();
        setTimeout(() => location.reload(), 500);
      }
    } finally {
      busy = false;
      e.submitter.classList.remove("busy");
    }
  });
}

submitted(dlg, form, async () => {
  const spec = {
    name: form.name.value.trim(), driver: form.driver.value, parent: form.parent.value.trim(),
    subnet: form.subnet.value.trim(), gateway: form.gateway.value.trim(),
    ipv6: form.ipv6.checked, subnet6: form.subnet6.value.trim(), internal: form.internal.checked,
  };
  const r = await post(editing ? `/networks/${encodeURIComponent(editing)}/recreate` : "/networks", spec, true);
  if (!r.ok) {
    if (r.body.id) {
      // The old network was put back under a new ID: the page shows the
      // removed one, so reload it whenever the form closes.
      editing = r.body.id;
      if (dlg.open) dlg.addEventListener("close", () => location.reload(), { once: true });
      else location.reload(); // closed while the request ran
    }
    toast(r.body.error || "Échec", true);
    return false;
  }
  toast(editing ? `${spec.name} recréé` : `${spec.name} créé`);
  return true;
});

const conn = $("#net-conn");
const connForm = $("#net-conn-form");
submitted(conn, connForm, async () => {
  const r = await post(`/networks/${encodeURIComponent(conn.dataset.id)}/connect`, {
    container: connForm.container.value, ipv4: connForm.ipv4.value.trim(),
    aliases: connForm.aliases.value.split(",").map((x) => x.trim()).filter(Boolean),
  }, true);
  if (!r.ok) {
    toast(r.body.error || "Échec", true);
    return false;
  }
  toast(`${connForm.container.value} connecté`);
  return true;
});

document.addEventListener("click", async (e) => {
  const t = e.target.closest("[data-net-new], [data-net-edit], [data-net-remove], [data-net-connect], [data-net-disconnect]");
  if (!t) return;
  if (busy) return;
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
