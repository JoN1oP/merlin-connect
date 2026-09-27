// Merlin Connect UI. Plain ES module, no build step. All strings live in T.

const T = {
  offline: "Non connecté",
  waiting: "Maintenez le bouton de Merlin 5 secondes…",
  waitingManual: "Rejoignez le WiFi MERLIN_… (mot de passe MERLIN_APP)",
  joining: (ssid) => `Connexion à ${ssid}…`,
  reading: "Lecture du contenu de Merlin…",
  connected: (ssid) => `Connecté à ${ssid || "Merlin"}`,
  syncing: "Synchronisation en cours",
  lost: "Connexion perdue",
  connect: "Connecter",
  cancel: "Annuler",
  disconnect: "Déconnecter",
  reconnect: "Reconnecter",
  free: (size) => `${size} libres`,
  battery: (pct, charging) => `Batterie ${pct} %${charging ? " (en charge)" : ""}`,
  seen: (date) => `Contenu vu le ${date}`,
  root: "Merlin",
  special: { Merlin_favorite: "Favoris", Merlin_discover: "À découvrir" },
  empty: "Ce dossier est vide. Ajoutez des fichiers ou importez un dossier.",
  emptyRoot: "Connectez Merlin pour voir ses histoires, ou ajoutez les vôtres dès maintenant.",
  emptyFolder: "Vide, non envoyé",
  unsent: "Pas encore sur Merlin",
  official: "Contenu officiel (lecture seule)",
  pendingNone: "Tout est sur Merlin",
  pending: (n) => `${n} changement${n > 1 ? "s" : ""} à envoyer`,
  pendingOffline: (n) => `${n} changement${n > 1 ? "s" : ""} à envoyer à la prochaine connexion`,
  phase: {
    files: (label, i, n) => `Envoi de « ${label} » (${i}/${n})`,
    playlist: "Mise à jour de la liste…",
    verify: "Vérification…",
  },
  percent: (n) => `${n} %`,
  speed: (bytesPerSec) => bytesPerSec >= 1e6
    ? `${new Intl.NumberFormat("fr-FR", { maximumFractionDigits: 1 }).format(bytesPerSec / 1e6)} Mo/s`
    : `${Math.round(bytesPerSec / 1e3)} Ko/s`,
  left: (sec) => sec < 60 ? "moins d'une minute" : sec < 3600
    ? `environ ${Math.round(sec / 60)} min`
    : `environ ${Math.floor(sec / 3600)} h ${String(Math.round((sec % 3600) / 60)).padStart(2, "0")}`,
  sync: "Synchroniser",
  newFolder: "Nouveau dossier",
  folderName: "Nom du dossier",
  create: "Créer",
  rename: "Renommer",
  save: "Enregistrer",
  changeCover: "Changer l'image",
  move: "Déplacer…",
  moveTo: (title) => `Déplacer « ${title} » vers`,
  remove: "Supprimer",
  removeTitle: (title) => `Supprimer « ${title} » ?`,
  removeNote: "L'élément disparaîtra de Merlin à la prochaine synchronisation. Ses fichiers restent sur la carte mémoire de l'enceinte.",
  close: "Fermer",
  importing: "Import en cours…",
  imported: (r) => [
    r.stories && `${r.stories} histoire${r.stories > 1 ? "s" : ""} ajoutée${r.stories > 1 ? "s" : ""}`,
    r.folders && `${r.folders} dossier${r.folders > 1 ? "s" : ""}`,
    r.skipped && `${r.skipped} déjà présente${r.skipped > 1 ? "s" : ""}`,
    r.placeholders && `${r.placeholders} sans image (image par défaut)`,
  ].filter(Boolean).join(", ") || "Aucun fichier MP3 trouvé.",
  noMp3: "Aucun fichier MP3 trouvé.",
  failed: "L'opération a échoué.",
};

const $ = (id) => document.getElementById(id);
let snap = { status: { state: "offline", pending: 0 }, tree: { children: [] } };
let path = []; // UUIDs of the open folders, from the root
let gridKey = "";
let lastError = "";

// --- API ---------------------------------------------------------------

async function api(method, url, body) {
  const opts = { method, headers: { "X-Merlin": "1" } };
  if (body instanceof FormData || body instanceof Blob) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.body = JSON.stringify(body);
    opts.headers["Content-Type"] = "application/json";
  }
  const res = await fetch(url, opts);
  if (!res.ok) {
    let message = T.failed;
    try { message = (await res.json()).error || message; } catch { /* not JSON */ }
    throw new Error(message);
  }
  return res.status === 204 ? null : res.json();
}

async function act(fn) {
  try { await fn(); } catch (err) { toast(err.message, true); }
}

// --- Helpers -----------------------------------------------------------

function el(tag, props = {}, ...children) {
  const node = Object.assign(document.createElement(tag), props);
  for (const child of children.flat()) {
    if (child != null && child !== false) node.append(child);
  }
  return node;
}

const displayTitle = (node) => T.special[node.title] || node.title;
const isSpecial = (node) => node.title in T.special;

function formatBytes(bytes) {
  const fmt = new Intl.NumberFormat("fr-FR", { maximumFractionDigits: 1 });
  return bytes >= 1e9 ? `${fmt.format(bytes / 1e9)} Go` : `${fmt.format(bytes / 1e6)} Mo`;
}

let toastTimer;
function toast(message, error = false) {
  const t = $("toast");
  t.textContent = message;
  t.classList.toggle("error", error);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, error ? 6000 : 3500);
}

function currentFolder() {
  let node = snap.tree;
  const trail = [];
  for (const id of path) {
    const next = (node.children || []).find((c) => c.uuid === id && c.folder);
    if (!next) break;
    trail.push(next);
    node = next;
  }
  path = trail.map((n) => n.uuid);
  return { node, trail };
}

function allFolders() {
  const out = [];
  const walk = (node, depth) => {
    for (const c of node.children || []) {
      if (c.folder && !isSpecial(c)) {
        out.push({ node: c, depth });
        walk(c, depth + 1);
      }
    }
  };
  walk(snap.tree, 1);
  return out;
}

// --- Rendering -----------------------------------------------------------

function render() {
  const { node, trail } = currentFolder();
  renderLink();
  renderBar(node, trail);
  renderGrid(node);
  renderDock();
  const error = snap.status.error || "";
  if (error && error !== lastError) toast(error, true);
  lastError = error;
}

function renderLink() {
  const st = snap.status;
  document.body.dataset.state = st.state;
  const title = {
    offline: T.offline,
    waiting: st.manual ? T.waitingManual : T.waiting,
    joining: T.joining(st.ssid),
    reading: T.reading,
    connected: T.connected(st.ssid),
    syncing: T.syncing,
    lost: T.lost,
  }[st.state];
  $("link-title").textContent = title;

  const detail = $("link-detail");
  detail.replaceChildren();
  if (st.info) {
    detail.append(el("span", { textContent: T.battery(st.info.battery, st.info.charging) }));
    detail.append(el("span", { textContent: T.free(formatBytes(st.info.freeBytes)) }));
  } else if (st.seen && st.state === "offline") {
    const date = new Date(st.seen).toLocaleDateString("fr-FR", { day: "numeric", month: "long" });
    detail.append(el("span", { textContent: T.seen(date) }));
  }

  const action = $("link-action");
  const label = { offline: T.connect, waiting: T.cancel, joining: T.cancel, reading: T.disconnect, connected: T.disconnect, lost: T.reconnect }[st.state];
  action.hidden = !label;
  action.textContent = label || "";
}

function renderBar(node, trail) {
  const crumbs = [{ title: T.root, depth: 0 }, ...trail.map((n, i) => ({ title: displayTitle(n), depth: i + 1 }))];
  $("crumbs").replaceChildren(...crumbs.map((c, i) => {
    if (i === crumbs.length - 1) return el("li", {}, el("span", { textContent: c.title, ariaCurrent: "page" }));
    return el("li", {}, el("button", { textContent: c.title, onclick: () => { path = path.slice(0, c.depth); render(); } }));
  }));
  $("tools").hidden = trail.some(isSpecial);
}

function renderGrid(node) {
  const key = JSON.stringify([path, node.children]);
  if (key === gridKey) return;
  gridKey = key;

  const children = node.children || [];
  const empty = $("empty");
  empty.hidden = children.length > 0;
  empty.textContent = path.length === 0 ? T.emptyRoot : T.empty;
  $("grid").replaceChildren(...children.map((child) => card(child, children)));
}

function card(node, siblings) {
  const initial = displayTitle(node).trim().charAt(0).toUpperCase();
  const cover = el("button", { className: "cover", tabIndex: node.folder ? 0 : -1, ariaLabel: displayTitle(node) });
  const missing = () => { cover.classList.add("missing"); cover.textContent = initial; };
  if (node.v) {
    const img = el("img", { alt: "", loading: "lazy", src: `/covers/${node.uuid}?v=${node.v}` });
    img.onerror = missing;
    cover.append(img);
  } else missing(); // the box cover is not fetched yet
  if (node.folder) cover.onclick = () => { path = [...path, node.uuid]; render(); };

  const caption = el("div", { className: "caption" },
    node.custom && !node.onBox && (!node.folder || node.children) && el("span", { className: "unsent", title: T.unsent }),
    !node.custom && lockIcon(),
    el("span", { className: "title" },
      displayTitle(node),
      node.custom && node.folder && !node.children && el("span", { className: "note", textContent: T.emptyFolder })),
  );

  const li = el("li", { className: `card${node.folder ? " folder" : ""}` }, cover, caption);
  if (node.custom) {
    li.append(el("button", { className: "more", textContent: "⋯", ariaLabel: `${T.rename}, ${T.move}, ${T.remove}`, onclick: () => itemSheet(node) }));
    enableReorder(li, node, siblings);
  }
  return li;
}

function lockIcon() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 12 12");
  svg.setAttribute("class", "lock");
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", T.official);
  svg.innerHTML = '<rect x="2" y="5" width="8" height="6" rx="1.5" fill="currentColor"/><path d="M4 5V3.5a2 2 0 0 1 4 0V5" fill="none" stroke="currentColor" stroke-width="1.4"/>';
  return svg;
}

// speed measures the upload rate over the last few seconds. Files already on
// the box count in "done" but not in "sent", so they never inflate it.
const speed = {
  samples: [],
  sample(sent) {
    const now = performance.now();
    const last = this.samples.at(-1);
    if (last && sent < last.sent) this.samples = []; // a new sync
    if (!last || sent !== last.sent) this.samples.push({ t: now, sent });
    while (this.samples.length > 2 && now - this.samples[1].t > 5000) this.samples.shift();
    const first = this.samples[0];
    const span = (now - first.t) / 1000;
    return span >= 2 ? (sent - first.sent) / span : 0;
  },
};

function renderDock() {
  const st = snap.status;
  const syncing = st.state === "syncing";
  const progress = $("progress");
  progress.hidden = !syncing;
  if (!syncing) speed.samples = [];
  const sync = $("sync");
  if (syncing && st.progress) {
    const p = st.progress;
    const pct = p.total ? Math.floor((100 * p.done) / p.total) : 100;
    $("progress-fill").style.width = `${pct}%`;
    $("pending").textContent = p.phase === "files" ? T.phase.files(p.label || "…", p.file, p.files) : T.phase[p.phase];
    const rate = speed.sample(p.sent);
    const stats = [T.percent(pct)];
    if (rate > 0 && p.phase === "files") stats.push(T.speed(rate), T.left((p.total - p.done) / rate));
    $("progress-stats").textContent = stats.join(" · ");
  } else if (st.pending === 0) {
    $("pending").textContent = T.pendingNone;
  } else {
    $("pending").textContent = st.state === "connected" ? T.pending(st.pending) : T.pendingOffline(st.pending);
  }
  sync.textContent = syncing ? T.cancel : T.sync;
  sync.classList.toggle("primary", !syncing);
  sync.disabled = !syncing && !(st.state === "connected" && st.pending > 0);
}

// --- Dialogs ---------------------------------------------------------------

function openDialog(...content) {
  const dialog = $("dialog");
  dialog.replaceChildren(...content);
  dialog.showModal();
  return dialog;
}

function closeDialog() { $("dialog").close(); }

function askText(title, value, confirmLabel) {
  return new Promise((resolve) => {
    const input = el("input", { type: "text", value, maxLength: 64, required: true });
    const form = el("form", { method: "dialog" },
      el("h2", { textContent: title }),
      input,
      el("div", { className: "actions" },
        el("button", { type: "button", textContent: T.cancel, onclick: () => { closeDialog(); resolve(null); } }),
        el("button", { type: "submit", className: "primary", textContent: confirmLabel })));
    form.onsubmit = () => resolve(input.value);
    openDialog(form).onclose = () => resolve(null);
    input.select();
  });
}

function itemSheet(node) {
  const title = displayTitle(node);
  openDialog(
    el("h2", { textContent: title }),
    el("div", { className: "menu" },
      el("button", { textContent: T.rename, onclick: () => rename(node) }),
      el("button", { textContent: T.changeCover, onclick: () => { closeDialog(); pickCover(node); } }),
      el("button", { textContent: T.move, onclick: () => moveDialog(node) }),
      el("button", { className: "danger", textContent: T.remove, onclick: () => removeDialog(node) })),
    el("div", { className: "actions" }, el("button", { textContent: T.close, onclick: closeDialog })));
}

async function rename(node) {
  const title = await askText(T.rename, node.title, T.save);
  if (title !== null) act(() => api("PATCH", `/api/items/${node.uuid}`, { title }));
}

function moveDialog(node) {
  const blocked = new Set([node.uuid]);
  const collect = (n) => (n.children || []).forEach((c) => { blocked.add(c.uuid); collect(c); });
  collect(node);
  const choice = (label, parent, depth) => el("button", {
    textContent: label,
    style: `padding-left: ${16 + depth * 18}px`,
    onclick: () => { closeDialog(); act(() => api("PATCH", `/api/items/${node.uuid}`, { parent })); },
  });
  openDialog(
    el("h2", { textContent: T.moveTo(displayTitle(node)) }),
    el("div", { className: "folders" },
      choice(T.root, "", 0),
      allFolders().filter((f) => !blocked.has(f.node.uuid)).map((f) => choice(displayTitle(f.node), f.node.uuid, f.depth))),
    el("div", { className: "actions" }, el("button", { textContent: T.cancel, onclick: closeDialog })));
}

function removeDialog(node) {
  openDialog(
    el("h2", { textContent: T.removeTitle(displayTitle(node)) }),
    el("p", { textContent: T.removeNote }),
    el("div", { className: "actions" },
      el("button", { textContent: T.cancel, onclick: closeDialog }),
      el("button", {
        className: "danger-fill",
        textContent: T.remove,
        onclick: () => { closeDialog(); act(() => api("DELETE", `/api/items/${node.uuid}`)); },
      })));
}

let coverTarget = null;
function pickCover(node) {
  coverTarget = node;
  $("pick-cover").value = "";
  $("pick-cover").click();
}

// --- Import ------------------------------------------------------------------

const importable = /\.(mp3|jpe?g|png|webp|bmp)$/i;

async function upload(entries) {
  entries = entries.filter((e) => importable.test(e.path));
  if (!entries.some((e) => /\.mp3$/i.test(e.path))) {
    toast(T.noMp3, true);
    return;
  }
  const form = new FormData();
  for (const { path: rel, file } of entries) {
    form.append("path", rel);
    form.append("file", file, file.name);
  }
  toast(T.importing);
  const parent = path[path.length - 1] || "";
  const result = await api("POST", `/api/import?parent=${encodeURIComponent(parent)}`, form);
  toast(T.imported(result));
}

// Folders dropped on the page are read through the File System entries API.
async function droppedEntries(items) {
  const out = [];
  const readAll = (reader) => new Promise((resolve, reject) => {
    const all = [];
    const next = () => reader.readEntries((batch) => (batch.length ? (all.push(...batch), next()) : resolve(all)), reject);
    next();
  });
  const visit = async (entry) => {
    if (entry.isFile) {
      const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
      out.push({ path: entry.fullPath.replace(/^\//, ""), file });
    } else if (entry.isDirectory) {
      for (const child of await readAll(entry.createReader())) await visit(child);
    }
  };
  const entries = [...items].map((item) => item.webkitGetAsEntry && item.webkitGetAsEntry()).filter(Boolean);
  for (const entry of entries) await visit(entry);
  return out;
}

// --- Reordering custom items -----------------------------------------------------

let dragged = null;

function enableReorder(li, node, siblings) {
  const custom = siblings.filter((s) => s.custom);
  li.draggable = true;
  li.ondragstart = (e) => {
    dragged = node;
    li.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", node.uuid);
  };
  li.ondragend = () => { dragged = null; li.classList.remove("dragging"); };
  li.ondragover = (e) => {
    if (!dragged || dragged === node || !custom.includes(dragged)) return;
    e.preventDefault();
    li.classList.add("drop-target");
  };
  li.ondragleave = () => li.classList.remove("drop-target");
  li.ondrop = (e) => {
    li.classList.remove("drop-target");
    if (!dragged || !custom.includes(dragged)) return;
    e.preventDefault();
    e.stopPropagation();
    const position = custom.indexOf(node);
    const uuid = dragged.uuid;
    act(() => api("PATCH", `/api/items/${uuid}`, { position }));
  };
}

// --- Wiring ----------------------------------------------------------------------

function wire() {
  $("link-action").onclick = () => {
    const state = snap.status.state;
    const endpoint = { offline: "connect", lost: "connect", waiting: "cancel", joining: "cancel", reading: "disconnect", connected: "disconnect" }[state];
    if (endpoint) act(() => api("POST", `/api/${endpoint}`));
  };
  $("sync").onclick = () => act(() => api("POST", snap.status.state === "syncing" ? "/api/cancel" : "/api/sync"));
  $("new-folder").onclick = async () => {
    const title = await askText(T.newFolder, "", T.create);
    if (title !== null) act(() => api("POST", "/api/folders", { parent: path[path.length - 1] || "", title }));
  };
  $("add-files").onclick = () => { $("pick-files").value = ""; $("pick-files").click(); };
  $("add-folder").onclick = () => { $("pick-folder").value = ""; $("pick-folder").click(); };
  $("pick-files").onchange = (e) => act(() => upload([...e.target.files].map((file) => ({ path: file.name, file }))));
  $("pick-folder").onchange = (e) => act(() => upload([...e.target.files].map((file) => ({ path: file.webkitRelativePath || file.name, file }))));
  $("pick-cover").onchange = (e) => {
    const file = e.target.files[0];
    if (file && coverTarget) act(() => api("PUT", `/api/items/${coverTarget.uuid}/cover`, file));
  };

  document.addEventListener("dragover", (e) => {
    if (!dragged && e.dataTransfer.types.includes("Files") && !$("tools").hidden) e.preventDefault();
  });
  document.addEventListener("drop", (e) => {
    if (dragged || !e.dataTransfer.types.includes("Files") || $("tools").hidden) return;
    e.preventDefault();
    act(async () => upload(await droppedEntries(e.dataTransfer.items)));
  });

  const events = new EventSource("/api/events");
  events.onmessage = (e) => { snap = JSON.parse(e.data); render(); };
}

wire();
render();
