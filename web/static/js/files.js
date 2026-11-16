// File browser: save the edited file, upload files into the current folder.
"use strict";

const root = document.getElementById("files");
const base = root.dataset.base;
const here = root.dataset.path;

const text = document.getElementById("fb-text");
const saveBtn = document.getElementById("fb-save");
if (text && saveBtn) {
  let saved = text.value;
  let saving = false; // one save at a time, whatever the button shows
  const dirty = () => text.value !== saved;
  text.addEventListener("input", () => (saveBtn.disabled = saving || !dirty()));
  window.addEventListener("beforeunload", (e) => { if (dirty()) e.preventDefault(); });
  const save = async () => {
    if (saving || !dirty()) return;
    saving = true;
    saveBtn.disabled = true;
    const value = text.value;
    // Browsers turn line ends into \n; a CRLF file keeps its own.
    const content = text.dataset.crlf ? value.replace(/\r?\n/g, "\r\n") : value;
    const r = await post(`${base}/save`, { path: here, content }, true);
    saving = false;
    if (!r.ok) {
      saveBtn.disabled = false;
      toast(r.body.error || "Échec de l'enregistrement", true);
      return;
    }
    saved = value;
    saveBtn.disabled = !dirty();
    toast("Enregistré");
  };
  saveBtn.addEventListener("click", save);
  text.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      save();
    }
  });
}

const pick = document.getElementById("fb-pick");
const input = document.getElementById("fb-upload");
if (pick && input) {
  pick.addEventListener("click", () => input.click());
  const send = async (files, overwrite) => {
    const fd = new FormData();
    for (const f of files) fd.append("files", f, f.name);
    const q = new URLSearchParams({ path: here });
    if (overwrite) q.set("overwrite", "1");
    let r;
    try {
      r = await fetch(`${base}/upload?${q}`, { method: "POST", body: fd });
    } catch (e) {
      location.reload();
      return;
    }
    let body = {};
    try { body = await r.json(); } catch (e) {}
    if (r.status === 409 && body.exists && !overwrite) {
      const n = body.exists.length;
      const ok = await confirmAsk(n > 1 ? `Remplacer ${n} fichiers ?` : `Remplacer ${body.exists[0]} ?`,
        n > 1 ? body.exists.join(", ") : "Un fichier de ce nom existe déjà dans ce dossier.", "Remplacer");
      if (ok) await send(files, true);
      return;
    }
    if (!r.ok) {
      toast(body.error || "Échec du téléversement", true);
      return;
    }
    location.reload();
  };
  input.addEventListener("change", async () => {
    const files = [...input.files];
    input.value = "";
    if (!files.length) return;
    const total = files.reduce((s, f) => s + f.size, 0);
    if (total > 100 * 1024 * 1024) {
      toast("Plus de 100 Mio : téléversement refusé", true);
      return;
    }
    pick.disabled = true;
    try {
      await send(files, false);
    } finally {
      pick.disabled = false;
    }
  });
}
