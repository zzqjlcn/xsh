'use strict';
/* ============================================================
   xsh Web UI — 原型 1:1 落地（渲染层与真实后端契约融合）
   后端契约：/api/auth/status · /api/login|logout · /api/hosts(CRUD)
             /ws/terminal(WebSocket) · xterm + FitAddon
   ============================================================ */
const sessions = new Map();
let hosts = [], token = '', editingOriginal = '', connectHost = '', activeSessionId = '', nextSessionId = 1, setupRequired = false, selectedIdx = -1;

const masterDialog = document.querySelector('#master-login');
const hostDialog = document.querySelector('#dlg-host');
const connectDialog = document.querySelector('#dlg-connect');
const confirmDialog = document.querySelector('#dlg-confirm');
const hostForm = document.querySelector('#host-form');
const drawer = document.querySelector('#host-drawer');
const backdrop = document.querySelector('#drawer-backdrop');
const drawerList = document.querySelector('#drawer-list');
const searchInput = document.querySelector('#host-search');
const countChip = document.querySelector('#host-count-chip');
const appEl = document.querySelector('#app');
const emptyState = document.querySelector('#empty-state');
const tabstrip = document.querySelector('#tabstrip');
const paneArea = document.querySelector('#pane-area');
const recentWrap = document.querySelector('#recent-wrap');
const recentChips = document.querySelector('#recent-chips');
const hostField = name => hostForm.elements.namedItem(name);

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function drawerOpen() { return drawer.classList.contains('open'); }
function authLabel(value) {
  return ({ auto: '自动', password: '密码', private_key: '私钥' })[value] || value || '自动';
}
function timeAgo(timestamp) {
  if (!timestamp) return '从未';
  const seconds = Math.max(0, Math.floor(Date.now() / 1000 - timestamp));
  if (seconds < 60) return '刚刚';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days === 1) return '昨天';
  if (days < 7) return `${days} 天前`;
  return new Date(timestamp * 1000).toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' });
}
function isSessionActive(hostName) {
  for (const session of sessions.values()) {
    if (session.host === hostName && (session.state === 'connecting' || session.state === 'connected')) return true;
  }
  return false;
}

/* ---------------- 登录 / 解锁 ---------------- */
async function loadHosts() {
  const response = await fetch('/api/hosts');
  if (response.status === 401) { await lockView(); return false; }
  if (!response.ok) throw new Error(await response.text());
  const data = await response.json(); hosts = data.hosts || []; token = data.token;
  appEl.hidden = false;
  if (masterDialog.open) masterDialog.close();
  updateCount(); renderDrawer(); renderRecent(); return true;
}
async function lockView(status) {
  appEl.hidden = true; closeDrawer();
  document.querySelector('#master-error').textContent = '';
  if (!status) {
    const statusResponse = await fetch('/api/auth/status');
    status = statusResponse.ok ? await statusResponse.json() : {};
  }
  setupRequired = Boolean(status.setup_required);
  document.querySelector('#master-title').textContent = setupRequired ? '创建密码本' : 'xsh Web';
  document.querySelector('#master-copy').textContent = setupRequired ? '首次使用，请创建密码本主密码。之后登录和读取主机密码均使用此密码。' : '输入密码本（encrypted vault）的 master password 以解锁已保存的凭据。';
  document.querySelector('#master-confirm-row').hidden = !setupRequired;
  document.querySelector('#master-confirm').required = setupRequired;
  document.querySelector('#master-submit').textContent = setupRequired ? '创建并进入' : '解锁终端';
  if (!masterDialog.open) masterDialog.showModal();
  setTimeout(() => document.querySelector('#master-password').focus(), 0);
}
document.querySelector('#master-form').onsubmit = async event => {
  event.preventDefault();
  const passwordInput = document.querySelector('#master-password'), confirmInput = document.querySelector('#master-confirm');
  if (setupRequired && passwordInput.value !== confirmInput.value) { document.querySelector('#master-error').textContent = '两次输入的 master password 不一致。'; confirmInput.focus(); return; }
  const response = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: passwordInput.value, create: setupRequired }) });
  passwordInput.value = ''; confirmInput.value = '';
  if (!response.ok) { document.querySelector('#master-error').textContent = (await response.text()).trim(); passwordInput.focus(); return; }
  token = (await response.json()).token; await loadHosts();
};
masterDialog.addEventListener('cancel', event => event.preventDefault());

/* ---------------- 主机抽屉 ---------------- */
function openDrawer() {
  renderDrawer();
  backdrop.hidden = false;
  requestAnimationFrame(() => { backdrop.classList.add('open'); drawer.classList.add('open'); drawer.setAttribute('aria-hidden', 'false'); });
  setTimeout(() => { if (!searchInput.value) searchInput.focus(); }, 240);
}
function closeDrawer() {
  backdrop.classList.remove('open');
  drawer.classList.remove('open');
  drawer.setAttribute('aria-hidden', 'true');
  setTimeout(() => { if (!backdrop.classList.contains('open')) backdrop.hidden = true; }, 220);
}
function updateCount() { if (countChip) countChip.textContent = hosts.length; }
function filteredHosts() {
  const q = searchInput.value.trim().toLowerCase();
  return hosts.filter(h => [h.name, h.hostname, h.user, ...(h.tags || [])].join(' ').toLowerCase().includes(q));
}
const PENCIL_SVG = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4L19.5 8.5a2.1 2.1 0 0 0-3-3L5 17v3Z"/><path d="m13.5 6.5 3 3"/></svg>';
function hostRowHTML(h, i) {
  const firstTag = (h.tags || []).slice(0, 2);
  const initial = escapeHtml((h.name || '?').charAt(0).toUpperCase());
  const when = escapeHtml(h.last_login ? timeAgo(h.last_login) : '从未');
  const dot = isSessionActive(h.name) ? '' : ' off';
  const tags = firstTag.length ? `<span class="host-tags">${firstTag.map(t => `<span class="tag">${escapeHtml(t)}</span>`).join('')}</span>` : '';
  return `<div class="host-item${i === selectedIdx ? ' sel' : ''}" data-idx="${i}" role="button" tabindex="0" aria-label="连接 ${escapeHtml(h.name)}">
    <span class="av" data-host="${escapeHtml(h.name)}">${initial}</span>
    <span class="host-main">
      <span class="host-name-row"><span class="host-name"><span class="dot${dot}"></span> ${escapeHtml(h.name)}</span></span>
      <span class="host-addr">${escapeHtml(h.user)}@${escapeHtml(h.hostname)}:${h.port}</span>
      ${tags}
    </span>
    <span class="host-side"><span class="when">${when}</span></span>
    <button class="row-edit" type="button" aria-label="编辑 ${escapeHtml(h.name)}" title="编辑（e）">${PENCIL_SVG}</button>
  </div>`;
}
function renderDrawer() {
  updateCount();
  const list = filteredHosts();
  if (!hosts.length) { drawerList.innerHTML = '<div class="drawer-empty"><span class="glyph">⌁</span>还没有主机，点「新增」创建第一条连接。</div>'; return; }
  if (!list.length) { drawerList.innerHTML = '<div class="drawer-empty"><span class="glyph">Ø</span>没有匹配的主机。<br>换个关键词试试。</div>'; return; }
  if (selectedIdx >= list.length) selectedIdx = list.length - 1;
  if (selectedIdx < 0) selectedIdx = 0;
  drawerList.innerHTML = list.map((h, i) => hostRowHTML(h, i)).join('');
  drawerList.querySelectorAll('.host-item').forEach(row => {
    const h = list[+row.dataset.idx];
    row.addEventListener('click', event => { if (!event.target.closest('.row-edit')) openConnect(h); });
    row.addEventListener('keydown', event => {
      if (event.key === 'e' || event.key === 'E') { event.preventDefault(); openHostModal(h); }
      else if (event.key === 'Delete') { event.preventDefault(); askDelete(); }
    });
    row.querySelector('.row-edit').addEventListener('click', event => { event.stopPropagation(); openHostModal(h); });
  });
}
function setSel(i) {
  const list = filteredHosts();
  if (!list.length) return;
  selectedIdx = Math.max(0, Math.min(list.length - 1, i));
  renderDrawer();
  drawerList.querySelectorAll('.host-item').forEach(row => {
    if (+row.dataset.idx === selectedIdx) { row.focus({ preventScroll: false }); row.scrollIntoView({ block: 'nearest' }); }
  });
}
document.addEventListener('keydown', event => {
  if (!drawerOpen()) return;
  if (!drawerList.contains(event.target) && !searchInput.contains(event.target)) return;
  if (event.target.tagName === 'BUTTON') return;
  if (event.key === 'Escape') {
    if (event.target === searchInput && searchInput.value) { searchInput.value = ''; selectedIdx = -1; renderDrawer(); }
    else closeDrawer();
    return;
  }
  const list = filteredHosts();
  if (!list.length) return;
  const typing = event.target === searchInput;
  if (event.key === 'ArrowDown') { event.preventDefault(); setSel(selectedIdx + 1); }
  else if (event.key === 'ArrowUp') { event.preventDefault(); setSel(selectedIdx - 1); }
  else if (event.key === 'Enter' || (event.key === ' ' && !typing)) { event.preventDefault(); openConnect(list[Math.max(0, selectedIdx)]); }
});
searchInput.addEventListener('input', () => { selectedIdx = -1; renderDrawer(); });
document.querySelector('#btn-open-hosts').addEventListener('click', openDrawer);
document.querySelector('#empty-open').addEventListener('click', openDrawer);
document.querySelector('#drawer-close').addEventListener('click', closeDrawer);
backdrop.addEventListener('click', closeDrawer);
/* 「新增」按钮：与 a 快捷键一致 → 关闭抽屉后打开新增主机对话框 */
document.querySelector('#btn-add-host').addEventListener('click', () => { closeDrawer(); setTimeout(() => openHostModal(), 80); });

/* 全局快捷键：/ 与 q 打开/关闭列表 · a 新增（仅非输入态） */
document.addEventListener('keydown', event => {
  if (appEl.hidden) return;
  if (document.querySelector('dialog[open]')) return;
  if (/INPUT|TEXTAREA|SELECT/.test(event.target.tagName)) return;
  if (event.key === '/' || event.key === 'q' || event.key === 'Q') {
    event.preventDefault();
    drawerOpen() ? closeDrawer() : openDrawer();
  }
  else if (event.key === 'a' || event.key === 'A') { event.preventDefault(); closeDrawer(); setTimeout(() => openHostModal(), 200); }
});

/* ---------------- 主机编辑 / 删除 ---------------- */
function authValue() { const rl = hostField('auth_type'); return (rl && rl.value) || 'auto'; }
function syncAuthUI() {
  const mode = authValue();
  document.querySelector('#row-key').hidden = mode !== 'private_key';
  document.querySelector('#row-passhint').hidden = mode !== 'password';
  hostField('identity_file').required = mode === 'private_key';
}
for (const radio of document.querySelectorAll('input[name="auth_type"]')) radio.addEventListener('change', syncAuthUI);

function openHostModal(host) {
  editingOriginal = host?.name || '';
  hostForm.reset();
  const jump = hostField('proxy_jump'); jump.replaceChildren(new Option('不使用跳板', ''));
  for (const candidate of hosts) if (candidate.name !== editingOriginal) jump.add(new Option(candidate.name, candidate.name));
  if (host?.proxy_jump && !hosts.some(candidate => candidate.name === host.proxy_jump && candidate.name !== editingOriginal)) jump.add(new Option(host.proxy_jump, host.proxy_jump));
  if (host) {
    for (const key of ['name', 'hostname', 'user', 'port', 'identity_file']) hostField(key).value = host[key] ?? '';
    hostField('name').readOnly = true;
    hostField('tags').value = (host.tags || []).join(', ');
    if (jump.value === '' && host.proxy_jump) jump.value = host.proxy_jump;
    hostField('auth_type').value = host.auth_type || 'auto';
  } else {
    hostField('name').readOnly = false;
  }
  syncAuthUI();
  document.querySelector('#host-title').textContent = host ? `编辑 ${host.name}` : '新增主机';
  document.querySelector('#btn-del-host').hidden = !host;
  const msg = document.querySelector('#host-msg'); msg.textContent = ''; msg.className = 'form-msg';
  hostDialog.showModal();
  setTimeout(() => (host ? hostField('hostname') : hostField('name')).focus(), 0);
}
hostForm.onsubmit = async event => {
  event.preventDefault();
  const host = Object.fromEntries(new FormData(hostForm));
  host.port = Number(host.port);
  host.tags = host.tags.split(/[,，]/).map(value => value.trim()).filter(Boolean);
  await mutateHost(editingOriginal ? 'PUT' : 'POST', host);
};
async function mutateHost(method, host) {
  const response = await fetch('/api/hosts', {
    method,
    headers: { 'Content-Type': 'application/json', 'X-XSH-Token': token },
    body: JSON.stringify({ original: editingOriginal, host })
  });
  if (response.status === 401) { lockView(); return; }
  if (!response.ok) {
    const msg = document.querySelector('#host-msg');
    msg.textContent = await response.text(); msg.className = 'form-msg error';
    return;
  }
  if (hostDialog.open) hostDialog.close();
  if (confirmDialog.open) confirmDialog.close();
  await loadHosts();
}
document.querySelector('#btn-del-host').addEventListener('click', () => { hostDialog.close(); askDelete(); });
function askDelete() {
  document.querySelector('#confirm-target').textContent = editingOriginal;
  confirmDialog.showModal();
}
document.querySelector('#confirm-ok').addEventListener('click', () => mutateHost('DELETE', {}));

/* ---------------- 连接 ---------------- */
function openConnect(host) {
  connectHost = host.name;
  closeDrawer();
  document.querySelector('#cc-av').textContent = (host.name || '?').charAt(0).toUpperCase();
  document.querySelector('#cc-name').textContent = host.name;
  document.querySelector('#cc-addr').textContent = `${host.user}@${host.hostname}:${host.port}`;
  document.querySelector('#cc-auth').textContent = authLabel(host.auth_type);
  const pass = document.querySelector('#cc-pass'); pass.value = '';
  const msg = document.querySelector('#connect-msg'); msg.textContent = ''; msg.className = 'form-msg';
  connectDialog.showModal();
  setTimeout(() => pass.focus(), 0);
}
document.querySelector('#connect-form').onsubmit = event => {
  event.preventDefault();
  const pass = document.querySelector('#cc-pass');
  const password = pass.value; pass.value = '';
  connectDialog.close();
  openTerminal(connectHost, password);
};

/* ---------------- 终端会话 ---------------- */
function updateWorkspace() { emptyState.hidden = sessions.size > 0; }
function setSessionState(session, state, label) {
  session.state = state;
  session.tab.dataset.st = state;
  session.tab.title = `${session.host} · ${label}`;
}
function activateSession(id) {
  const session = sessions.get(id); if (!session) return;
  activeSessionId = id;
  for (const item of sessions.values()) {
    const active = item.id === id;
    item.pane.classList.toggle('active', active);
    item.tab.setAttribute('aria-selected', String(active));
    item.tab.tabIndex = active ? 0 : -1;
  }
  requestAnimationFrame(() => { try { session.fitAddon.fit(); session.terminal.focus(); } catch (_) {} });
}
function openTerminal(host, password) {
  const id = `terminal-${nextSessionId++}`;
  const tab = document.createElement('div');
  tab.className = 'tab'; tab.dataset.st = 'connecting'; tab.setAttribute('role', 'tab'); tab.tabIndex = -1;
  tab.innerHTML = `<span class="tab-state"></span><span class="tab-label">${escapeHtml(host)}</span><button class="tab-close" type="button" title="关闭 ${escapeHtml(host)}" aria-label="关闭 ${escapeHtml(host)}">✕</button>`;
  tabstrip.append(tab);
  const pane = document.createElement('div');
  pane.className = 'pane'; pane.id = id; pane.setAttribute('role', 'tabpanel');
  paneArea.append(pane);
  const terminal = new Terminal({
    cursorBlink: true, convertEol: true,
    fontFamily: 'Cascadia Mono,Cascadia Code,Consolas,monospace',
    fontSize: 14, fontWeight: '400', fontWeightBold: '700',
    drawBoldTextInBrightColors: true, minimumContrastRatio: 1, scrollback: 10000,
    theme: termTheme()
  });
  const fitAddon = new FitAddon.FitAddon();
  terminal.loadAddon(fitAddon); terminal.open(pane);
  const session = { id, host, tab, pane, terminal, fitAddon, socket: null, resizeObserver: null, state: 'connecting', auth: { password } };
  sessions.set(id, session); updateWorkspace();
  tab.onclick = event => { if (!event.target.closest('.tab-close')) activateSession(id); };
  tab.onauxclick = event => { if (event.button === 1) { event.preventDefault(); closeSession(id); } };
  tab.onkeydown = event => { if (event.target === tab && (event.key === 'Enter' || event.key === ' ')) activateSession(id); };
  tab.querySelector('.tab-close').onclick = event => { event.stopPropagation(); closeSession(id); };
  session.resizeObserver = new ResizeObserver(() => { if (activeSessionId === id) { try { fitAddon.fit(); } catch (_) {} } });
  session.resizeObserver.observe(pane);
  activateSession(id);
  terminal.writeln('\x1b[38;5;108mxsh\x1b[0m connecting…');
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:';
  session.socket = new WebSocket(`${scheme}//${location.host}/ws/terminal?token=${encodeURIComponent(token)}`);
  session.socket.onopen = () => {
    const auth = session.auth; session.auth = null;
    sendTerminal(session, { type: 'connect', host, password: auth.password, cols: terminal.cols, rows: terminal.rows });
    auth.password = ''; password = '';
  };
  session.socket.onmessage = event => {
    const data = JSON.parse(event.data);
    if (data.type === 'output') terminal.write(data.data);
    if (data.type === 'connected') {
      setSessionState(session, 'connected', '已连接');
      terminal.write('\r\n');
      const known = hosts.find(h => h.name === host);
      if (known) { known.last_login = Math.floor(Date.now() / 1000); renderRecent(); renderDrawer(); }
    }
    if (data.type === 'error') { setSessionState(session, 'error', '连接失败'); terminal.writeln(`\r\n\x1b[91m${escapeHtml(data.message)}\x1b[0m`); }
    if (data.type === 'closed') { setSessionState(session, 'closed', '已断开'); terminal.writeln(`\r\n\x1b[90m${escapeHtml(data.message)}\x1b[0m`); }
    if (data.type === 'trust') sendTerminal(session, { type: 'trust', trust: confirm(`首次连接 ${data.message}\n\n密钥类型：${data.key_type}\n指纹：${data.fingerprint}\n\n确认信任并保存该主机密钥？`) });
  };
  session.socket.onerror = () => { if (sessions.has(id)) setSessionState(session, 'error', '连接出错'); };
  session.socket.onclose = () => { if (sessions.has(id) && session.state === 'connected') setSessionState(session, 'closed', '已断开'); };
  terminal.onData(data => sendTerminal(session, { type: 'input', data }));
  terminal.onResize(size => sendTerminal(session, { type: 'resize', cols: size.cols, rows: size.rows }));
}
function sendTerminal(session, message) { if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(JSON.stringify(message)); }
function closeSession(id) {
  const session = sessions.get(id); if (!session) return;
  sendTerminal(session, { type: 'close' });
  if (session.socket) session.socket.close();
  session.resizeObserver.disconnect(); session.terminal.dispose();
  session.tab.remove(); session.pane.remove();
  sessions.delete(id); updateWorkspace();
  if (!sessions.size) activeSessionId = '';
  else if (activeSessionId === id) activateSession(Array.from(sessions.keys()).at(-1));
}

/* ---------------- 空状态最近使用 chips ---------------- */
function renderRecent() {
  const recent = hosts.filter(h => h.last_login).sort((a, b) => b.last_login - a.last_login).slice(0, 3);
  recentWrap.hidden = !recent.length;
  if (!recent.length) return;
  recentChips.innerHTML = recent.map(h => `<button class="chip-host" data-n="${escapeHtml(h.name)}"><span class="dot off"></span>${escapeHtml(h.name)}</button>`).join('');
  recentChips.querySelectorAll('.chip-host').forEach(button => {
    button.addEventListener('click', () => { const h = hosts.find(x => x.name === button.dataset.n); if (h) openConnect(h); });
  });
}

/* ---------------- 防误关（Ctrl+W / Ctrl+Shift+W） ----------------
   浏览器把 Ctrl+W / Ctrl+Shift+W 视为「保留快捷键」，普通标签页内页面脚本无法完全吞掉：
   真实浏览器中关闭动作由浏览器进程执行 → 依赖下方 beforeunload 的原生「离开确认」兜底；
   在内嵌 WebView / 应用窗口等可拦截环境里，此处理器仍尽量 preventDefault。
   结束会话请使用标签页 ✕；锁定请用顶栏 🔒。 */
let warnedTermClose = false;
document.addEventListener('keydown', event => {
  if (event.altKey || !(event.ctrlKey || event.metaKey)) return;
  if (event.key !== 'w' && event.key !== 'W') return;
  if (appEl.hidden || sessions.size === 0) return;
  event.preventDefault(); /* 可拦截环境下不放行关标签页；事件仍会继续传播 → xterm 可收到 Ctrl+W 做删词 */
  if (!warnedTermClose) {
    warnedTermClose = true;
    const session = sessions.get(activeSessionId) || Array.from(sessions.values())[0];
    if (session) { try { session.terminal.writeln('\r\n\x1b[90m[guard] Ctrl+W 已拦截，不会关闭标签页；如需结束会话请点击标签页 ✕。\x1b[0m'); } catch (_) {} }
  }
});

/* ---------------- 锁定 ---------------- */
document.querySelector('#btn-lock').addEventListener('click', async () => {
  for (const id of Array.from(sessions.keys())) closeSession(id);
  await fetch('/api/logout', { method: 'POST', headers: { 'X-XSH-Token': token } });
  token = ''; await lockView();
});
/* 有活跃 SSH 会话时，浏览器关闭标签/窗口（含 Ctrl+W / Ctrl+Shift+W）会触发原生「离开确认」，
   取消即可保住会话；无会话或处于登录页时不打扰。 */
window.addEventListener('beforeunload', event => {
  if (appEl.hidden || sessions.size === 0) return;
  event.preventDefault();
  event.returnValue = '';
});

/* 通用：data-close 关闭对话框 / data-eye 显隐密码 */
for (const button of document.querySelectorAll('[data-close]')) {
  button.onclick = () => { const dialog = document.querySelector('#' + button.dataset.close); if (dialog && typeof dialog.close === 'function') dialog.close(); };
}
for (const button of document.querySelectorAll('[data-eye]')) {
  button.onclick = () => {
    const input = document.getElementById(button.dataset.eye); if (!input) return;
    const reveal = input.type === 'password';
    input.type = reveal ? 'text' : 'password';
    button.setAttribute('aria-label', reveal ? '隐藏密码' : '显示/隐藏密码');
  };
}

/* ============================================================
   主题切换：深/亮（持久化）＋ xterm 联动
   ============================================================ */
const themeToggle = document.querySelector('#theme-toggle');
function termTheme() {
  const css = getComputedStyle(document.documentElement);
  const value = n => css.getPropertyValue(n).trim();
  return {
    background: value('--term-bg') || value('--bg-inset') || '#0A0E14',
    foreground: value('--x-tx') || '#DDE6EF',
    cursor: value('--acc'),
    cursorAccent: value('--bg-0'),
    selectionBackground: value('--ring'),
    black: value('--x-0'), red: value('--x-1'), green: value('--x-2'), yellow: value('--x-3'),
    blue: value('--x-4'), magenta: value('--x-5'), cyan: value('--x-6'), white: value('--x-7'),
    brightBlack: value('--x-8'), brightRed: value('--x-9'), brightGreen: value('--x-10'), brightYellow: value('--x-11'),
    brightBlue: value('--x-12'), brightMagenta: value('--x-13'), brightCyan: value('--x-14'), brightWhite: value('--x-15')
  };
}
function applyTerminalTheme() {
  const theme = termTheme();
  for (const session of sessions.values()) { try { session.terminal.options.theme = theme; } catch (_) {} }
}
function setTheme(mode) {
  document.documentElement.setAttribute('data-theme', mode);
  try { localStorage.setItem('xsh-theme', mode); } catch (_) {}
  const light = mode === 'light';
  if (themeToggle) {
    themeToggle.setAttribute('aria-pressed', String(light));
    themeToggle.innerHTML = light
      ? '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
      : '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/></svg>';
  }
  applyTerminalTheme();
}
let savedTheme = 'dark';
try { savedTheme = localStorage.getItem('xsh-theme') || 'dark'; } catch (_) {}
setTheme(savedTheme);
if (themeToggle) themeToggle.addEventListener('click', () => setTheme(document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark'));

async function initialize() {
  const statusResponse = await fetch('/api/auth/status');
  const status = statusResponse.ok ? await statusResponse.json() : {};
  if (status.authenticated) await loadHosts();
  else await lockView(status);
}
initialize().catch(async error => { await lockView(); document.querySelector('#master-error').textContent = error.message; });
