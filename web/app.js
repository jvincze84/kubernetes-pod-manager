'use strict';

// ---------- segédek ----------
const $ = (sel, root = document) => root.querySelector(sel);
const app = $('#app');

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const enc = encodeURIComponent;

const KIND_LABEL = { deployment: 'Deployment', statefulset: 'StatefulSet', daemonset: 'DaemonSet' };

async function api(method, path, { raw = false, signal } = {}) {
  const headers = {};
  if (method !== 'GET') headers['X-Requested-With'] = 'pod-manager';
  const res = await fetch('api/' + path, { method, headers, signal, cache: 'no-store' });
  if (!res.ok) {
    let msg = res.status + ' ' + res.statusText;
    try { const j = await res.json(); if (j.error) msg = j.error; } catch { /* nem JSON */ }
    throw new Error(msg);
  }
  return raw ? res : res.json();
}

function toast(msg, type = '') {
  const el = document.createElement('div');
  el.className = 'toast ' + type;
  el.textContent = msg;
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), type === 'err' ? 8000 : 4000);
}

function age(ts) {
  if (!ts) return '';
  let s = Math.max(0, Math.floor((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 60) return s + 's';
  let m = Math.floor(s / 60); if (m < 60) return m + 'm';
  let h = Math.floor(m / 60); if (h < 48) return h + 'h' + (h < 10 ? ' ' + (m % 60) + 'm' : '');
  return Math.floor(h / 24) + 'd';
}

function statusClass(pod) {
  const s = pod.status || '';
  const [a, b] = (pod.ready || '0/0').split('/');
  if (/Err|BackOff|Fail|OOM|Evicted|Unknown|ExitCode|Signal|NotReady|Invalid|Lost/i.test(s)) return 'b-red';
  if (s === 'Running') return a === b ? 'b-green' : 'b-yellow';
  if (s === 'Completed' || s === 'Succeeded') return 'b-gray';
  if (s === 'Terminating') return 'b-gray';
  return 'b-yellow';
}

function containerColor(c) {
  if (c.state === 'Running') return c.ready ? 'var(--green)' : 'var(--yellow)';
  if (c.state === 'Completed') return 'var(--gray)';
  if (/Err|BackOff|OOM|Error|exit/i.test(c.state)) return 'var(--red)';
  return 'var(--yellow)';
}

function readyBadge(ready, desired) {
  const cls = desired === 0 ? 'b-gray' : ready >= desired ? 'b-green' : ready === 0 ? 'b-red' : 'b-yellow';
  return `<span class="badge ${cls}">${ready}/${desired}</span>`;
}

function setCrumbs(parts) {
  const html = ['<a href="#/">Namespaces</a>'];
  for (const p of parts) {
    html.push('<span class="sep">/</span>' + (p.href ? `<a href="${esc(p.href)}">${esc(p.label)}</a>` : esc(p.label)));
  }
  $('#crumbs').innerHTML = html.join('');
}

// ---------- megerősítő ablak ----------
function confirmDialog({ title = 'Confirm', text, typeToConfirm = null, okLabel = 'Yes' }) {
  return new Promise((resolve) => {
    const modal = $('#confirmModal'), input = $('#confirmInput'), ok = $('#confirmOk');
    $('#confirmTitle').textContent = title;
    $('#confirmText').textContent = text;
    ok.textContent = okLabel;
    $('#confirmTypeWrap').classList.toggle('hidden', !typeToConfirm);
    $('#confirmExpected').textContent = typeToConfirm || '';
    input.value = '';
    ok.disabled = !!typeToConfirm;
    input.oninput = () => { ok.disabled = input.value !== typeToConfirm; };
    const done = (v) => {
      modal.classList.add('hidden');
      ok.onclick = $('#confirmCancel').onclick = input.onkeydown = document.onkeydown = null;
      resolve(v);
    };
    ok.onclick = () => done(true);
    $('#confirmCancel').onclick = () => done(false);
    document.onkeydown = (e) => { if (e.key === 'Escape') done(false); };
    input.onkeydown = (e) => { if (e.key === 'Enter' && !ok.disabled) done(true); };
    modal.classList.remove('hidden');
    (typeToConfirm ? input : ok).focus();
  });
}

async function runAction(btn, fn) {
  if (btn) btn.disabled = true;
  try { await fn(); } catch (e) { toast(e.message, 'err'); } finally { if (btn) btn.disabled = false; }
}

function deleteSummary(res) {
  const failed = Object.keys(res.failed || {});
  if (failed.length) toast(`Failed to delete (${failed.length}): ${failed.join(', ')}`, 'err');
  toast(`${res.deleted.length} pod(s) deleted`, 'ok');
}

// ---------- POD tábla (közös) ----------
function podTable(ns, pods, { showOwner = false, filter = '' } = {}) {
  const f = filter.toLowerCase();
  const list = f ? pods.filter((p) => p.name.toLowerCase().includes(f) || p.status.toLowerCase().includes(f) || (p.node || '').toLowerCase().includes(f)) : pods;
  if (!list.length) return `<div class="empty">${pods.length ? 'No matches' : 'No pods'}</div>`;
  const rows = list.map((p) => {
    const dots = p.containers.map((c) =>
      `<span class="cdot" style="background:${containerColor(c)}" title="${esc((c.init ? 'init: ' : '') + c.name + ' – ' + c.state + (c.restarts ? ', restart: ' + c.restarts : ''))}"></span>`).join('');
    return `<tr>
      <td><span class="mono">${esc(p.name)}</span><div class="containers">${dots}</div></td>
      <td><span class="badge ${statusClass(p)}">${esc(p.status)}</span></td>
      <td>${esc(p.ready)}</td>
      <td>${p.restarts ? `<span style="color:var(--yellow)">${p.restarts}</span>` : 0}</td>
      <td title="${esc(p.created)}">${age(p.created)}</td>
      ${showOwner ? `<td class="small muted">${esc(p.owner || '-')}</td>` : ''}
      <td class="small">${esc(p.node || '-')}</td>
      <td class="small mono">${esc(p.ip || '-')}</td>
      <td class="actions">
        <button class="btn sm" data-act="logs" data-pod="${esc(p.name)}">Logs</button>
        ${p.status === 'Terminating' ? `<button class="btn sm warn" data-act="force-delete" data-pod="${esc(p.name)}">Force delete</button>` : ''}
        <button class="btn sm danger" data-act="delete-pod" data-pod="${esc(p.name)}">Delete</button>
      </td></tr>`;
  }).join('');
  return `<table><thead><tr>
    <th>Name</th><th>Status</th><th>Ready</th><th>Restarts</th><th>Age</th>
    ${showOwner ? '<th>Owner</th>' : ''}<th>Node</th><th>IP</th><th></th>
  </tr></thead><tbody>${rows}</tbody></table>`;
}

// POD táblában lévő gombok kezelése
function bindPodActions(root, ns, getPods, reload) {
  root.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-act]');
    if (!btn) return;
    const pod = btn.dataset.pod;
    if (btn.dataset.act === 'logs') {
      const p = getPods().find((x) => x.name === pod);
      openLogs(ns, pod, p ? p.containers : []);
    } else if (btn.dataset.act === 'delete-pod' || btn.dataset.act === 'force-delete') {
      const force = btn.dataset.act === 'force-delete';
      confirmDialog({
        title: force ? 'Force delete pod' : 'Delete pod',
        text: `Delete pod "${pod}" in namespace "${ns}"?` + (force ? ' (grace period: 0)' : ''),
        okLabel: 'Delete',
      }).then((ok) => ok && runAction(btn, async () => {
        await api('DELETE', `namespaces/${enc(ns)}/pods/${enc(pod)}${force ? '?force=true' : ''}`);
        toast(`${pod} deleted`, 'ok');
        reload();
      }));
    }
  });
}

// ---------- nézetek ----------
let current = null; // { load: fn } – az aktuális nézet frissítő függvénye

function viewNamespaces() {
  setCrumbs([]);
  const saved = sessionStorage.getItem('nsFilter') || '';
  app.innerHTML = `
    <h2>Namespaces</h2>
    <div class="sub">Start typing a namespace name. Press Enter to open the first match.</div>
    <input type="search" id="nsSearch" class="search-big" placeholder="Search namespaces…" value="${esc(saved)}" autofocus>
    <div id="nsList" class="ns-grid"></div>`;
  const search = $('#nsSearch'), listEl = $('#nsList');
  let all = [];

  const render = () => {
    const f = search.value.trim().toLowerCase();
    sessionStorage.setItem('nsFilter', search.value);
    const items = f ? all.filter((n) => n.name.toLowerCase().includes(f)) : all;
    listEl.innerHTML = items.length ? items.map((n, i) => `
      <a class="ns-card${i === 0 && f ? ' sel' : ''}" href="#/ns/${enc(n.name)}">
        <span class="name">${esc(n.name)}</span>
        <span class="ns-meta">
          ${n.status !== 'Active' ? `<span class="badge b-yellow">${esc(n.status)}</span>` : ''}
          <span class="podcount" title="Running / pending / failing pods${n.stuck ? ` (${n.stuck} pending for over 5 min)` : ''}"><span class="${n.running ? 'c-green' : 'muted'}">${n.running}</span><span class="muted">/</span><span class="${n.stuck ? 'c-red' : n.pending ? 'c-yellow' : 'muted'}">${n.pending}${n.stuck ? '!' : ''}</span><span class="muted">/</span><span class="${n.errors ? 'c-red' : 'muted'}">${n.errors}</span></span>
        </span>
      </a>`).join('') : '<div class="empty">No matches</div>';
    return items;
  };
  search.addEventListener('input', render);
  search.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      const items = render();
      if (items.length) location.hash = '#/ns/' + enc(items[0].name);
    }
  });
  search.focus();
  search.setSelectionRange(search.value.length, search.value.length);

  return {
    load: async () => { all = await api('GET', 'namespaces'); render(); },
  };
}

function viewNamespace(ns, tab) {
  setCrumbs([{ label: ns }]);
  tab = tab === 'pods' ? 'pods' : 'workloads';
  app.innerHTML = `
    <h2>${esc(ns)}</h2>
    <div class="toolbar">
      <a class="btn ${tab === 'pods' ? 'primary' : ''}" href="#/ns/${enc(ns)}/pods">List all pods</a>
      <button class="btn warn" id="clean" title="Delete all Completed (Succeeded) pods">Clean <span class="count" id="cntC">–</span></button>
      <button class="btn danger" id="delAll">Delete all pods</button>
      <span class="spacer"></span>
      <input type="search" id="filter" placeholder="Filter…">
    </div>
    <div class="tabs">
      <a class="tab ${tab === 'workloads' ? 'active' : ''}" href="#/ns/${enc(ns)}">Workloads <span class="count" id="cntW">–</span></a>
      <a class="tab ${tab === 'pods' ? 'active' : ''}" href="#/ns/${enc(ns)}/pods">Pods <span class="count" id="cntP">–</span></a>
    </div>
    <div id="content"><div class="empty">Loading…</div></div>`;

  const content = $('#content'), filter = $('#filter');
  let workloads = [], pods = [];

  const render = () => {
    const f = filter.value.trim().toLowerCase();
    $('#cntW').textContent = workloads.length;
    $('#cntP').textContent = pods.length;
    $('#cntC').textContent = pods.filter((p) => p.phase === 'Succeeded').length;
    if (tab === 'pods') { content.innerHTML = podTable(ns, pods, { showOwner: true, filter: f }); return; }

    const list = f ? workloads.filter((w) => w.name.toLowerCase().includes(f) || w.kind.includes(f)) : workloads;
    if (!list.length) { content.innerHTML = `<div class="empty">${workloads.length ? 'No matches' : 'No Deployments / StatefulSets / DaemonSets'}</div>`; return; }
    content.innerHTML = `<table><thead><tr>
        <th>Kind</th><th>Name</th><th>Ready</th><th>Up-to-date</th><th>Image</th><th>Age</th><th></th>
      </tr></thead><tbody>${list.map((w) => {
        const href = `#/ns/${enc(ns)}/${w.kind}/${enc(w.name)}`;
        return `<tr class="clickable" data-href="${esc(href)}">
          <td><span class="badge b-blue kind">${KIND_LABEL[w.kind]}</span></td>
          <td><a href="${esc(href)}">${esc(w.name)}</a></td>
          <td>${readyBadge(w.ready, w.desired)}</td>
          <td>${w.updated}</td>
          <td class="small muted mono">${w.images.map(esc).join('<br>')}</td>
          <td>${age(w.created)}</td>
          <td class="actions">
            <button class="btn sm danger" data-wact="delete-pods" data-kind="${w.kind}" data-name="${esc(w.name)}">Delete pods</button>
            <button class="btn sm warn" data-wact="restart" data-kind="${w.kind}" data-name="${esc(w.name)}">Rollout restart</button>
            ${w.kind === 'deployment' ? scaleButtons(w, 'sm') : ''}
          </td></tr>`;
      }).join('')}</tbody></table>`;
  };

  const load = async () => {
    // Mindkét fülön mindkettőt lekérjük, hogy a számlálók mindig helyesek legyenek.
    [workloads, pods] = await Promise.all([
      api('GET', `namespaces/${enc(ns)}/workloads`),
      api('GET', `namespaces/${enc(ns)}/pods`),
    ]);
    render();
  };

  filter.addEventListener('input', render);
  bindPodActions(content, ns, () => pods, () => load().catch((e) => toast(e.message, 'err')));
  content.addEventListener('click', (e) => {
    const btn = e.target.closest('button[data-wact]');
    if (btn) { workloadAction(btn, ns, btn.dataset.kind, btn.dataset.name, load); return; }
    const row = e.target.closest('tr[data-href]');
    if (row && !e.target.closest('a,button')) location.hash = row.dataset.href;
  });

  $('#clean').addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const n = pods.filter((p) => p.phase === 'Succeeded').length;
    if (!n) { toast('There are no Completed pods in this namespace'); return; }
    const ok = await confirmDialog({
      title: 'Delete Completed pods',
      text: `${n} Completed (Succeeded) pod(s) in namespace "${ns}" will be deleted.`,
      okLabel: 'Clean',
    });
    if (ok) runAction(btn, async () => {
      deleteSummary(await api('POST', `namespaces/${enc(ns)}/clean-completed`));
      await load();
    });
  });

  $('#delAll').addEventListener('click', async (e) => {
    const btn = e.currentTarget;
    const ok = await confirmDialog({
      title: 'Delete all pods',
      text: `ALL pods in namespace "${ns}" will be deleted (${pods.length || '?'}). Pods managed by a controller will be recreated; all others are gone for good.`,
      okLabel: 'Delete all',
    });
    if (ok) runAction(btn, async () => {
      deleteSummary(await api('DELETE', `namespaces/${enc(ns)}/pods?confirm=${enc(ns)}`));
      await load();
    });
  });

  return { load };
}

function scaleButtons(w, size = '') {
  return [0, 1].map((n) => `<button class="btn ${size}" data-wact="scale" data-replicas="${n}" data-kind="${w.kind}" data-name="${esc(w.name)}"${w.desired === n ? ' disabled title="Already at this replica count"' : ''}>Scale to ${n}</button>`).join(' ');
}

async function workloadAction(btn, ns, kind, name, reload) {
  if (btn.dataset.wact === 'scale') {
    const n = btn.dataset.replicas;
    const ok = await confirmDialog({
      title: `Scale to ${n}`,
      text: `Deployment "${name}" will be scaled to ${n} replica(s).` + (n === '0' ? ' All of its pods will stop!' : ''),
      okLabel: `Scale to ${n}`,
    });
    if (ok) runAction(btn, async () => {
      await api('POST', `namespaces/${enc(ns)}/workloads/deployment/${enc(name)}/scale?replicas=${n}`);
      toast(`${name}: scaled to ${n}`, 'ok');
      await reload();
    });
  } else if (btn.dataset.wact === 'restart') {
    const ok = await confirmDialog({
      title: 'Rollout restart',
      text: `Rollout restart of ${KIND_LABEL[kind]} "${name}". Its pods are restarted one by one, following the rollout strategy.`,
      okLabel: 'Restart',
    });
    if (ok) runAction(btn, async () => {
      await api('POST', `namespaces/${enc(ns)}/workloads/${kind}/${enc(name)}/restart`);
      toast(`${name}: rollout restart started`, 'ok');
      await reload();
    });
  } else {
    const ok = await confirmDialog({
      title: 'Delete pods',
      text: `All pods of ${KIND_LABEL[kind]} "${name}" will be deleted at once (the controller recreates them). This can cause a short outage!`,
      okLabel: 'Delete pods',
    });
    if (ok) runAction(btn, async () => {
      deleteSummary(await api('DELETE', `namespaces/${enc(ns)}/workloads/${kind}/${enc(name)}/pods`));
      await reload();
    });
  }
}

function viewWorkload(ns, kind, name) {
  setCrumbs([{ label: ns, href: `#/ns/${enc(ns)}` }, { label: `${KIND_LABEL[kind] || kind}/${name}` }]);
  app.innerHTML = `
    <h2><span class="badge b-blue kind">${esc(KIND_LABEL[kind] || kind)}</span> ${esc(name)}</h2>
    <div class="toolbar">
      <button class="btn danger" data-wact="delete-pods">Delete pods</button>
      <button class="btn warn" data-wact="restart">Rollout restart</button>
      <span id="scaleBtns"></span>
      <span class="spacer"></span>
      <input type="search" id="filter" placeholder="Filter pods…">
    </div>
    <dl class="info-grid" id="info"></dl>
    <h3>Pods</h3>
    <div id="content"><div class="empty">Loading…</div></div>`;

  const content = $('#content'), filter = $('#filter');
  let pods = [];
  const render = () => { content.innerHTML = podTable(ns, pods, { filter: filter.value.trim() }); };
  const load = async () => {
    const data = await api('GET', `namespaces/${enc(ns)}/workloads/${enc(kind)}/${enc(name)}`);
    const w = data.workload;
    pods = data.pods;
    if (kind === 'deployment') $('#scaleBtns').innerHTML = scaleButtons(w);
    $('#info').innerHTML = `
      <dt>Ready</dt><dd>${readyBadge(w.ready, w.desired)} <span class="muted small">up-to-date: ${w.updated}</span></dd>
      <dt>Selector</dt><dd class="mono small">${esc(w.selector)}</dd>
      <dt>Image</dt><dd class="mono small">${w.images.map(esc).join('<br>')}</dd>
      <dt>Age</dt><dd>${age(w.created)}</dd>`;
    render();
  };
  filter.addEventListener('input', render);
  bindPodActions(content, ns, () => pods, () => load().catch((e) => toast(e.message, 'err')));
  // Delegálás, mert a scale gombok minden frissítéskor újrarajzolódnak
  app.querySelector('.toolbar').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-wact]');
    if (b) workloadAction(b, ns, kind, name, load);
  });
  return { load };
}

// ---------- log ablak ----------
const log = { ns: '', pod: '', abort: null, text: '', timer: null };
const MAX_LOG_CHARS = 5_000_000;

function openLogs(ns, pod, containers) {
  Object.assign(log, { ns, pod });
  $('#logTitle').textContent = `${ns} / ${pod}`;
  const sel = $('#logContainer');
  const list = containers.length ? containers : [{ name: '', init: false, state: '' }];
  sel.innerHTML = list.map((c) =>
    `<option value="${esc(c.name)}">${esc((c.init ? 'init: ' : '') + (c.name || '(default)'))}${c.state ? ' – ' + esc(c.state) : ''}</option>`).join('');
  // Alapértelmezés: első nem-init konténer
  const firstMain = list.find((c) => !c.init);
  if (firstMain) sel.value = firstMain.name;
  $('#logFilter').value = '';
  $('#logModal').classList.remove('hidden');
  loadLogs();
}

function closeLogs() {
  if (log.abort) log.abort.abort();
  log.abort = null;
  $('#logModal').classList.add('hidden');
  $('#logOutput').textContent = '';
  log.text = '';
}

function logQuery(extra = {}) {
  const p = new URLSearchParams({
    container: $('#logContainer').value,
    tail: $('#logTail').value,
    previous: $('#logPrevious').checked,
    timestamps: $('#logTimestamps').checked,
    ...extra,
  });
  return `namespaces/${enc(log.ns)}/pods/${enc(log.pod)}/logs?${p}`;
}

function renderLog() {
  const out = $('#logOutput');
  const atBottom = out.scrollHeight - out.scrollTop - out.clientHeight < 40;
  const f = $('#logFilter').value.toLowerCase();
  out.textContent = f ? log.text.split('\n').filter((l) => l.toLowerCase().includes(f)).join('\n') : log.text;
  if (atBottom) out.scrollTop = out.scrollHeight;
}

function scheduleRender() {
  if (!log.timer) log.timer = setTimeout(() => { log.timer = null; renderLog(); }, 150);
}

async function loadLogs() {
  if (log.abort) log.abort.abort();
  const ctrl = new AbortController();
  log.abort = ctrl;
  log.text = '';
  const out = $('#logOutput');
  out.textContent = 'Loading…';
  const follow = $('#logFollow').checked;
  $('#logDownload').href = 'api/' + logQuery({ download: 'true' });

  try {
    const res = await api('GET', logQuery({ follow }), { raw: true, signal: ctrl.signal });
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    out.textContent = '';
    out.scrollTop = out.scrollHeight;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      log.text += dec.decode(value, { stream: true });
      if (log.text.length > MAX_LOG_CHARS) log.text = log.text.slice(-MAX_LOG_CHARS * 0.8);
      scheduleRender();
    }
    if (follow && log.abort === ctrl) {
      log.text += '\n--- stream closed ---\n';
    }
    if (!log.text) log.text = '(empty log)';
    renderLog();
    if (!follow) out.scrollTop = out.scrollHeight;
  } catch (e) {
    if (e.name !== 'AbortError') out.textContent = 'Error: ' + e.message;
  }
}

$('#logClose').addEventListener('click', closeLogs);
$('#logModal').addEventListener('click', (e) => { if (e.target.id === 'logModal') closeLogs(); });
['#logContainer', '#logTail', '#logFollow', '#logPrevious', '#logTimestamps'].forEach((s) =>
  $(s).addEventListener('change', loadLogs));
$('#logReload').addEventListener('click', loadLogs);
$('#logFilter').addEventListener('input', renderLog);
$('#logWrap').addEventListener('change', (e) => $('#logOutput').classList.toggle('wrap', e.target.checked));
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && !$('#logModal').classList.contains('hidden') && $('#confirmModal').classList.contains('hidden')) closeLogs();
});

// ---------- router + frissítés ----------
let loading = false;
async function refresh() {
  if (!current || loading) return;
  loading = true;
  try { await current.load(); } catch (e) { toast(e.message, 'err'); } finally { loading = false; }
}

function route() {
  closeLogs();
  const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  if (parts[0] === 'ns' && parts[1]) {
    if (parts.length >= 4) current = viewWorkload(parts[1], parts[2], parts[3]);
    else current = viewNamespace(parts[1], parts[2]);
  } else {
    current = viewNamespaces();
  }
  loading = false;
  refresh();
}

window.addEventListener('hashchange', route);
$('#refresh').addEventListener('click', refresh);
const autoEl = $('#autorefresh');
autoEl.checked = localStorage.getItem('autorefresh') !== 'false';
autoEl.addEventListener('change', () => localStorage.setItem('autorefresh', autoEl.checked));
setInterval(() => {
  if (autoEl.checked && !document.hidden && $('#confirmModal').classList.contains('hidden')) refresh();
}, 5000);

route();
