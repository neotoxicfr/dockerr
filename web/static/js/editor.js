// Compose editor, stack page and new stack page.
//
// Two modes on the same text: graphical (forms built from the server's model
// of the compose file) and text (CodeMirror). Graphical edits are applied by
// the server, which keeps comments, and its text becomes the one both modes
// show. Every change is checked after a short quiet time, and the result
// stays visible above the editor.
import { mount } from "/static/vendor/cm.js";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const { toast, post, confirmAsk } = window.dockerr;

const root = $("#compose-editor") || $("#new-stack");
const isNew = root.id === "new-stack";
const stack = root.dataset.stack || "";
const editable = isNew || root.dataset.editable === "1";
const composeOk = root.dataset.composeOk === "1";
const nonce = document.querySelector('meta[name="csp-nonce"]')?.content;

const NEW_TEMPLATE = "services:\n  app:\n    image: \n    restart: unless-stopped\n";
const SENSITIVE = /pass|secret|token|key|pwd|auth|cred|private|salt/i;
const QUIET_MS = 600;

const st = {
  compose: "", env: "", saved: { compose: "", env: "" }, hash: "",
  model: null, issues: [], warnings: [], checking: false,
  mode: root.dataset.mode === "text" ? "text" : "visual",
  part: "compose",
};
let ed = null; // CodeMirror instance (text mode)

const dirty = () => st.compose !== st.saved.compose || st.env !== st.saved.env;

// ---------------------------------------------------------------- DOM helpers
function h(tag, attrs = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  for (const k of kids.flat()) {
    if (k === null || k === undefined || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}
const icon = (name, small = true) => {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", small ? "i s" : "i");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", "#i-" + name);
  svg.append(use);
  return svg;
};

// ---------------------------------------------------------------- check
let checkTimer = null;
let chain = Promise.resolve();

function scheduleCheck() {
  clearTimeout(checkTimer);
  st.checking = true;
  renderCheck();
  checkTimer = setTimeout(() => check(), QUIET_MS);
}

// check queues a check of the current text, with graphical edits if any.
// Checks run one after the other, each on the latest text: an edit's answer
// is never dropped for a newer one.
function check(edits) {
  clearTimeout(checkTimer);
  const job = () => runCheck(edits);
  chain = chain.then(job, job);
  return chain;
}

async function runCheck(edits) {
  st.checking = true;
  renderCheck();
  const body = { stack, name: isNew ? $("#ns-name").value.trim() : "", compose: st.compose, env: st.env, edits: edits || [] };
  const r = await post("/compose/check", body, true);
  st.checking = false;
  if (!r.ok) { renderCheck(); return toast(r.body.error || "Vérification impossible", true); }
  if (r.body.edit_error) toast(r.body.edit_error, true);
  const edited = edits && edits.length && !r.body.edit_error;
  if (edited) {
    st.compose = r.body.compose;
    if (ed && st.part === "compose") ed.value = st.compose;
  }
  // A text change made while this check ran: a newer check is scheduled.
  if (!edited && body.compose !== st.compose) return;
  st.model = r.body.model;
  st.issues = r.body.issues || [];
  st.warnings = r.body.warnings || [];
  renderCheck();
  updateButtons();
  if (ed) ed.mark(st.part === "compose" ? st.issues : []);
  // Typing in a form field must not lose focus: redraw only after edits or
  // when nothing is drawn yet.
  if (st.mode === "visual" && st.part === "compose" && (edited || !$("#visual").childElementCount)) renderVisualSafe();
}

function renderCheck() {
  const box = $("#ed-check");
  const msg = $(".msg", box);
  const more = $("#ed-check-more");
  const list = $("#ed-check-list");
  const all = [...st.issues.map((i) => ({ ...i, bad: true })), ...st.warnings];
  let cls, ico, parts;
  if (st.checking) {
    cls = "busy"; ico = "loader"; parts = ["Vérification…"];
  } else if (st.issues.length) {
    const i = st.issues[0];
    cls = "bad"; ico = "alert";
    parts = [h("b", {}, "Ne se construira pas"), " · ", i.line ? `ligne ${i.line} : ` : "", i.message];
  } else if (st.warnings.length) {
    const w = st.warnings[0];
    cls = "warn"; ico = "alert";
    parts = [h("b", {}, st.warnings.length > 1 ? `${st.warnings.length} avertissements` : "1 avertissement"), " · ", w.message];
  } else if (st.model) {
    cls = "ok"; ico = "check";
    parts = [h("b", {}, "Configuration valide"), dirty() ? " · prête à enregistrer" : ""];
  } else {
    cls = "busy"; ico = "loader"; parts = ["Vérification…"];
  }
  box.className = "check " + cls;
  box.firstElementChild.replaceWith(icon(ico, false));
  msg.replaceChildren(...parts);
  msg.title = msg.textContent;
  more.hidden = !(all.length > 1 || (all.length === 1 && all[0].line));
  more.textContent = list.hidden ? "Détails" : "Masquer";
  list.replaceChildren(...all.map((x) => h("button", { type: "button", onclick: () => jump(x.line) },
    icon(x.bad ? "x" : "alert"), h("span", { class: "ln" }, x.line ? `ligne ${x.line}` : "—"), h("span", {}, x.message))));
  if (!all.length) list.hidden = true;
}

$("#ed-check-more").addEventListener("click", () => {
  const list = $("#ed-check-list");
  list.hidden = !list.hidden;
  renderCheck();
});

function jump(line) {
  if (!line) return;
  st.part = "compose";
  $$("[data-part]", root).forEach((b) => b.setAttribute("aria-selected", String(b.dataset.part === "compose")));
  setMode("text", true);
  requestAnimationFrame(() => {
    const doc = ed.view.state.doc;
    const l = doc.line(Math.min(line, doc.lines));
    ed.view.dispatch({ selection: { anchor: l.from }, scrollIntoView: true });
    ed.view.focus();
  });
}

// ---------------------------------------------------------------- modes and parts
function setMode(mode, persist = false) {
  st.mode = mode;
  $$("button[data-mode]", root).forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.mode === mode)));
  if (persist) post("/prefs", { key: "editor_mode", value: mode });
  render();
}

function setPart(part) {
  st.part = part;
  $$("[data-part]", root).forEach((b) => b.setAttribute("aria-selected", String(b.dataset.part === part)));
  render();
}

function render() {
  const textVisible = st.mode === "text" && st.part !== "secrets";
  $("#editor").hidden = !textVisible;
  $("#visual").hidden = textVisible;
  if (textVisible) {
    if (ed) ed.destroy();
    ed = mount($("#editor"), st[st.part], {
      lang: st.part === "compose" ? "yaml" : "env",
      readOnly: !editable,
      nonce,
      onChange: (v) => { st[st.part] = v; updateButtons(); scheduleCheck(); },
    });
    ed.mark(st.part === "compose" ? st.issues : []);
  } else {
    if (ed) { ed.destroy(); ed = null; }
    renderVisual();
  }
}

root.addEventListener("click", (e) => {
  const m = e.target.closest("button[data-mode]");
  if (m) return setMode(m.dataset.mode, true);
  const p = e.target.closest("[data-part]");
  if (p) return setPart(p.dataset.part);
});

// ---------------------------------------------------------------- graphical mode
// A redraw while the user types in another field would drop that input:
// wait until the focus leaves the form fields.
let redrawPending = false;
function renderVisualSafe() {
  const a = document.activeElement;
  if (a && $("#visual").contains(a) && (a.tagName === "INPUT" || a.tagName === "SELECT")) { redrawPending = true; return; }
  renderVisual();
}
$("#visual").addEventListener("focusout", () => setTimeout(() => {
  const a = document.activeElement;
  if (redrawPending && !($("#visual").contains(a) && (a.tagName === "INPUT" || a.tagName === "SELECT"))) { redrawPending = false; renderVisual(); }
}));

function renderVisual() {
  redrawPending = false;
  const box = $("#visual");
  const y = window.scrollY;
  if (st.part === "env") box.replaceChildren(envPanel());
  else if (st.part === "secrets") box.replaceChildren(...secretsPanel());
  else box.replaceChildren(...composePanel());
  window.scrollTo(0, y);
}

const edit = (op, path, value) => check([{ op, path, value }]);

function composePanel() {
  const m = st.model;
  if (!m) {
    if (!st.compose.trim() && st.checking) return [];
    return [h("div", { class: "banner bad" }, icon("alert", false),
      h("span", {}, "Le fichier ne se lit pas : corrige-le en mode texte. "),
      h("button", { type: "button", class: "btn sm", onclick: () => setMode("text", true) }, "Ouvrir en texte"))];
  }
  const out = m.services.map((s) => serviceCard(s, m));
  if (editable) out.push(newServiceCard());
  if (m.other.length) {
    out.push(h("p", { class: "vother" }, `Autres sections, à modifier en mode texte : ${m.other.join(", ")}. `,
      h("button", { type: "button", onclick: () => setMode("text", true) }, "Ouvrir en texte")));
  }
  return out;
}

function serviceCard(s, m) {
  const locked = (f) => !editable || s.locked.includes(f);
  const path = (f) => ["services", s.name, f];
  const others = m.services.map((x) => x.name).filter((n) => n !== s.name);
  const grid = h("div", { class: "vgrid" },
    field("Image", "cube", s.build && !s.image ? h("div", { class: "ro" }, "Construite depuis un Dockerfile") :
      text(s.image, locked("image"), (v) => edit("set", path("image"), v), "nginx:1.27")),
    field("Redémarrage", "restart", locked("restart") ? ro(s.restart || "no") :
      select(s.restart, [["", "jamais (no)"], ["unless-stopped", "sauf arrêt manuel"], ["always", "toujours"], ["on-failure", "en cas d'erreur"]],
        (v) => edit("set", path("restart"), v))),
    field("Ports", "network", listEditor(s.ports, locked("ports"), (vs) => edit("list", path("ports"), vs), "8080:80")),
    field("Volumes", "box", listEditor(s.volumes, locked("volumes"), (vs) => edit("list", path("volumes"), vs), "./data:/data")),
    field("Variables d'environnement", "env", kvEditor(s.environment, locked("environment"), (kv) => edit("map", path("environment"), kv)), true),
    field("Réseaux", "network", listEditor(s.networks.map((v) => ({ value: v })), locked("networks"), (vs) => edit("list", path("networks"), vs), "proxy")),
    field("Mode réseau", "link", text(s.network_mode, locked("network_mode"), (v) => edit("set", path("network_mode"), v), "service:vpn")),
    others.length ? field("Démarre après", "play", checks(others, s.depends_on, locked("depends_on"), (vs) => edit("list", path("depends_on"), vs))) : null,
    m.secrets.length ? field("Secrets", "key", checks(m.secrets.map((x) => x.name), s.secrets, locked("secrets"), (vs) => edit("list", path("secrets"), vs))) : null,
    field("Commande", "terminal", text(s.command, locked("command"), (v) => edit("set", path("command"), v), "celle de l'image")),
    field("Labels", "sliders", kvEditor(s.labels, locked("labels"), (kv) => edit("map", path("labels"), kv)), true),
  );
  const head = h("div", { class: "vsvc-head" },
    h("h3", {}, icon("cube"), s.name), h("span", { class: "grow" }),
    editable ? h("button", { type: "button", class: "btn ghost sm icon", title: "Supprimer le service", "aria-label": `Supprimer ${s.name}`,
      onclick: async () => {
        if (await confirmAsk(`Supprimer le service ${s.name} ?`, "Il disparaît du fichier ; ses conteneurs restent jusqu'au prochain « Appliquer ».", "Supprimer")) edit("remove", ["services", s.name]);
      } }, icon("trash")) : null);
  const card = h("section", { class: "card vsvc" }, head, grid);
  if (s.other.length) {
    card.append(h("p", { class: "vother" }, `En mode texte : ${s.other.join(", ")}. `,
      h("button", { type: "button", onclick: () => setMode("text", true) }, "Ouvrir en texte")));
  }
  return card;
}

function newServiceCard() {
  const name = h("input", { type: "text", placeholder: "nom du service", "aria-label": "Nom du nouveau service", autocapitalize: "off", spellcheck: "false" });
  const image = h("input", { type: "text", placeholder: "image, ex. redis:7", "aria-label": "Image du nouveau service", autocapitalize: "off", spellcheck: "false" });
  const add = () => {
    const n = name.value.trim();
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(n)) return toast("Nom de service : lettres, chiffres, . _ -", true);
    if (st.model.services.some((s) => s.name === n)) return toast("Ce service existe déjà", true);
    edit("set", ["services", n, "image"], image.value.trim() || "busybox");
  };
  return h("section", { class: "card vnew" }, name, image,
    h("button", { type: "button", class: "btn", onclick: add }, icon("plus"), "Ajouter un service"));
}

function field(label, ico, control, wide = false) {
  return h("div", { class: "vf" + (wide ? " wide" : "") }, h("span", { class: "lbl" }, icon(ico), label), control);
}
const ro = (v) => h("div", { class: "ro", title: "À modifier en mode texte" }, icon("lock"), " ", v || "—");

function text(value, locked, onChange, placeholder) {
  if (locked) return ro(value);
  const el = h("input", { type: "text", value: value || "", placeholder, autocapitalize: "off", spellcheck: "false", class: "mono" });
  el.addEventListener("change", () => onChange(el.value.trim()));
  return el;
}

function select(value, options, onChange) {
  const cur = value || "";
  const el = h("select", {}, ...options.map(([v, l]) => h("option", { value: v, selected: v === cur }, l)));
  if (!options.some(([v]) => v === cur)) el.prepend(h("option", { value: cur, selected: true }, cur));
  el.addEventListener("change", () => onChange(el.value));
  return el;
}

function checks(names, selected, locked, onChange) {
  if (locked) return ro((selected || []).join(", "));
  const box = h("div", { class: "vchecks" });
  for (const n of names) {
    const cb = h("input", { type: "checkbox", checked: (selected || []).includes(n), "data-name": n });
    cb.addEventListener("change", () => onChange($$("input:checked", box).map((x) => x.dataset.name)));
    box.append(h("label", {}, cb, n));
  }
  return box;
}

// listEditor edits a list of strings; long-syntax items lock the list.
function listEditor(items, locked, onChange, placeholder) {
  items = items || [];
  const hasLong = items.some((i) => i.long);
  const box = h("div", { class: "vlist" });
  const commit = () => onChange($$("input", box).map((x) => x.value.trim()).filter(Boolean));
  for (const it of items) {
    if (it.long || locked) {
      box.append(h("div", { class: "it long" }, h("span", { title: "Syntaxe longue : à modifier en mode texte" }, it.value)));
      continue;
    }
    box.append(listRow(it.value, commit, placeholder));
  }
  if (!locked && !hasLong) {
    box.append(h("button", { type: "button", class: "btn ghost sm vadd", onclick: (e) => {
      const row = listRow("", commit, placeholder);
      e.currentTarget.before(row);
      $("input", row).focus();
    } }, icon("plus"), "Ajouter"));
  }
  if (!items.length && locked) box.append(ro(""));
  return box;
}

function listRow(value, commit, placeholder) {
  const input = h("input", { type: "text", value, placeholder, class: "mono", autocapitalize: "off", spellcheck: "false" });
  input.addEventListener("change", commit);
  const row = h("div", { class: "it" }, input,
    h("button", { type: "button", class: "btn ghost sm icon", "aria-label": "Retirer", onclick: () => { row.remove(); commit(); } }, icon("x")));
  return row;
}

// kvEditor edits KEY=VALUE pairs; sensitive values are masked until shown.
function kvEditor(kvs, locked, onChange) {
  kvs = kvs || [];
  const box = h("div", { class: "vlist vkv" });
  const commit = () => onChange($$(".it", box).map((r) => ({ key: $(".k", r).value.trim(), value: $(".v", r).value })).filter((x) => x.key));
  for (const kv of kvs) box.append(kvRow(kv.key, kv.value, commit, locked));
  if (!locked) {
    box.append(h("button", { type: "button", class: "btn ghost sm vadd", onclick: (e) => {
      const row = kvRow("", "", commit, false);
      e.currentTarget.before(row);
      $(".k", row).focus();
    } }, icon("plus"), "Ajouter"));
  }
  if (!kvs.length && locked) box.append(ro(""));
  return box;
}

function kvRow(key, value, commit, locked) {
  const secret = SENSITIVE.test(key);
  const k = h("input", { type: "text", value: key, class: "k mono", placeholder: "CLÉ", "aria-label": "Clé", readonly: locked, autocapitalize: "off", spellcheck: "false" });
  const v = h("input", { type: secret ? "password" : "text", value, class: "v mono", placeholder: "valeur", "aria-label": `Valeur de ${key || "la variable"}`, readonly: locked, autocomplete: "off", spellcheck: "false" });
  const eye = h("button", { type: "button", class: "btn ghost sm icon", "aria-label": "Afficher la valeur", title: "Afficher", hidden: !secret,
    onclick: () => { const show = v.type === "password"; v.type = show ? "text" : "password"; eye.replaceChildren(icon(show ? "eye-off" : "eye")); } }, icon("eye"));
  k.addEventListener("input", () => { const s = SENSITIVE.test(k.value); eye.hidden = !s; if (s && !v.value) v.type = "password"; });
  if (!locked) { k.addEventListener("change", commit); v.addEventListener("change", commit); }
  const row = h("div", { class: "it" }, k, h("div", { class: "val" }, v, eye),
    locked ? h("span") : h("button", { type: "button", class: "btn ghost sm icon", "aria-label": "Retirer", onclick: () => { row.remove(); commit(); } }, icon("x")));
  return row;
}

// ---------------------------------------------------------------- .env in graphical mode
// The text stays the source: values are rewritten in place, comments kept.
const ENV_LINE = /^\s*(export\s+)?([A-Za-z_][A-Za-z0-9_.-]*)\s*=(.*)$/;

function parseEnv(text) {
  const out = [];
  for (const line of text.split("\n")) {
    const m = line.match(ENV_LINE);
    if (!m) continue;
    let v = m[3].trim();
    if ((v.startsWith('"') && v.endsWith('"') && v.length > 1) || (v.startsWith("'") && v.endsWith("'") && v.length > 1)) v = v.slice(1, -1);
    out.push({ key: m[2], value: v });
  }
  return out;
}

function quoteEnv(v) {
  return /[\s#"'$\\]/.test(v) ? `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"` : v;
}

function writeEnv(text, kvs) {
  const want = new Map(kvs.map((x) => [x.key, x.value]));
  const lines = text === "" ? [] : text.replace(/\n$/, "").split("\n");
  const out = [];
  for (const line of lines) {
    const m = line.match(ENV_LINE);
    if (!m) { out.push(line); continue; }
    if (!want.has(m[2])) continue; // removed
    const cur = parseEnv(line)[0].value;
    out.push(cur === want.get(m[2]) ? line : `${m[1] || ""}${m[2]}=${quoteEnv(want.get(m[2]))}`);
    want.delete(m[2]);
  }
  for (const [k, v] of want) out.push(`${k}=${quoteEnv(v)}`);
  return out.length ? out.join("\n") + "\n" : "";
}

function envPanel() {
  const editor = kvEditor(parseEnv(st.env), !editable, (kvs) => {
    st.env = writeEnv(st.env, kvs);
    updateButtons();
    check();
  });
  return h("section", { class: "card vsvc" },
    h("div", { class: "vsvc-head" }, h("h3", {}, icon("env"), ".env"), h("span", { class: "grow" }),
      h("span", { class: "faint small" }, "Lu par compose : ${NOM} dans le fichier")),
    h("div", { class: "vgrid" }, h("div", { class: "vf wide" }, editor)));
}

// ---------------------------------------------------------------- secrets
function secretsPanel() {
  const out = [h("div", { class: "banner warn" }, icon("shield", false), h("span", {},
    h("b", {}, "Toutes les images ne savent pas lire un secret. "),
    "Il est monté en fichier dans ", h("code", {}, "/run/secrets/<nom>"),
    " : l'image doit lire ce chemin, souvent via une variable qui finit par ", h("code", {}, "_FILE"),
    ". Le fichier est créé en 0600 : si le conteneur ne tourne pas en root, donne-lui son ",
    h("code", {}, "uid"), " et un ", h("code", {}, "mode"), " dans la syntaxe longue du service (mode texte). ",
    "Les valeurs restent masquées tant que tu ne cliques pas sur Afficher."))];
  if (isNew) return out.concat(h("p", { class: "faint" }, "Crée d'abord la pile pour lui ajouter des secrets."));
  if (!st.model) return out.concat(h("p", { class: "faint" }, "Le fichier ne se lit pas : corrige-le en mode texte."));
  if (dirty()) {
    out.push(h("div", { class: "banner info" }, icon("info", false), h("span", {}, "Enregistre d'abord tes modifications en cours : les secrets écrivent directement dans le fichier.")));
  }
  const list = h("section", { class: "card secrets" });
  if (!st.model.secrets.length) list.append(h("p", { class: "empty" }, "Aucun secret dans cette pile."));
  for (const sec of st.model.secrets) list.append(secretRow(sec));
  out.push(list);
  if (editable && !dirty()) out.push(newSecretCard());
  return out;
}

const usersOf = (name) => st.model.services.filter((s) => s.secrets.includes(name)).map((s) => s.name);

function secretRow(sec) {
  const users = usersOf(sec.name);
  const val = h("div", { class: "secret-val" });
  const masked = () => {
    if (!sec.file) { val.replaceChildren(h("span", { class: "faint small" }, sec.external ? "secret externe" : "défini hors fichier")); return; }
    val.replaceChildren(h("span", { class: "mask", "aria-label": "valeur masquée" }, "••••••••"),
      h("button", { type: "button", class: "btn ghost sm", onclick: reveal }, icon("eye"), "Afficher"));
  };
  const reveal = async () => {
    const r = await fetch(`/s/${encodeURIComponent(stack)}/secrets/${encodeURIComponent(sec.name)}`);
    const b = await r.json().catch(() => ({}));
    if (!r.ok) return toast(b.error || "Lecture impossible", true);
    const input = h("input", { type: "text", value: b.value, class: "mono", "aria-label": `Valeur de ${sec.name}`, autocomplete: "off", spellcheck: "false", readonly: !editable || dirty() });
    val.replaceChildren(input,
      editable && !dirty() ? h("button", { type: "button", class: "btn sm", onclick: () => saveSecret(sec.name, input.value, users) }, "Enregistrer") : null,
      h("button", { type: "button", class: "btn ghost sm icon", "aria-label": "Masquer", title: "Masquer", onclick: masked }, icon("eye-off")));
    input.focus();
  };
  masked();
  return h("div", { class: "secret-row" },
    h("div", { class: "secret-name" }, h("strong", {}, icon("key"), sec.name),
      h("span", { class: "faint small" }, sec.file || (sec.external ? "externe" : sec.environment ? `variable ${sec.environment}` : "")),
      h("div", { class: "secret-users" }, users.length ? users.map((u) => h("span", { class: "chip" }, u)) : h("span", { class: "faint small" }, "aucun service"))),
    val,
    editable && !dirty() && sec.file ? h("button", { type: "button", class: "btn ghost sm icon", "aria-label": `Supprimer ${sec.name}`, title: "Supprimer",
      onclick: async () => {
        if (!(await confirmAsk(`Supprimer le secret ${sec.name} ?`, "Son fichier est effacé et les services ne le reçoivent plus.", "Supprimer"))) return;
        saveSecret(sec.name, null, [], true);
      } }, icon("trash")) : h("span"));
}

function newSecretCard() {
  const name = h("input", { type: "text", placeholder: "db_password", "aria-label": "Nom du secret", class: "mono", autocapitalize: "off", spellcheck: "false" });
  const value = h("input", { type: "password", placeholder: "valeur", "aria-label": "Valeur du secret", class: "mono", autocomplete: "new-password" });
  const svc = checks(st.model.services.map((s) => s.name), [], false, () => {});
  const add = () => {
    const n = name.value.trim();
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(n)) return toast("Nom : lettres, chiffres, . _ -", true);
    if (st.model.secrets.some((x) => x.name === n)) return toast("Ce secret existe déjà", true);
    saveSecret(n, value.value, $$("input:checked", svc).map((x) => x.dataset.name));
  };
  return h("section", { class: "card vsvc" },
    h("div", { class: "vsvc-head" }, h("h3", {}, icon("plus"), "Nouveau secret")),
    h("div", { class: "vgrid" },
      field("Nom", "key", name), field("Valeur", "lock", value),
      field("Services qui le reçoivent", "cube", svc, true),
      h("div", { class: "vf wide" }, h("button", { type: "button", class: "btn primary vadd", onclick: add }, "Ajouter le secret"))));
}

async function saveSecret(name, value, services, remove = false) {
  const body = { name, services, remove, base: st.hash };
  if (value !== null) body.value = value;
  const r = await post(`/s/${encodeURIComponent(stack)}/secrets`, body, true);
  if (!r.ok) return toast(r.body.error || "Échec", true);
  st.compose = st.saved.compose = r.body.compose;
  st.hash = r.body.hash;
  toast(remove ? "Secret supprimé" : "Secret enregistré · « Appliquer » pour le donner aux conteneurs");
  await check();
  renderVisual();
}

// ---------------------------------------------------------------- save
function updateButtons() {
  if (isNew || !editable) return;
  const d = dirty();
  $("#ed-reset").disabled = !d;
  $("#ed-save").disabled = !d || st.issues.length > 0;
  $("#ed-apply").disabled = st.issues.length > 0 || !composeOk;
  $("#ed-apply").textContent = d ? "Enregistrer et appliquer" : "Appliquer";
}

function renderDiff(d) {
  const box = $("#diff-body");
  box.replaceChildren();
  for (const [name, lines] of [["compose", d.compose], [".env", d.env]]) {
    if (!lines || !lines.some((l) => l && l.Op !== 32)) continue;
    const pre = h("pre", { class: "diff" });
    for (const l of lines) {
      if (!l) { pre.append(h("div", { class: "gap" }, "⋯")); continue; }
      const op = String.fromCharCode(l.Op);
      // .env values may be secrets: show which keys changed, not their values.
      let txt = l.Text;
      if (name === ".env") { const m = txt.match(ENV_LINE); if (m && SENSITIVE.test(m[2])) txt = `${m[1] || ""}${m[2]}=••••••`; }
      pre.append(h("div", { class: op === "+" ? "add" : op === "-" ? "del" : "" }, h("span", { class: "ln" }, l.New || l.Old), `${op} ${txt}`));
    }
    box.append(h("div", { class: "diff-title" }, name), pre);
  }
}

async function save(apply) {
  if (dirty()) {
    const d = await post(`/s/${encodeURIComponent(stack)}/diff`, { compose: st.compose, env: st.env }, true);
    if (!d.ok) return toast(d.body.error || "Échec", true);
    renderDiff(d.body);
    const dlg = $("#diff-dialog");
    $("#diff-ok").textContent = apply ? "Enregistrer et appliquer" : "Enregistrer";
    dlg.returnValue = "";
    dlg.showModal();
    const ok = await new Promise((res) => dlg.addEventListener("close", () => res(dlg.returnValue === "yes"), { once: true }));
    if (!ok) return;
  }
  const r = await post(`/s/${encodeURIComponent(stack)}/save`, { compose: st.compose, env: st.env, apply, base: st.hash }, true);
  if (r.status === 422) { st.issues = r.body.issues || []; renderCheck(); return; }
  if (r.status === 409) return toast(r.body.error, true);
  if (!r.ok) return toast(r.body.error || "Échec", true);
  st.saved = { compose: st.compose, env: st.env };
  st.hash = r.body.hash || st.hash;
  updateButtons();
  renderCheck();
  if (st.part === "secrets") renderVisual();
  if (r.body.id) {
    sessionStorage.setItem("dockerr-op", JSON.stringify({ id: r.body.id, stack, op: "start" }));
    location.href = `/s/${encodeURIComponent(stack)}`;
  } else {
    toast("Enregistré");
  }
}

async function create(start) {
  const name = $("#ns-name").value.trim();
  const err = $("#ns-name-err");
  err.hidden = true;
  const r = await post("/new", { name, compose: st.compose, env: st.env, start }, true);
  if (r.status === 422) {
    if (r.body.field === "name") { err.textContent = r.body.error; err.hidden = false; $("#ns-name").focus(); }
    st.issues = r.body.issues || [];
    renderCheck();
    return;
  }
  if (!r.ok) return toast(r.body.error || "Échec", true);
  if (r.body.id) sessionStorage.setItem("dockerr-op", JSON.stringify({ id: r.body.id, stack: name, op: "start" }));
  location.href = r.body.url;
}

// ---------------------------------------------------------------- start
async function init() {
  $$("button[data-mode]", root).forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.mode === st.mode)));
  if (isNew) {
    st.compose = NEW_TEMPLATE;
    $("#ns-create").onclick = () => create(false);
    $("#ns-start").onclick = () => create(true);
    if (!composeOk) $("#ns-start").disabled = true;
    $("#ns-name").addEventListener("input", scheduleCheck);
    $("#ns-name").focus();
  } else {
    const r = await fetch(`/s/${encodeURIComponent(stack)}/files`);
    const body = await r.json();
    if (!r.ok) { st.issues = [{ message: body.error || "Lecture impossible" }]; renderCheck(); return; }
    st.compose = body.compose;
    st.env = body.env;
    st.saved = { compose: body.compose, env: body.env };
    st.hash = body.hash;
    if (editable) {
      $("#ed-reset").onclick = () => { st.compose = st.saved.compose; st.env = st.saved.env; render(); updateButtons(); check([]); };
      $("#ed-save").onclick = () => save(false);
      $("#ed-apply").onclick = () => save(true);
      addEventListener("beforeunload", (e) => { if (dirty()) e.preventDefault(); });
      document.addEventListener("keydown", (e) => {
        if ((e.ctrlKey || e.metaKey) && e.key === "s") { e.preventDefault(); if (!$("#ed-save").disabled) save(false); }
      });
    }
  }
  await check();
  render();
  updateButtons();
}

init();
