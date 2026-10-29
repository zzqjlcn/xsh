'use strict';
document.addEventListener('contextmenu', event => event.preventDefault(), true);
/* ============================================================
   xsh Web UI — 原型 1:1 落地（渲染层与真实后端契约融合）
   后端契约：/api/auth/status · /api/login|logout · /api/hosts(CRUD)
             /ws/terminal(WebSocket) · xterm + FitAddon
   ============================================================ */
const sessions = new Map();
const lastLoginStorageKey = `xsh-last-login:${new URL('.', location.href).pathname}`;
const workspaceTabs = new Map();
let activeTabId = '', nextTabId = 1, connectPlacement = null;
let hosts = [], token = '', editingOriginal = '', connectHost = '', activeSessionId = '', nextSessionId = 1, setupRequired = false, selectedIdx = -1;

const masterDialog = document.querySelector('#master-login');
const screenLockDialog = document.querySelector('#screen-lock');
let screenLocked = false, settingsUsers = [];
const hostDialog = document.querySelector('#dlg-host');
const connectDialog = document.querySelector('#dlg-connect');
const confirmDialog = document.querySelector('#dlg-confirm');
const hostForm = document.querySelector('#host-form');
const drawer = document.querySelector('#host-drawer');
const backdrop = document.querySelector('#drawer-backdrop');
const drawerList = document.querySelector('#drawer-list');
const hostMenu = document.querySelector('#host-context-menu');
let contextHostName = '';
const searchInput = document.querySelector('#host-search');
const countChip = document.querySelector('#host-count-chip');
const appEl = document.querySelector('#app');
const emptyState = document.querySelector('#empty-state');
const tabstrip = document.querySelector('#tabstrip');
const paneArea = document.querySelector('#pane-area');
const recentWrap = document.querySelector('#recent-wrap');
const recentChips = document.querySelector('#recent-chips');
const hostField = name => hostForm.elements.namedItem(name);
let currentUser = null, acceptingShareId = 0, sharingHost = '', legacyVaultRequired = false, authGeneration = 0;

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function drawerOpen() { return drawer.classList.contains('open'); }
function authLabel(value) {
  return ({ auto: '密码', password: '密码', private_key: '私钥' })[value] || value || '密码';
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
  const generation = authGeneration;
  const response = await fetch('api/hosts');
  if (generation !== authGeneration) return false;
  if (response.status === 401) { await lockView(); return false; }
  if (response.status === 423) { showScreenLock(); return false; }
  if (!response.ok) throw new Error(await response.text());
  const data = await response.json();
  if (generation !== authGeneration) return false;
  hosts = data.hosts || []; token = data.token; currentUser = data.user;
  loadSecuritySettings();
  document.querySelector('#settings-nav-users').hidden = !currentUser?.admin;
  appEl.hidden = false;
  if (masterDialog.open) masterDialog.close();
  updateCount(); renderDrawer(); renderRecent(); if (!workspaceTabs.size) newHomeTab(); return true;
}
async function lockView(status) {
  const generation = ++authGeneration;
  securitySettingsKey = ''; lockNeedsSync = false; lockRequest = null;
  screenLocked = false; settingsUsers = [];
  document.querySelector('#screen-unlock-password').value = '';
  for (const id of Array.from(sessions.keys())) closeSession(id);
  for (const id of Array.from(workspaceTabs.keys())) closeWorkspaceTab(id);
  for (const dialog of document.querySelectorAll('dialog[open]')) if (dialog !== masterDialog) dialog.close();
  hosts = []; token = ''; currentUser = null; acceptingShareId = 0; sharingHost = '';
  hostForm.reset(); document.querySelector('#cc-pass').value = '';
  for (const id of ['drawer-list','recent-chips','inbox-list','account-users','share-target','cc-name','cc-addr']) document.getElementById(id).replaceChildren();
  window.dispatchEvent(new Event('xsh-logout'));
  appEl.hidden = true; closeDrawer();
  document.querySelector('#master-error').textContent = '';
  if (!status) {
    const response = await fetch('api/auth/status'); status = response.ok ? await response.json() : {};
  }
  if (generation !== authGeneration) return;
  setupRequired = Boolean(status.setup_required);
  document.querySelector('#master-title').textContent = setupRequired ? '创建管理员' : 'xsh';
  document.querySelector('#master-copy').textContent = setupRequired ? '首个账号为管理员，其他账号由管理员创建。' : '登录你的 xsh 账号。';
  document.querySelector('#setup-code-row').hidden = !setupRequired;
  document.querySelector('#setup-code').required = setupRequired;
  document.querySelector('#master-confirm-row').hidden = !setupRequired;
  document.querySelector('#master-confirm').required = setupRequired;
  document.querySelector('#master-password').autocomplete = setupRequired ? 'new-password' : 'current-password';
  document.querySelector('#legacy-import-row').hidden = !setupRequired || !status.legacy_available;
  legacyVaultRequired = setupRequired && Boolean(status.legacy_vault);
  syncLegacyImport();
  document.querySelector('#master-submit').textContent = setupRequired ? '创建并登录' : '登录';
  const username = document.querySelector('#login-username');
  if (!setupRequired && !username.value) { try { username.value = localStorage.getItem(lastLoginStorageKey) || ''; } catch (_) {} }
  if (!masterDialog.open) masterDialog.showModal();
  setTimeout(() => (username.value && !setupRequired ? document.querySelector('#master-password') : username).focus(), 0);
}
function syncLegacyImport() {
  const needed = legacyVaultRequired && document.querySelector('#legacy-import').checked;
  document.querySelector('#legacy-password-row').hidden = !needed;
  document.querySelector('#legacy-password').required = needed;
}
document.querySelector('#legacy-import').addEventListener('change', syncLegacyImport);
document.querySelector('#master-form').onsubmit = async event => {
  event.preventDefault();
  const password = document.querySelector('#master-password'), confirmation = document.querySelector('#master-confirm');
  const error = document.querySelector('#master-error'), submit = document.querySelector('#master-submit');
  if (setupRequired && password.value !== confirmation.value) { error.textContent = '两次输入的登录密码不一致。'; return; }
  const generation = ++authGeneration;
  submit.disabled = true; error.textContent = '';
  try {
    const response = await fetch('api/login', { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({
      username:document.querySelector('#login-username').value, password:password.value, create:setupRequired,
      setup_code:document.querySelector('#setup-code').value, import_legacy:setupRequired && !document.querySelector('#legacy-import-row').hidden && document.querySelector('#legacy-import').checked,
      legacy_password:document.querySelector('#legacy-password').value
    }) });
    if (generation !== authGeneration) return;
    password.value = ''; confirmation.value = ''; document.querySelector('#legacy-password').value = '';
    if (!response.ok) throw new Error((await response.text()).trim());
    const data = await response.json(); if (generation !== authGeneration) return; token = data.token; currentUser = data.user;
    lastSecurityActivity = Date.now();
    try { localStorage.setItem(lastLoginStorageKey, data.user.username); } catch (_) {}
    document.querySelector('#setup-code').value = ''; await loadHosts();
  } catch (failure) { error.textContent = failure.message; }
  finally { submit.disabled = false; }
};
masterDialog.addEventListener('cancel', event => event.preventDefault());

/* ---------------- 主机抽屉 ---------------- */
function openDrawer() {
  renderDrawer();
  document.querySelector('#btn-open-hosts').setAttribute('aria-expanded', 'true');
  backdrop.hidden = false;
  requestAnimationFrame(() => { backdrop.classList.add('open'); drawer.classList.add('open'); drawer.setAttribute('aria-hidden', 'false'); });
  setTimeout(() => { if (drawerOpen() && hostMenu.hidden && !searchInput.value) searchInput.focus(); }, 240);
}
function closeDrawer() {
  closeHostMenu();
  document.querySelector('#btn-open-hosts').setAttribute('aria-expanded', 'false');
  backdrop.classList.remove('open');
  drawer.classList.remove('open');
  drawer.setAttribute('aria-hidden', 'true');
  setTimeout(() => { if (!backdrop.classList.contains('open')) backdrop.hidden = true; }, 220);
}
function updateCount() {
  if (!countChip) return;
  countChip.textContent = hosts.length > 99 ? '99+' : hosts.length;
  countChip.hidden = !hosts.length;
  const button = document.querySelector('#btn-open-hosts');
  button.title = `主机列表 · ${hosts.length} 台 (Alt+O)`;
  button.setAttribute('aria-label', `主机列表，${hosts.length} 台`);
}
function filteredHosts() {
  const q = searchInput.value.trim().toLowerCase();
  return hosts.filter(h => [h.name, h.hostname, h.user, ...(h.tags || [])].join(' ').toLowerCase().includes(q));
}
const PENCIL_SVG = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4L19.5 8.5a2.1 2.1 0 0 0-3-3L5 17v3Z"/><path d="m13.5 6.5 3 3"/></svg>';
function hostRowHTML(h, i) {
  const hostTags = h.tags || [];
  const initial = escapeHtml((h.name || '?').charAt(0).toUpperCase());
  const when = escapeHtml(h.last_login ? timeAgo(h.last_login) : '从未');
  const dot = isSessionActive(h.name) ? '' : ' off';
  const savedPassword = h.save_password ? '<span class="host-saved-password" role="img" aria-label="已保存密码" title="已保存密码"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="5"/><path d="m12 12 9 9m-4-4 3-3m-6 0 3-3"/></svg></span>' : '';
  const tags = hostTags.length ? `<span class="host-tags">${hostTags.map(t => `<span class="tag" title="${escapeHtml(t)}">${escapeHtml(t)}</span>`).join('')}</span>` : '';
  return `<div class="host-item${i === selectedIdx ? ' sel' : ''}" data-idx="${i}" role="button" tabindex="0" aria-label="连接 ${escapeHtml(h.name)}">
    <span class="av" data-host="${escapeHtml(h.name)}">${initial}</span>
    <span class="host-main">
      <span class="host-name-row"><span class="host-name"><span class="dot${dot}"></span> ${escapeHtml(h.name)}</span>${savedPassword}</span>
      <span class="host-addr">${escapeHtml(h.user)}@${escapeHtml(h.hostname)}:${h.port}</span>
      ${tags}
    </span>
    <span class="host-side"><span class="when">${when}</span></span>
    <button class="row-edit" type="button" aria-label="编辑 ${escapeHtml(h.name)}" title="编辑（e）">${PENCIL_SVG}</button>
  </div>`;
}
function renderDrawer() {
  closeHostMenu();
  updateCount();
  const list = filteredHosts();
  if (!hosts.length) { drawerList.innerHTML = '<div class="drawer-empty"><span class="glyph">⌁</span>还没有主机，点「新增」创建第一条连接。</div>'; return; }
  if (!list.length) { drawerList.innerHTML = '<div class="drawer-empty"><span class="glyph">Ø</span>没有匹配的主机。<br>换个关键词试试。</div>'; return; }
  if (selectedIdx >= list.length) selectedIdx = list.length - 1;
  if (selectedIdx < 0) selectedIdx = 0;
  drawerList.innerHTML = list.map((h, i) => hostRowHTML(h, i)).join('');
  drawerList.querySelectorAll('.host-item').forEach(row => {
    const h = list[+row.dataset.idx];
    row.setAttribute('aria-haspopup', 'menu');
    row.addEventListener('click', event => { if (!event.target.closest('.row-edit')) connectToHost(h); });
    row.addEventListener('contextmenu', event => { event.preventDefault(); event.stopPropagation(); openHostMenu(h, event.clientX, event.clientY); });
    row.addEventListener('keydown', event => {
      if (event.key === 'e' || event.key === 'E') { event.preventDefault(); openHostModal(h); }
      else if (event.key === 'Delete') { event.preventDefault(); askDelete(h); }
      else if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) { event.preventDefault(); const rect=row.getBoundingClientRect(); openHostMenu(h, rect.left, rect.bottom); }
    });
    row.querySelector('.row-edit').addEventListener('click', event => { event.stopPropagation(); openHostModal(h); });
  });
}
function connectToHost(host) {
  if (screenLocked || !currentUser) return;
  closeHostMenu();
  if (host.save_password && host.user) { closeDrawer(); openTerminal(host.name, '', null, host.user); }
  else openConnect(host);
}
function closeHostMenu() { hostMenu.hidden = true; contextHostName = ''; }
function openHostMenu(host, x, y) {
  if (screenLocked || !currentUser) return;
  closeTabMenu(); closeTerminalMenu();
  contextHostName = host.name; hostMenu.hidden = false;
  const rect = hostMenu.getBoundingClientRect();
  hostMenu.style.left = `${Math.max(8, Math.min(x, innerWidth - rect.width - 8))}px`;
  hostMenu.style.top = `${Math.max(8, Math.min(y, innerHeight - rect.height - 8))}px`;
  hostMenu.querySelector('button').focus({preventScroll:true});
}
hostMenu.addEventListener('click', event => {
  const action = event.target.closest('[data-host-action]')?.dataset.hostAction;
  const host = hosts.find(item => item.name === contextHostName);
  if (!action || !host || screenLocked) return;
  closeHostMenu();
  if (action === 'connect') connectToHost(host);
  else if (action === 'other-user') openConnect(host, null, '');
  else if (action === 'edit') openHostModal(host);
  else if (action === 'share') openShare(host.name);
  else if (action === 'delete') askDelete(host);
});
hostMenu.addEventListener('keydown', event => {
  if (event.key === 'Escape' || event.key === 'Tab') {
    event.preventDefault(); const name = contextHostName; closeHostMenu();
    const row = [...drawerList.querySelectorAll('.host-item')].find(item => filteredHosts()[+item.dataset.idx]?.name === name);
    row?.focus();
  } else if (['ArrowDown','ArrowUp','Home','End'].includes(event.key)) {
    event.preventDefault(); const buttons = [...hostMenu.querySelectorAll('button')], index = buttons.indexOf(document.activeElement);
    buttons[event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length-1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length]?.focus();
  }
});
document.addEventListener('pointerdown', event => { if (!hostMenu.contains(event.target)) closeHostMenu(); });
drawerList.addEventListener('scroll', closeHostMenu);
window.addEventListener('resize', closeHostMenu);
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
  else if (event.key === 'Enter' || (event.key === ' ' && !typing)) { event.preventDefault(); connectToHost(list[Math.max(0, selectedIdx)]); }
});
searchInput.addEventListener('input', () => { selectedIdx = -1; renderDrawer(); });
document.querySelector('#btn-open-hosts').addEventListener('click', openDrawer);
document.querySelector('#empty-open').addEventListener('click', openDrawer);
document.querySelector('#drawer-close').addEventListener('click', closeDrawer);
backdrop.addEventListener('click', closeDrawer);
/* 「新增」按钮：与 a 快捷键一致 → 关闭抽屉后打开新增主机对话框 */
document.querySelector('#btn-add-host').addEventListener('click', () => { closeDrawer(); setTimeout(() => openHostModal(), 80); });

/* Alt+O 在输入框、终端和弹窗内均可打开主机列表。捕获阶段拦截，避免传入远端终端。 */
document.addEventListener('keydown', event => {
  if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.code !== 'KeyO') return;
  if (appEl.hidden || screenLocked || !currentUser) return;
  event.preventDefault();
  event.stopImmediatePropagation();
  for (const dialog of document.querySelectorAll('dialog[open]')) dialog.close();
  openDrawer();
  searchInput.focus();
}, true);

// Browsers may reserve Ctrl+Tab; Alt+PageUp/PageDown provides an in-page alternative.
 document.addEventListener('keydown', event => {
  const controlTab = event.ctrlKey && !event.altKey && !event.metaKey && event.key === 'Tab';
  const alternate = event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey && ['PageUp','PageDown'].includes(event.key);
  if ((!controlTab && !alternate) || event.isComposing || appEl.hidden || screenLocked || document.querySelector('dialog[open]')) return;
  const ids = [...workspaceTabs.keys()]; if (!ids.length) return;
  event.preventDefault(); event.stopImmediatePropagation();
  const direction = controlTab ? (event.shiftKey ? -1 : 1) : (event.key === 'PageUp' ? -1 : 1);
  const index = ids.indexOf(activeTabId);
  closeTabMenu(); closeTerminalMenu(); closeDrawer();
  const group = workspaceTabs.get(ids[(index + direction + ids.length) % ids.length]);
  activateWorkspaceTab(group.id);
  if (!group.sessionIds.size) group.tab.focus();
}, true);

document.addEventListener('keydown', event => {
  if (appEl.hidden) return;
  if (document.querySelector('dialog[open]')) return;
  if (/INPUT|TEXTAREA|SELECT/.test(event.target.tagName)) return;
  if (event.key === '/') {
    event.preventDefault();
    drawerOpen() ? closeDrawer() : openDrawer();
  }
  else if (event.key === 'a' || event.key === 'A') { event.preventDefault(); closeDrawer(); setTimeout(() => openHostModal(), 200); }
});

/* ---------------- 主机编辑 / 删除 ---------------- */
function authValue() { const rl = hostField('auth_type'); return (rl && rl.value) || 'password'; }
function syncAuthUI() {
  const mode = authValue();
  const password = hostField('password');
  document.querySelector('#row-key').hidden = mode !== 'private_key';
  document.querySelector('#row-passhint').hidden = mode !== 'password';
  document.querySelector('#f-key-display').required = mode === 'private_key' && hostForm.dataset.hasPrivateKey !== 'true';
  hostField('private_key').disabled = mode !== 'private_key';
  password.disabled = mode !== 'password';
  if (mode !== 'password') {
    password.value = '';
  }
}
for (const radio of document.querySelectorAll('input[name="auth_type"]')) radio.addEventListener('change', syncAuthUI);

const testPasswordButton = document.querySelector('#btn-test-password'), hostTestResult = document.querySelector('#host-test-result');
let hostTestController = null;
function resetHostTest() {
  hostTestController?.abort(); hostTestController = null;
  testPasswordButton.disabled = false; testPasswordButton.removeAttribute('aria-busy');
  hostTestResult.hidden = true; hostTestResult.textContent = ''; hostTestResult.className = 'hint';
}
hostForm.addEventListener('input', resetHostTest);
hostForm.addEventListener('change', resetHostTest);
hostDialog.addEventListener('close', resetHostTest);
testPasswordButton.addEventListener('click', async () => {
  resetHostTest();
  const controller = new AbortController(), generation = authGeneration;
  hostTestController = controller; testPasswordButton.disabled = true; testPasswordButton.setAttribute('aria-busy', 'true');
  hostTestResult.hidden = false; hostTestResult.textContent = '正在验证密码…';
  const payload = {original:editingOriginal, host:{hostname:hostField('hostname').value.trim(), port:Number(hostField('port').value), user:hostField('user').value.trim(), proxy_jump:hostField('proxy_jump').value}, password:hostField('password').value, trust:{}};
  try {
    for (;;) {
      const response = await fetch('api/hosts/test', {method:'POST', headers:{'Content-Type':'application/json','X-XSH-Token':token}, body:JSON.stringify(payload), signal:controller.signal});
      if (controller.signal.aborted || generation !== authGeneration) return;
      if (response.status === 401) { await lockView(); return; }
      if (response.status === 423) { showScreenLock(); return; }
      if (response.status === 409) {
        const trust = await response.json();
        if (controller.signal.aborted || generation !== authGeneration) return;
        if (payload.trust[trust.address] || !window.confirm(`首次连接 ${trust.address}\n服务器指纹：${trust.fingerprint}\n是否信任此指纹并继续测试？`)) throw new Error('已取消验证');
        payload.trust[trust.address] = trust.fingerprint; continue;
      }
      if (!response.ok) throw new Error((await response.text()).trim());
      hostTestResult.textContent = '密码验证通过'; hostTestResult.className = 'hint success'; break;
    }
  } catch (error) {
    if (!controller.signal.aborted && generation === authGeneration) { hostTestResult.textContent = error.message === 'Failed to fetch' ? '连接中断，请重试' : error.message; hostTestResult.className = 'hint error'; }
  } finally {
    if (hostTestController === controller) { hostTestController = null; testPasswordButton.disabled = false; testPasswordButton.removeAttribute('aria-busy'); }
  }
});

const jumpPicker = document.querySelector('#jump-picker'), jumpToggle = document.querySelector('#f-jump-toggle'), jumpPanel = document.querySelector('#jump-panel'), jumpSearch = document.querySelector('#jump-search'), jumpOptions = document.querySelector('#jump-options');
let jumpCandidates = [];
function setJumpOpen(open) {
  jumpPanel.hidden = !open; jumpToggle.setAttribute('aria-expanded', String(open));
  if (open) {
    jumpSearch.value = ''; renderJumpOptions();
    const bounds = hostDialog.querySelector('.dlg').getBoundingClientRect(), anchor = jumpToggle.getBoundingClientRect();
    const above = anchor.top - bounds.top - 12, below = bounds.bottom - anchor.bottom - 12, opensUp = below < 250 && above > below;
    jumpPicker.classList.toggle('opens-up', opensUp);
    jumpOptions.style.maxHeight = `${Math.max(60, Math.min(176, (opensUp ? above : below) - 68))}px`;
    jumpSearch.focus({preventScroll:true});
  }
}
function renderJumpOptions() {
  const query = jumpSearch.value.trim().toLowerCase();
  jumpOptions.replaceChildren();
  const choices = [{name:'', label:'不使用跳板'}, ...jumpCandidates.map(host => ({name:host.name, label:host.name, address:host.hostname}))];
  for (const choice of choices.filter(item => !item.name || `${item.name} ${item.address || ''}`.toLowerCase().includes(query))) {
    const button = document.createElement('button'), text = document.createElement('span'), name = document.createElement('span'), check = document.createElement('span');
    button.type = 'button'; button.className = 'jump-option'; button.setAttribute('role', 'option'); button.setAttribute('aria-selected', String(hostField('proxy_jump').value === choice.name));
    text.className = 'jump-option-text'; name.textContent = choice.label; text.append(name);
    if (choice.address && choice.address !== choice.name) { const address = document.createElement('small'); address.textContent = choice.address; text.append(address); }
    check.className = 'jump-option-check'; check.textContent = '✓'; check.setAttribute('aria-hidden', 'true'); button.append(text, check);
    button.addEventListener('click', () => { hostField('proxy_jump').value = choice.name; document.querySelector('#jump-value').textContent = choice.label; hostField('proxy_jump').dispatchEvent(new Event('change', {bubbles:true})); setJumpOpen(false); jumpToggle.focus(); });
    jumpOptions.append(button);
  }
  if (query && jumpOptions.children.length === 1) { const empty = document.createElement('p'); empty.className = 'share-picker-empty'; empty.textContent = '没有匹配的主机'; jumpOptions.append(empty); }
}
jumpToggle.addEventListener('click', () => setJumpOpen(jumpPanel.hidden));
jumpSearch.addEventListener('input', renderJumpOptions);
jumpPicker.addEventListener('keydown', event => {
  if (event.key === 'Escape' && !jumpPanel.hidden) { event.preventDefault(); event.stopPropagation(); setJumpOpen(false); jumpToggle.focus(); }
  else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault();
    if (jumpPanel.hidden) setJumpOpen(true);
    const choices = [...jumpOptions.querySelectorAll('[role="option"]')], index = choices.indexOf(document.activeElement), offset = event.key === 'ArrowDown' ? 1 : -1;
    choices[index < 0 ? (offset > 0 ? 0 : choices.length - 1) : (index + offset + choices.length) % choices.length]?.focus();
  } else if (event.key === 'Enter' && event.target === jumpSearch) event.preventDefault();
});
document.addEventListener('click', event => { if (!jumpPicker.contains(event.target)) setJumpOpen(false); });
jumpPicker.addEventListener('focusout', event => { if (!jumpPicker.contains(event.relatedTarget)) setJumpOpen(false); });
hostDialog.addEventListener('close', () => setJumpOpen(false));
hostDialog.querySelector('.dlg').addEventListener('scroll', () => setJumpOpen(false));
window.addEventListener('resize', () => setJumpOpen(false));

function bindPrivateKeyFile(inputId, field, dialog, messageId) {
  const input = document.getElementById(inputId);
  const display = document.getElementById(`${field.id}-display`);
  const editor = field.closest('.private-key-editor'), source = editor.querySelector('.private-key-source'), eye = editor.querySelector('[data-key-view]');
  const clearButton = editor.querySelector('[data-key-clear]');
  let fileName = '';
  const syncDisplay = () => {
    const hasKey = Boolean(field.value.trim());
    display.value = hasKey ? fileName || '已粘贴私钥' : '';
    display.title = hasKey ? fileName || '已粘贴私钥' : '';
    display.disabled = field.disabled; display.placeholder = field.placeholder;
    source.hidden = !hasKey; eye.hidden = !hasKey; clearButton.hidden = !hasKey;
    source.querySelector('[data-source-file]').toggleAttribute('hidden', !fileName);
    source.querySelector('[data-source-paste]').toggleAttribute('hidden', Boolean(fileName));
    editor.classList.toggle('has-key', hasKey);
  };
  new MutationObserver(syncDisplay).observe(field, {attributes:true, attributeFilter:['disabled', 'placeholder']});
  let revision = 0;
  const reset = () => { revision++; input.value = ''; delete field.dataset.fileLoading; };
  field.form.addEventListener('reset', reset);
  const clear = () => { fileName = ''; field.value = ''; syncDisplay(); };
  field.form.addEventListener('reset', clear);
  dialog.addEventListener('close', () => { reset(); clear(); if (keyViewDialog.open) keyViewDialog.close(); });
  field.addEventListener('input', () => { reset(); fileName = ''; syncDisplay(); });
  clearButton.addEventListener('click', () => {
    if (field.disabled) return;
    field.value = ''; field.dispatchEvent(new Event('input', {bubbles:true}));
    document.getElementById(messageId).textContent = '';
    display.focus();
  });
  display.addEventListener('keydown', event => {
    if (event.key === 'Backspace' || event.key === 'Delete') { event.preventDefault(); field.value = ''; field.dispatchEvent(new Event('input', {bubbles:true})); }
  });
  display.addEventListener('paste', event => {
    const text = event.clipboardData?.getData('text');
    if (text === undefined) return;
    event.preventDefault(); field.value = text; field.dispatchEvent(new Event('input', {bubbles:true}));
  });
  field.closest('.private-key-editor').querySelector('[data-key-paste]').addEventListener('click', async () => {
    if (field.disabled || !dialog.open) return;
    reset();
    const current = revision, generation = authGeneration, message = document.getElementById(messageId);
    const active = () => current === revision && generation === authGeneration && dialog.open && !field.disabled;
    message.textContent = '';
    display.focus();
    try {
      if (!navigator.clipboard?.readText) throw new Error('当前浏览器无法读取剪贴板，请在私钥框按 Ctrl+V 粘贴。');
      const text = await navigator.clipboard.readText();
      if (!active()) return;
      if (new Blob([text]).size > 64 * 1024) throw new Error('私钥内容不能超过 64 KB。');
      if (!text.trim()) throw new Error('剪贴板为空，请先复制私钥内容。');
      field.value = text;
      field.dispatchEvent(new Event('input', {bubbles:true}));
    } catch (error) {
      if (active()) message.textContent = error.name === 'NotAllowedError' || error.name === 'SecurityError'
        ? '浏览器不允许读取剪贴板，请在私钥框按 Ctrl+V 粘贴。'
        : error.message || '无法读取剪贴板，请在私钥框按 Ctrl+V 粘贴。';
    }
  });
  input.addEventListener('change', async () => {
    const file = input.files[0]; if (!file) return;
    const current = ++revision, generation = authGeneration, message = document.getElementById(messageId);
    message.textContent = ''; delete field.dataset.fileLoading;
    if (file.size > 64 * 1024) { message.textContent = '私钥文件不能超过 64 KB。'; input.value = ''; return; }
    field.dataset.fileLoading = 'true';
    try {
      const text = await file.text();
      if (current !== revision || generation !== authGeneration || !dialog.open || field.disabled) return;
      if (!/^-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----/m.test(text)) throw new Error('未识别到私钥内容，请选择私钥文件，不是公钥文件。');
      field.value = text; field.dispatchEvent(new Event('input', {bubbles:true})); fileName = file.name; syncDisplay();
    } catch (error) {
      if (current === revision && dialog.open && generation === authGeneration) message.textContent = error.message || '无法读取私钥文件。';
    } finally {
      if (current === revision) { delete field.dataset.fileLoading; input.value = ''; }
    }
  });
}
const keyViewDialog = document.querySelector('#dlg-key-view');
const keyViewContent = document.querySelector('#key-view-content');
keyViewDialog.addEventListener('close', () => { keyViewContent.textContent = ''; });
for (const button of document.querySelectorAll('[data-key-view]')) button.addEventListener('click', () => {
  const field = document.getElementById(button.dataset.keyView);
  keyViewContent.textContent = field.value || '未输入私钥内容';
  keyViewDialog.showModal();
});
bindPrivateKeyFile('f-key-file', hostField('private_key'), hostDialog, 'host-msg');
bindPrivateKeyFile('cc-key-file', document.querySelector('#cc-private-key'), connectDialog, 'connect-msg');
for (const button of document.querySelectorAll('[data-key-file]')) button.addEventListener('click', () => document.getElementById(button.dataset.keyFile).click());

function openHostModal(host, shareId = 0) {
  closeHostMenu();
  resetHostTest();
  const editing = Boolean(host && !shareId);
  acceptingShareId = shareId;
  editingOriginal = editing ? host.name : '';
  hostForm.reset();
  hostField('password').type = 'password';
  hostForm.dataset.hasPrivateKey = String(Boolean(editing && host?.has_private_key));
  hostField('private_key').placeholder = editing && host?.has_private_key ? '留空保留当前私钥' : '粘贴私钥内容';
  const jump = hostField('proxy_jump'); jump.value = host?.proxy_jump || '';
  jumpCandidates = hosts.filter(candidate => candidate.name !== editingOriginal);
  if (jump.value && !jumpCandidates.some(candidate => candidate.name === jump.value)) jumpCandidates = [...jumpCandidates, {name:jump.value}];
  document.querySelector('#jump-value').textContent = jump.value || '不使用跳板'; setJumpOpen(false);
  if (host) {
    for (const key of ['name', 'hostname', 'user', 'port']) hostField(key).value = host[key] ?? '';
    hostField('name').readOnly = false;
    hostField('tags').value = (host.tags || []).join(', ');
    hostField('auth_type').value = host.auth_type === 'private_key' ? 'private_key' : 'password';
    hostField('password').placeholder = host.save_password ? '留空则保留当前已保存密码' : '留空则在连接时输入';
  } else {
    hostField('name').readOnly = false;
    hostField('password').placeholder = '留空则在连接时输入';
  }
  syncAuthUI();
  if (host?.save_password) hostField('password').placeholder = '留空则保留当前已保存密码';
  document.querySelector('#host-title').textContent = editing ? `编辑 ${host.name}` : shareId ? '添加分享的主机' : '新增主机';
  document.querySelector('#btn-del-host').hidden = !editing;
  document.querySelector('#btn-share-host').hidden = !editing;
  const msg = document.querySelector('#host-msg'); msg.textContent = ''; msg.className = 'form-msg';
  hostDialog.showModal();
  setTimeout(() => (shareId ? hostField('user') : editing ? hostField('hostname') : hostField('name')).focus(), 0);
}
hostForm.onsubmit = async event => {
  event.preventDefault();
  if (document.querySelector('#f-key-display').required && !hostField('private_key').value.trim()) { document.querySelector('#host-msg').textContent = '请选择私钥文件或粘贴私钥内容。'; return; }
  if (hostField('private_key').dataset.fileLoading) { document.querySelector('#host-msg').textContent = '正在读取私钥文件，请稍候再保存。'; return; }
  const host = Object.fromEntries(new FormData(hostForm));
  host.port = Number(host.port);
  host.tags = host.tags.split(/[,，]/).map(value => value.trim()).filter(Boolean);
  const password = host.password || '';
  const privateKey = host.private_key || '';
  delete host.password; delete host.private_key;
  await mutateHost(editingOriginal ? 'PUT' : 'POST', host, password, privateKey);
};
async function mutateHost(method, host, password = '', privateKey = '') {
  const original = editingOriginal;
  const response = await fetch('api/hosts', {
    method,
    headers: { 'Content-Type': 'application/json', 'X-XSH-Token': token },
    body: JSON.stringify({ original: editingOriginal, host, password, private_key: privateKey, share_id: acceptingShareId })
  });
  if (response.status === 401) { lockView(); return; }
  if (!response.ok) {
    const msg = document.querySelector(method === 'DELETE' ? '#confirm-msg' : '#host-msg');
    msg.textContent = await response.text(); msg.className = 'form-msg error';
    return;
  }
  if (method === 'PUT' && original !== host.name.trim()) {
    const name = host.name.trim();
    for (const session of sessions.values()) if (session.host === original) session.host = name;
    for (const group of workspaceTabs.values()) updateTabLabel(group);
    if (connectHost === original) connectHost = name;
  }
  if (hostDialog.open) hostDialog.close();
  if (confirmDialog.open) confirmDialog.close();
  await loadHosts();
}
document.querySelector('#btn-del-host').addEventListener('click', () => { hostDialog.close(); askDelete(); });
function askDelete(host = null) {
  if (host) { editingOriginal = host.name; acceptingShareId = 0; }
  closeDrawer();
  document.querySelector('#confirm-target').textContent = editingOriginal;
  document.querySelector('#confirm-msg').textContent = '';
  confirmDialog.showModal();
}
document.querySelector('#confirm-ok').addEventListener('click', async event => {
  const button = event.currentTarget; button.disabled = true;
  try { await mutateHost('DELETE', {}); }
  catch (error) { document.querySelector('#confirm-msg').textContent = error.message; }
  finally { button.disabled = false; }
});

/* ---------------- 连接 ---------------- */
function openConnect(host, placement = null, username = host.user, authType = host.auth_type) {
  connectPlacement = placement;
  connectHost = host.name;
  closeDrawer();
  document.querySelector('#connect-form').reset();
  document.querySelector(authType === 'private_key' ? '#cc-auth-key' : '#cc-auth-password').checked = true;
  document.querySelector('#cc-av').textContent = (host.name || '?').charAt(0).toUpperCase();
  document.querySelector('#cc-name').textContent = host.name;
  document.querySelector('#cc-user').value = username;
  const pass = document.querySelector('#cc-pass'); pass.value = '';
  syncConnectUser();
  const msg = document.querySelector('#connect-msg'); msg.textContent = ''; msg.className = 'form-msg';
  connectDialog.showModal();
  setTimeout(() => (username ? document.querySelector('#cc-auth-key').checked ? document.querySelector('#cc-private-key-display') : pass : document.querySelector('#cc-user')).focus(), 0);
}
function syncConnectUser() {
  const host=hosts.find(item=>item.name===connectHost);if(!host)return;
  const username=document.querySelector('#cc-user').value.trim(), changed=username!==host.user;
  const retry = Boolean(connectPlacement?.retrySessionId);
  const keyMode = document.querySelector('#cc-auth-key').checked;
  const credential = keyMode ? '私钥' : '密码';
  const passwordStatus = document.querySelector('#cc-password-status');
  const hasSavedPassword = !retry && !changed && (keyMode ? host.auth_type === 'private_key' && host.has_private_key : Boolean(host.save_password));
  passwordStatus.hidden = false;
  passwordStatus.classList.toggle('is-saved', hasSavedPassword);
  passwordStatus.title = retry ? `本次重试需重新提供${credential}` : hasSavedPassword ? `已保存${credential}，留空自动使用` : `未保存${credential}`;
  passwordStatus.setAttribute('aria-label', passwordStatus.title);
  document.querySelector('#cc-password-unsaved').toggleAttribute('hidden', hasSavedPassword);
  document.querySelector('#cc-addr').textContent = `${username || '…'}@${host.hostname}:${host.port}`;
  document.querySelector('#cc-auth').textContent = authLabel(keyMode ? 'private_key' : 'password');
  document.querySelector('#cc-password-row').hidden = keyMode; document.querySelector('#cc-key-row').hidden = !keyMode;
  const pass=document.querySelector('#cc-pass'); pass.disabled=keyMode; pass.required=!keyMode&&!hasSavedPassword; pass.placeholder=hasSavedPassword?'留空使用已保存密码':changed?'输入该用户的密码':'输入密码';
  const key=document.querySelector('#cc-private-key'); key.disabled=!keyMode; key.placeholder=hasSavedPassword?'留空使用已保存私钥':'选择文件或粘贴私钥';
  const keyDisplay=document.querySelector('#cc-private-key-display'); keyDisplay.disabled=!keyMode; keyDisplay.required=keyMode&&!hasSavedPassword;
}

function retryTerminalCredentials(session, authenticationFailed = false) {
  const host = hosts.find(item => item.name === session.host);
  if (!host || screenLocked || document.querySelector('dialog[open]') || !sessions.has(session.id)) return;
  activateSession(session.id);
  openConnect(host, {retrySessionId:session.id}, session.user, session.authType);
  document.querySelector('#connect-msg').textContent = authenticationFailed ? 'SSH 凭证验证失败，请重新提供密码或私钥。本次重试不会修改已保存的凭据。' : '请提供本次连接的密码或私钥，将在原标签页重新连接。';
}
function reconnectTerminal(session) {
  if (!session || !['error','closed'].includes(session.state) || screenLocked) return;
  const host = hosts.find(item => item.name === session.host); if (!host) return;
  if (!session.authenticationFailed && canReuseSavedAuth(session, host)) connectTerminal(session, '', {authType:session.authType});
  else retryTerminalCredentials(session, session.authenticationFailed);
}
function canReuseSavedAuth(session, host) {
  return session.user === host.user && (session.authType === 'private_key' ? host.auth_type === 'private_key' && host.has_private_key : host.save_password);
}
document.querySelector('#cc-user').addEventListener('input',()=>{document.querySelector('#cc-pass').value='';syncConnectUser();});
for (const radio of document.querySelectorAll('input[name="connect_auth_type"]')) radio.addEventListener('change', () => {
  document.querySelector('#cc-pass').value = '';
  const key = document.querySelector('#cc-private-key'); key.value = ''; key.dispatchEvent(new Event('input', {bubbles:true}));
  syncConnectUser();
});
document.querySelector('#connect-form').onsubmit = event => {
  event.preventDefault();
  const key = document.querySelector('#cc-private-key');
  if (document.querySelector('#cc-private-key-display').required && !key.value.trim()) { document.querySelector('#connect-msg').textContent = '请选择私钥文件或粘贴私钥内容。'; return; }
  if (key.dataset.fileLoading) { document.querySelector('#connect-msg').textContent = '正在读取私钥文件，请稍候再连接。'; return; }
  const pass = document.querySelector('#cc-pass');
  const keyMode = document.querySelector('#cc-auth-key').checked;
  const password = keyMode ? '' : pass.value; pass.value = '';
  const credentials = {authType:keyMode?'private_key':'password', privateKey:keyMode?key.value:''}; key.value = '';
  const username=document.querySelector('#cc-user').value.trim();
  const placement = connectPlacement; connectPlacement = null;
  connectDialog.close();
  if (placement?.retrySessionId) {
    const session = sessions.get(placement.retrySessionId);
    if (session && ['error','closed'].includes(session.state)) { session.user = username; connectTerminal(session, password, credentials); }
  } else openTerminal(connectHost, password, placement, username, credentials);
};

/* ---------------- 终端会话 ---------------- */
let draggedTabId = '';
function finishTabDrag() {
  workspaceTabs.get(draggedTabId)?.tab.classList.remove('dragging');
  draggedTabId = '';
}
tabstrip.addEventListener('dragover', event => {
  const group = workspaceTabs.get(draggedTabId);
  if (!group || screenLocked || appEl.hidden) return;
  event.preventDefault(); event.dataTransfer.dropEffect = 'move';
  const next = [...tabstrip.children].find(tab => tab !== group.tab && event.clientX < tab.getBoundingClientRect().left + tab.offsetWidth / 2) || null;
  if (group.tab.nextElementSibling !== next) {
    tabstrip.insertBefore(group.tab, next);
    const ordered = [...tabstrip.children].map(tab => workspaceTabs.get(tab.id));
    workspaceTabs.clear();
    for (const item of ordered) workspaceTabs.set(item.id, item);
  }
  const bounds = tabstrip.getBoundingClientRect();
  if (event.clientX < bounds.left + 24) tabstrip.scrollLeft -= 16;
  else if (event.clientX > bounds.right - 24) tabstrip.scrollLeft += 16;
});
tabstrip.addEventListener('drop', event => { if (draggedTabId) { event.preventDefault(); finishTabDrag(); } });
function updateWorkspace() { emptyState.hidden = Boolean(workspaceTabs.get(activeTabId)?.sessionIds.size); }
function createWorkspaceTab(label = '主页') {
  const id = `tab-${nextTabId++}`, tab = document.createElement('div');
  tab.className = 'tab'; tab.setAttribute('role', 'tab'); tab.tabIndex = -1; tab.id = id;tab.setAttribute('aria-controls','empty-state');
  tab.innerHTML = `<span class="tab-state"></span><span class="tab-label">${escapeHtml(label)}</span><button class="tab-close" type="button" title="关闭 ${escapeHtml(label)}" aria-label="关闭 ${escapeHtml(label)}">✕</button>`;
  const group = {id, tab, root:null, sessionIds:new Set(), focused:'', customLabel:'', defaultLabel:label}; workspaceTabs.set(id, group); tabstrip.append(tab);
  tab.draggable = true;
  tab.addEventListener('pointerdown', event => { tab.draggable = event.button === 0 && !event.target.closest('.tab-close'); });
  tab.addEventListener('dragstart', event => {
    if (screenLocked || !currentUser || !tab.draggable) { event.preventDefault(); return; }
    closeTabMenu(); closeTerminalMenu(); closeHostMenu();
    draggedTabId = id; tab.classList.add('dragging');
    event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('application/x-xsh-tab', id);
    activateWorkspaceTab(id);
  });
  tab.addEventListener('dragend', finishTabDrag);
  tab.onclick = event => { if (!event.target.closest('.tab-close')) activateWorkspaceTab(id); };
  tab.onauxclick = event => { if (event.button === 1) {event.preventDefault();closeWorkspaceTab(id);} };
  tab.onkeydown = event => {
    if (event.target !== tab) return;
    if (event.key === 'Enter' || event.key === ' ') { event.preventDefault();activateWorkspaceTab(id); }
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) { event.preventDefault();const rect=tab.getBoundingClientRect();openTabMenu(id,rect.left,rect.bottom); }
    if (['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) {event.preventDefault();const ids=[...workspaceTabs.keys()],index=ids.indexOf(id);activateWorkspaceTab(ids[event.key==='Home'?0:event.key==='End'?ids.length-1:(index+(event.key==='ArrowRight'?1:-1)+ids.length)%ids.length]);workspaceTabs.get(activeTabId)?.tab.focus();}
  };
  tab.oncontextmenu = event => {event.preventDefault();openTabMenu(id,event.clientX,event.clientY);};
  tab.querySelector('.tab-close').onclick = event => {event.stopPropagation();closeWorkspaceTab(id);};
  return group;
}
function updateTabLabel(group) {
  const label = group.customLabel || sessions.get(group.focused)?.host || group.defaultLabel;
  group.tab.querySelector('.tab-label').textContent = label;
  group.tab.title = label;
  const close = group.tab.querySelector('.tab-close'); close.title = `关闭 ${label}`; close.setAttribute('aria-label', `关闭 ${label}`);
}
function activateWorkspaceTab(id) {
  const group = workspaceTabs.get(id); if (!group) return;
  activeTabId = id; activeSessionId = group.focused;
  const focused=sessions.get(group.focused);if(focused) group.tab.dataset.st=focused.state;
  updateTabLabel(group);
  for (const item of workspaceTabs.values()) {
    const active = item.id === id; item.tab.setAttribute('aria-selected', String(active)); item.tab.tabIndex = active ? 0 : -1;
    if (item.root) item.root.hidden = !active;
  }
  updateWorkspace(); updateUploadButton();
  requestAnimationFrame(() => { for (const sessionId of group.sessionIds) { const session = sessions.get(sessionId);try {session.fitAddon.fit();if(sessionId===group.focused) session.terminal.focus();}catch(_){} } });
  group.tab.scrollIntoView({block:'nearest',inline:'nearest'});
}
function newHomeTab() { closeTabMenu();const group=createWorkspaceTab();activateWorkspaceTab(group.id);return group; }
function closeWorkspaceTab(id) {
  const group=workspaceTabs.get(id);if(!group)return;
  for (const sessionId of [...group.sessionIds]) closeSession(sessionId);
  group.root?.remove();group.tab.remove();workspaceTabs.delete(id);
  if(activeTabId===id) {activeTabId='';activeSessionId='';const next=[...workspaceTabs.keys()].at(-1);if(next)activateWorkspaceTab(next);}
  updateWorkspace();updateUploadButton();closeTabMenu();
}
function setSessionState(session, state, label) {
  session.state = state;
  const group = workspaceTabs.get(session.tabId);
  if(group?.focused===session.id) {session.tab.dataset.st = state;session.tab.title = `${group.customLabel || session.host} · ${label}`;}
  updateUploadButton();
  if (screenLocked) updateScreenLockInfo();
}
function activateSession(id) {
  const session = sessions.get(id); if (!session) return;
  const group = workspaceTabs.get(session.tabId); group.focused = id;
  activateWorkspaceTab(group.id);
  for (const item of sessions.values()) {
    const active = item.id === id;
    item.pane.classList.toggle('focused', active);
  }
}

const tabMenu=document.querySelector('#tab-context-menu');let contextTabId='';
const tabRenameDialog = document.querySelector('#dlg-tab-rename'), tabRenameName = document.querySelector('#tab-rename-name');
let renamingTabId = '';
tabRenameDialog.addEventListener('close', () => { renamingTabId = ''; tabRenameName.value = ''; });
document.querySelector('#tab-rename-form').onsubmit = event => {
  event.preventDefault();
  const group = workspaceTabs.get(renamingTabId), name = tabRenameName.value.trim();
  if (!group || !name || screenLocked) return;
  group.customLabel = name; updateTabLabel(group); tabRenameDialog.close();
};
function closeTabMenu() {tabMenu.hidden=true;contextTabId='';}
function openTabMenu(id,x,y) {
  closeHostMenu();
  closeTerminalMenu();
  const group=workspaceTabs.get(id);if(!group||screenLocked)return;
  contextTabId=id;tabMenu.hidden=false;
  for(const button of tabMenu.querySelectorAll('[data-tab-action]')) button.disabled = ['vertical','horizontal'].includes(button.dataset.tabAction) && !group.sessionIds.size;
  tabMenu.querySelector('[data-tab-action="reconnect"]').disabled = !['error','closed'].includes(sessions.get(group.focused)?.state);
  const rect=tabMenu.getBoundingClientRect();tabMenu.style.left=`${Math.max(8,Math.min(x,innerWidth-rect.width-8))}px`;tabMenu.style.top=`${Math.max(8,Math.min(y,innerHeight-rect.height-8))}px`;
  tabMenu.querySelector('button').focus();
}
document.querySelector('#btn-new-tab').addEventListener('click',newHomeTab);
tabMenu.addEventListener('click',event=>{
  const button=event.target.closest('[data-tab-action]'), action=button?.dataset.tabAction,group=workspaceTabs.get(contextTabId);if(!action||!group||button.disabled||screenLocked)return;
  const id=group.id;closeTabMenu();
  if(action==='new')newHomeTab();
  else if(action==='rename') {
    renamingTabId = id; tabRenameName.value = group.tab.querySelector('.tab-label').textContent;
    tabRenameDialog.showModal(); tabRenameName.focus(); tabRenameName.select();
  }
  else if(action==='reconnect')reconnectTerminal(sessions.get(group.focused));
  else if(action==='close')closeWorkspaceTab(id);
  else if(action==='close-others') {for(const other of [...workspaceTabs.keys()])if(other!==id)closeWorkspaceTab(other);activateWorkspaceTab(id);}
  else if(action==='close-all') {for(const other of [...workspaceTabs.keys()])closeWorkspaceTab(other);}
  else {
    const session=sessions.get(group.focused);if(!session) {if(action==='duplicate')newHomeTab();return;}
    const host=hosts.find(item=>item.name===session.host);if(!host)return;
    const placement=action==='duplicate'?{newTab:true}:{sessionId:session.id,direction:action};
    if(canReuseSavedAuth(session,host))openTerminal(host.name,'',placement,session.user,{authType:session.authType});else openConnect(host,placement,session.user,session.authType);
  }
});
tabMenu.addEventListener('keydown',event=>{
  if(event.key==='Escape'||event.key==='Tab') {const id=contextTabId;event.preventDefault();closeTabMenu();workspaceTabs.get(id)?.tab.focus();}
  else if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)) {
    event.preventDefault();const buttons=[...tabMenu.querySelectorAll('button:not(:disabled)')],index=buttons.indexOf(document.activeElement);
    buttons[event.key==='Home'?0:event.key==='End'?buttons.length-1:(index+(event.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length]?.focus();
  }
});
document.addEventListener('pointerdown',event=>{if(!tabMenu.contains(event.target))closeTabMenu();});
window.addEventListener('resize',closeTabMenu);

const terminalMenu=document.querySelector('#terminal-context-menu'), terminalMenuMessage=document.querySelector('#terminal-menu-message');
let contextTerminalId='', contextTerminalSelection='';
function closeTerminalMenu() {terminalMenu.hidden=true;contextTerminalId='';contextTerminalSelection='';}
function openTerminalMenu(session,event) {
  if(screenLocked||!sessions.has(session.id))return;
  event.preventDefault();event.stopPropagation();closeTabMenu();closeHostMenu();
  const selection=session.terminal.getSelection();activateSession(session.id);
  contextTerminalId=session.id;contextTerminalSelection=selection;terminalMenuMessage.hidden=true;terminalMenu.hidden=false;
  for(const button of terminalMenu.querySelectorAll('[data-terminal-action]')) {
    const action=button.dataset.terminalAction;
    button.disabled=action==='copy'?!selection:action==='paste'?session.state!=='connected':false;
  }
  const bounds=terminalMenu.getBoundingClientRect();terminalMenu.style.left=`${Math.max(8,Math.min(event.clientX,innerWidth-bounds.width-8))}px`;terminalMenu.style.top=`${Math.max(8,Math.min(event.clientY,innerHeight-bounds.height-8))}px`;
  requestAnimationFrame(()=>{if(!terminalMenu.hidden)terminalMenu.querySelector('button:not(:disabled)')?.focus({preventScroll:true});});
}
terminalMenu.addEventListener('click',async event=>{
  const button=event.target.closest('[data-terminal-action]'), session=sessions.get(contextTerminalId);if(!button||button.disabled||!session||screenLocked)return;
  const action=button.dataset.terminalAction, selection=contextTerminalSelection;
  try {
    if(action==='copy') {
      if(navigator.clipboard?.writeText&&window.isSecureContext)await navigator.clipboard.writeText(selection);
      else {const field=document.createElement('textarea');field.className='clipboard-helper';field.value=selection;document.body.append(field);field.select();try {if(!document.execCommand('copy'))throw new Error('请使用 Ctrl+C 复制选中文字');}finally {field.remove();}}
    } else if(action==='paste') {
      if(!navigator.clipboard?.readText||!window.isSecureContext)throw new Error('请使用 Ctrl+V 粘贴');
      let text;try {text=await navigator.clipboard.readText();}catch(_){throw new Error('无法读取剪贴板，请使用 Ctrl+V 粘贴');}
      if(screenLocked||sessions.get(session.id)!==session||session.state!=='connected')return;
      session.terminal.paste(text);
    } else if(action==='select-all')session.terminal.selectAll();
    else if(action==='clear')session.terminal.clear();
    else if(action==='close')closeSession(session.id);
    else {const host=hosts.find(item=>item.name===session.host);if(!host)return;const placement={sessionId:session.id,direction:action};if(canReuseSavedAuth(session,host))openTerminal(host.name,'',placement,session.user,{authType:session.authType});else openConnect(host,placement,session.user,session.authType);}
    closeTerminalMenu();if(sessions.has(session.id)&&activeSessionId===session.id)session.terminal.focus();
  }catch(error){if(contextTerminalId===session.id&&!screenLocked){terminalMenuMessage.textContent=error.message;terminalMenuMessage.hidden=false;const bounds=terminalMenu.getBoundingClientRect();terminalMenu.style.top=`${Math.max(8,Math.min(bounds.top,innerHeight-bounds.height-8))}px`;}}
});
terminalMenu.addEventListener('keydown',event=>{
  if(event.key==='Escape'||event.key==='Tab') {event.preventDefault();const session=sessions.get(contextTerminalId);closeTerminalMenu();session?.terminal.focus();}
  else if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)) {event.preventDefault();const buttons=[...terminalMenu.querySelectorAll('button:not(:disabled)')],index=buttons.indexOf(document.activeElement);buttons[event.key==='Home'?0:event.key==='End'?buttons.length-1:(index+(event.key==='ArrowDown'?1:-1)+buttons.length)%buttons.length]?.focus();}
});
document.addEventListener('pointerdown',event=>{if(!terminalMenu.contains(event.target))closeTerminalMenu();});
window.addEventListener('resize',closeTerminalMenu);
function openTerminal(host, password, placement = null, username = hosts.find(item=>item.name===host)?.user || '', credentials = {}) {
  const id = `terminal-${nextSessionId++}`;
  const source = placement?.sessionId && sessions.get(placement.sessionId);
  let group = source ? workspaceTabs.get(source.tabId) : !placement && !workspaceTabs.get(activeTabId)?.sessionIds.size ? workspaceTabs.get(activeTabId) : null;
  if(!group) group=createWorkspaceTab(host);
  const tab=group.tab; tab.dataset.st='connecting';updateTabLabel(group);
  const pane = document.createElement('div');
  pane.className = 'pane'; pane.id = id; pane.setAttribute('role', 'tabpanel');
  pane.setAttribute('aria-label',host);pane.setAttribute('aria-labelledby',group.id);
  if (source) {
    const split=document.createElement('div');split.className=`terminal-split ${placement.direction}`;
    source.pane.replaceWith(split);split.append(source.pane,pane);
  } else {group.root=document.createElement('div');group.root.className='terminal-layout';group.root.id=`layout-${group.id}`;group.root.append(pane);paneArea.append(group.root);tab.setAttribute('aria-controls',group.root.id);}
  const terminal = new Terminal({
    cursorBlink: true, convertEol: true,
    fontFamily: 'Cascadia Mono,Cascadia Code,Consolas,monospace',
    fontSize: 14, fontWeight: '400', fontWeightBold: '700',
    drawBoldTextInBrightColors: true, minimumContrastRatio: 1, scrollback: 10000,
    theme: termTheme()
  });
  const fitAddon = new FitAddon.FitAddon();
  terminal.loadAddon(fitAddon); terminal.open(pane);
  const paneClose = document.createElement('button');
  paneClose.type = 'button'; paneClose.className = 'pane-close'; paneClose.title = '关闭当前分屏'; paneClose.setAttribute('aria-label', `关闭终端 ${host}`);
  paneClose.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg>';
  paneClose.addEventListener('pointerdown', event => event.stopPropagation());
  paneClose.addEventListener('click', event => { event.stopPropagation(); closeSession(id); });
  pane.append(paneClose);
  const session = { id, host, user:username, tabId:group.id, tab, pane, terminal, fitAddon, socket: null, resizeObserver: null, state: 'connecting' };
  group.sessionIds.add(id); sessions.set(id, session); updateWorkspace();
  pane.addEventListener('pointerdown',()=>{if(activeSessionId!==id)activateSession(id);});
  pane.addEventListener('focusin',()=>{if(activeSessionId!==id)activateSession(id);});
  pane.addEventListener('contextmenu',event=>openTerminalMenu(session,event),true);
  session.resizeObserver = new ResizeObserver(() => { if (activeTabId === group.id) { try { fitAddon.fit(); } catch (_) {} } });
  session.resizeObserver.observe(pane);
  terminal.onData(data => sendTerminal(session, { type: 'input', data }));
  terminal.onResize(size => sendTerminal(session, { type: 'resize', cols: size.cols, rows: size.rows }));
  connectTerminal(session, password, credentials);
}
function connectTerminal(session, password, credentials = {}) {
  if (screenLocked || sessions.get(session.id) !== session) return;
  const {id, host, terminal} = session;
  const authType = credentials.authType || '';
  let privateKey = credentials.privateKey || ''; credentials.privateKey = '';
  session.authType = authType || (hosts.find(item=>item.name===host)?.auth_type === 'private_key' ? 'private_key' : 'password');
  const previous = session.socket; session.socket = null;
  if (previous) previous.close();
  session.uploadSession = ''; session.authenticationFailed = false;
  setSessionState(session, 'connecting', '连接中');
  activateSession(id);
  terminal.writeln('\r\n\x1b[38;5;108mxsh\x1b[0m connecting…');
  const socketURL = new URL(`ws/terminal?token=${encodeURIComponent(token)}`, location.href);
  socketURL.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const socket = new WebSocket(socketURL); session.socket = socket;
  const current = () => sessions.get(id) === session && session.socket === socket;
  socket.onopen = () => {
    if (!current()) { password = ''; privateKey = ''; socket.close(); return; }
    try { sendTerminal(session, { type: 'connect', host, user:session.user, password, auth_type:authType, private_key:privateKey, cols: terminal.cols, rows: terminal.rows }); }
    finally { password = ''; privateKey = ''; }
  };
  socket.onmessage = event => {
    if (!current()) return;
    const data = JSON.parse(event.data);
    if (data.type === 'output') terminal.write(data.data);
    if (data.type === 'connected') {
      session.user = data.user || session.user;
      session.uploadSession = data.session;
      setSessionState(session, 'connected', '已连接');
      terminal.write('\r\n');
      const known = hosts.find(h => h.name === host);
      if (known) { known.last_login = Math.floor(Date.now() / 1000); renderRecent(); renderDrawer(); }
    }
    if (data.type === 'error') {
      session.authenticationFailed = data.code === 'authentication_failed';
      setSessionState(session, 'error', '连接失败'); terminal.writeln(`\r\n\x1b[91m${escapeHtml(data.message)}\x1b[0m`);
      if (session.authenticationFailed) retryTerminalCredentials(session, true);
    }
    if (data.type === 'closed') { setSessionState(session, 'closed', '已断开'); terminal.writeln(`\r\n\x1b[90m${escapeHtml(data.message)}\x1b[0m`); }
    if (data.type === 'trust') sendTerminal(session, { type: 'trust', trust: confirm(`首次连接 ${data.message}\n\n密钥类型：${data.key_type}\n指纹：${data.fingerprint}\n\n确认信任并保存该主机密钥？`) });
  };
  socket.onerror = () => { if (current()) setSessionState(session, 'error', '连接出错'); };
  socket.onclose = () => { password = ''; privateKey = ''; if (current() && ['connected','connecting'].includes(session.state)) setSessionState(session, 'closed', '已断开'); };
}
function sendTerminal(session, message) { if (screenLocked && message.type === 'input') return; if (session.socket?.readyState === WebSocket.OPEN) session.socket.send(JSON.stringify(message)); }
function closeSession(id) {
  if(contextTerminalId===id)closeTerminalMenu();
  const session = sessions.get(id); if (!session) return;
  sendTerminal(session, { type: 'close' });
  if (session.socket) session.socket.close();
  session.resizeObserver.disconnect(); session.terminal.dispose();
  const group=workspaceTabs.get(session.tabId), parent=session.pane.parentElement;
  session.pane.remove(); sessions.delete(id);group?.sessionIds.delete(id);
  if(parent?.classList.contains('terminal-split')&&parent.children.length===1)parent.replaceWith(parent.firstElementChild);
  if(group&&!group.sessionIds.size) {group.root?.remove();group.tab.remove();workspaceTabs.delete(group.id);if(activeTabId===group.id) {activeTabId='';activeSessionId='';const next=[...workspaceTabs.keys()].at(-1);if(next)activateWorkspaceTab(next);}}
  else if(group&&group.focused===id) {group.focused=[...group.sessionIds].at(-1);if(activeTabId===group.id)activateSession(group.focused);}
  updateWorkspace();
  updateUploadButton();
}

function updateUploadButton() {
  const session = sessions.get(activeSessionId);
  const available = session?.state === 'connected' && Boolean(session.uploadSession);
  for (const id of ['btn-upload', 'btn-password']) {
    const button = document.getElementById(id);
    button.hidden = !available;
    button.disabled = !available;
  }
}

/* ---------------- 空状态最近使用 chips ---------------- */
function renderRecent() {
  const recent = hosts.filter(h => h.last_login).sort((a, b) => b.last_login - a.last_login).slice(0, 3);
  recentWrap.hidden = !recent.length;
  if (!recent.length) return;
  recentChips.innerHTML = recent.map(h => `<button class="chip-host" data-n="${escapeHtml(h.name)}"><span class="dot off"></span>${escapeHtml(h.name)}</button>`).join('');
  recentChips.querySelectorAll('.chip-host').forEach(button => {
    button.addEventListener('click', () => { const h = hosts.find(x => x.name === button.dataset.n); if (h) connectToHost(h); });
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

/* ---------------- 退出与锁屏 ---------------- */
async function logoutWeb() {
  for (const id of Array.from(sessions.keys())) closeSession(id);
  await fetch('api/logout', { method:'POST', headers:{'X-XSH-Token':token} });
  token = ''; await lockView();
}
document.querySelector('#btn-logout').addEventListener('click', logoutWeb);
document.querySelector('#screen-lock-logout').addEventListener('click', logoutWeb);
function updateScreenLockInfo() {
  const count = Array.from(sessions.values()).filter(session => session.state === 'connected').length;
  document.querySelector('#screen-lock-connections').textContent = count ? `${count} 个连接保持中` : '登录状态已保留';
}
function showScreenLock() {
  if (!currentUser) return;
  closeTabMenu();
  closeTerminalMenu();
  screenLocked = true; appEl.hidden = true; closeDrawer();
  for (const dialog of document.querySelectorAll('dialog[open]')) if (dialog !== screenLockDialog) dialog.close();
  document.querySelector('#screen-lock-user').textContent = currentUser.username;
  document.querySelector('#screen-lock-error').textContent = '';
  document.querySelector('#screen-unlock-password').value = '';
  updateScreenLockInfo();
  if (!screenLockDialog.open) screenLockDialog.showModal();
  document.querySelector('#screen-unlock-password').focus();
  checkLockedSession(true);
}
const securityIdleLock = document.querySelector('#security-idle-lock');
const securityIdleMinutes = document.querySelector('#security-idle-minutes');
const securityAwayLock = document.querySelector('#security-away-lock');
let securitySettingsKey = '', lastSecurityActivity = Date.now(), lockNeedsSync = false, lockRequest = null, lastLockAttempt = 0;
const securityIdleOptions = [1, 5, 10, 15, 30, 60];
function idleLockMinutes() {
  const minutes = Number(securityIdleMinutes.value);
  return securityIdleOptions.includes(minutes) ? minutes : 5;
}
let lockedSessionCheck = null, lastLockedSessionCheck = 0;
const lockedSessionCheckInterval = 30 * 1000;
function checkLockedSession(force = false) {
  if (!currentUser || !screenLocked || lockedSessionCheck) return lockedSessionCheck;
  if (!force && Date.now() - lastLockedSessionCheck < lockedSessionCheckInterval) return;
  const generation = authGeneration;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 10000);
  lastLockedSessionCheck = Date.now();
  const request = (async () => {
    try {
      const response = await fetch('api/auth/status', {cache:'no-store', signal:controller.signal});
      if (!response.ok) return;
      const status = await response.json();
      if (generation !== authGeneration || !screenLocked) return;
      if (status.authenticated === false) await lockView(status);
    } catch (_) {
      // A network/server failure does not establish that the session expired.
    } finally { clearTimeout(timeout); }
  })();
  lockedSessionCheck = request;
  request.finally(() => { if (lockedSessionCheck === request) lockedSessionCheck = null; });
  return request;
}
function loadSecuritySettings() {
  if (!currentUser) return;
  const key = `xsh-security:${new URL('.', location.href).pathname}:${currentUser.id}:${currentUser.username}`;
  if (key === securitySettingsKey) return;
  securitySettingsKey = key; lastSecurityActivity = Date.now();
  let saved = {};
  try { saved = JSON.parse(localStorage.getItem(key)) || {}; } catch (_) {}
  securityIdleLock.checked = saved.idleLock !== false;
  securityIdleMinutes.value = String(securityIdleOptions.includes(Number(saved.idleMinutes)) ? Number(saved.idleMinutes) : 5);
  securityIdleMinutes.disabled = !securityIdleLock.checked;
  securityAwayLock.checked = saved.awayLock === true;
}
function saveSecuritySettings() {
  securityIdleMinutes.disabled = !securityIdleLock.checked;
  if (!securitySettingsKey) return;
  const message = document.querySelector('#security-msg');
  try {
    localStorage.setItem(securitySettingsKey, JSON.stringify({idleLock:securityIdleLock.checked, idleMinutes:idleLockMinutes(), awayLock:securityAwayLock.checked}));
    message.textContent = '';
  } catch (_) { message.textContent = '浏览器未允许保存，设置仅在本次页面中生效。'; }
  lastSecurityActivity = Date.now();
}
for (const input of [securityIdleLock, securityIdleMinutes, securityAwayLock]) input.addEventListener('change', saveSecuritySettings);
function lockWorkspace() {
  if (!currentUser || lockRequest) return lockRequest;
  const generation = authGeneration;
  lockNeedsSync = true; lastLockAttempt = Date.now();
  if (!screenLocked) showScreenLock();
  const request = (async () => {
    try {
      const response = await fetch('api/lock',{method:'POST',headers:{'X-XSH-Token':token}});
      if (generation !== authGeneration) return;
      if (response.status === 401) { await lockView(); return; }
      if (!response.ok) throw new Error((await response.text()).trim());
      lockNeedsSync = false;
      if (screenLocked) document.querySelector('#screen-lock-error').textContent = '';
    } catch (_) {
      if (generation === authGeneration && screenLocked) document.querySelector('#screen-lock-error').textContent = '页面已锁定，暂时无法同步到服务器，正在重试。';
    }
  })();
  lockRequest = request;
  request.finally(() => { if (lockRequest === request) lockRequest = null; });
  return request;
}
function checkSecurityLock() {
  if (!currentUser) return;
  if (screenLocked) {
    checkLockedSession();
    if (lockNeedsSync && Date.now() - lastLockAttempt >= 5000) lockWorkspace();
    return;
  }
  if (securityIdleLock.checked && Date.now() - lastSecurityActivity >= idleLockMinutes() * 60 * 1000) lockWorkspace();
}
function recordSecurityActivity(event) {
  if (!event.isTrusted || !currentUser || screenLocked) return;
  // Check the deadline before accepting the first interaction after sleep/background throttling.
  checkSecurityLock();
  if (screenLocked) { event.preventDefault(); event.stopImmediatePropagation(); }
  else lastSecurityActivity = Date.now();
}
for (const type of ['keydown','pointerdown','pointermove','wheel','touchstart']) window.addEventListener(type, recordSecurityActivity, {capture:true});
document.addEventListener('visibilitychange', () => {
  if (!currentUser) return;
  if (screenLocked) { if (!document.hidden) checkLockedSession(true); return; }
  if (document.hidden && securityAwayLock.checked) lockWorkspace();
  else checkSecurityLock();
});
function checkReturningWorkspace() {
  checkSecurityLock();
  if (screenLocked) checkLockedSession(true);
}
window.addEventListener('focus', checkReturningWorkspace);
window.addEventListener('pageshow', checkReturningWorkspace);
setInterval(checkSecurityLock, 1000);
for (const id of ['btn-screen-lock','security-lock-now']) document.getElementById(id).addEventListener('click', lockWorkspace);
screenLockDialog.addEventListener('cancel', event => event.preventDefault());
document.querySelector('#screen-unlock-form').addEventListener('submit', async event => {
  event.preventDefault(); const submit = event.submitter, input = document.querySelector('#screen-unlock-password');
  submit.disabled = true;
  try {
    if (lockRequest) await lockRequest;
    const response = await fetch('api/unlock',{method:'POST',headers:{'Content-Type':'application/json','X-XSH-Token':token},body:JSON.stringify({password:input.value})});
    input.value = '';
    if (response.status === 401) { await lockView(); return; }
    if (!response.ok) throw new Error((await response.text()).trim());
    if (!await loadHosts()) return;
    screenLocked = false; lockNeedsSync = false; lastSecurityActivity = Date.now(); screenLockDialog.close();
    const active = sessions.get(activeSessionId);
    if (active) requestAnimationFrame(() => { try { active.fitAddon.fit(); active.terminal.focus(); } catch (_) {} });
  } catch(error) { document.querySelector('#screen-lock-error').textContent = error.message; input.focus(); }
  finally { submit.disabled = false; }
});
/* 有活跃 SSH 会话时，浏览器关闭标签/窗口（含 Ctrl+W / Ctrl+Shift+W）会触发原生「离开确认」，
   取消即可保住会话；无会话或处于登录页时不打扰。 */
window.addEventListener('beforeunload', event => {
  if (!currentUser || sessions.size === 0) return;
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
  for (const button of document.querySelectorAll('[data-theme-choice]')) button.setAttribute('aria-pressed', String(button.dataset.themeChoice === mode));
  applyTerminalTheme();
}
let savedTheme = 'dark';
try { savedTheme = localStorage.getItem('xsh-theme') || 'dark'; } catch (_) {}
setTheme(savedTheme);
for (const button of document.querySelectorAll('[data-theme-choice]')) button.addEventListener('click', () => setTheme(button.dataset.themeChoice));

async function initialize() {
  const statusResponse = await fetch('api/auth/status');
  const status = statusResponse.ok ? await statusResponse.json() : {};
  if (status.authenticated && status.locked) { currentUser = status.user; token = status.token; loadSecuritySettings(); showScreenLock(); }
  else if (status.authenticated) await loadHosts();
  else await lockView(status);
}
initialize().catch(async error => { await lockView(); document.querySelector('#master-error').textContent = error.message; });


/* ---------------- 远程用户密码 ---------------- */
const passwordDialog = document.querySelector('#dlg-password');
const passwordForm = document.querySelector('#password-form');
let passwordSession = null, passwordBusy = false, passwordChanged = false, passwordUncertain = false;
function passwordMessage(message, error = false) {
  const el = document.querySelector('#password-msg');
  el.textContent = message; el.className = error ? 'form-msg error' : 'form-msg form-ok';
}
function randomPassword(length) {
  if (!Number.isInteger(length) || length < 12 || length > 128) throw new Error('随机密码长度须为 12–128。');
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*_-+=?';
  const limit = 256 - (256 % alphabet.length);
  for (;;) {
    let result = '';
    while (result.length < length) {
      const bytes = crypto.getRandomValues(new Uint8Array(length * 2));
      for (const value of bytes) {
        if (value < limit) result += alphabet[value % alphabet.length];
        if (result.length === length) break;
      }
    }
    if (/[A-Z]/.test(result) && /[a-z]/.test(result) && /[0-9]/.test(result) && /[^A-Za-z0-9]/.test(result)) return result;
  }
}
async function copyPassword(input) {
  if (!input.value) throw new Error('请先输入或生成密码。');
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(input.value); return;
  }
  // HTTP deployments may lack the Clipboard API. Keep the temporary field in
  // the active dialog, because modal dialogs make the rest of the page inert.
  const temporary = document.createElement('textarea');
  temporary.value = input.value; temporary.className = 'clipboard-helper';
  (input.closest('dialog') || document.body).append(temporary);
  temporary.select();
  try { if (!document.execCommand('copy')) throw new Error('浏览器未允许复制，请显示密码后手动复制。'); }
  finally { temporary.remove(); input.focus(); }
}
for (const button of document.querySelectorAll('[data-generate], [data-copy]')) {
  button.addEventListener('click', async () => {
    const id = button.dataset.generate || button.dataset.copy;
    const input = document.getElementById(id);
    const status = document.querySelector('#password-msg');
    status.classList.remove('error');
    try {
      if (button.dataset.generate) {
        input.value = randomPassword(20);
        if (id === 'password-new') document.querySelector('#password-confirm').value = input.value;
        status.textContent = '已生成随机密码，请复制并妥善保存。';
      } else { await copyPassword(input); status.textContent = '密码已复制。'; }
    } catch (error) { status.textContent = error.message; status.classList.add('error'); }
  });
}
let currentPasswordRequest = 0;
async function useCurrentPassword(action) {
  if (!passwordSession || passwordBusy || passwordChanged || passwordUncertain) return;
  const input = document.querySelector('#password-current');
  const toggle = document.querySelector('#password-current-toggle');
  const copy = document.querySelector('#password-current-copy');
  if (action === 'show' && input.type === 'text') {
    input.type = 'password'; toggle.setAttribute('aria-label', '显示当前密码'); toggle.title = '显示当前密码'; return;
  }
  const request = ++currentPasswordRequest, session = passwordSession;
  toggle.disabled = copy.disabled = true;
  try {
    if (!input.value) {
      const response = await fetch('api/password/current', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-XSH-Token': token },
        body: JSON.stringify({ session: session.uploadSession })
      });
      if (!response.ok) throw new Error((await response.text()).trim());
      const data = await response.json();
      if (request !== currentPasswordRequest || passwordSession !== session || !passwordDialog.open || passwordBusy || passwordChanged || passwordUncertain) return;
      if (!input.value) input.value = data.password;
    }
    if (action === 'copy') { await copyPassword(input); passwordMessage('当前密码已复制。'); }
    else { input.type = 'text'; toggle.setAttribute('aria-label', '隐藏当前密码'); toggle.title = '隐藏当前密码'; }
  } catch (error) {
    if (request === currentPasswordRequest && passwordDialog.open) passwordMessage(error.message, true);
  } finally {
    if (request === currentPasswordRequest) toggle.disabled = copy.disabled = passwordBusy || passwordChanged || passwordUncertain;
  }
}
document.querySelector('#password-current-toggle').addEventListener('click', () => useCurrentPassword('show'));
document.querySelector('#password-current-copy').addEventListener('click', () => useCurrentPassword('copy'));

document.querySelector('#btn-password').addEventListener('click', () => {
  const session = sessions.get(activeSessionId);
  if (session?.state !== 'connected' || !session.uploadSession) return;
  currentPasswordRequest++;
  passwordSession = session; passwordChanged = false; passwordUncertain = false; passwordForm.reset();
  document.querySelector('#password-current').type = 'password';
  document.querySelector('#password-current-toggle').setAttribute('aria-label', '显示当前密码');
  document.querySelector('#password-current-toggle').title = '显示当前密码';
  for (const control of passwordDialog.querySelectorAll('button, input')) control.disabled = false;
  document.querySelector('#password-new').type = 'password'; passwordMessage('');
  const profile = hosts.find(host => host.name === session.host);
  document.querySelector('#password-target').textContent = profile ? `${session.host} · ${session.user || profile.user}@${profile.hostname}:${profile.port}` : `${session.host} · 当前登录用户`;
  document.querySelector('#password-submit').disabled = false;
  passwordDialog.showModal(); document.querySelector('#password-current').focus();
});
passwordDialog.addEventListener('cancel', event => { if (passwordBusy) event.preventDefault(); });
passwordDialog.addEventListener('close', () => { currentPasswordRequest++; passwordForm.reset(); document.querySelector('#password-current').type = 'password'; passwordSession = null; passwordMessage(''); });
passwordForm.addEventListener('submit', async event => {
  event.preventDefault();
  if (passwordBusy || passwordChanged || passwordUncertain || !passwordSession) return;
  const next = document.querySelector('#password-new').value;
  const confirmation = document.querySelector('#password-confirm').value;
  if (next !== confirmation) { passwordMessage('两次输入的新密码不一致。', true); return; }
  if (passwordSession.state !== 'connected') { passwordMessage('终端已断开，请重新连接。', true); return; }
  passwordBusy = true;
  for (const control of passwordDialog.querySelectorAll('button, input')) control.disabled = true;
  passwordMessage('正在修改远程密码，请等待服务器确认…');
  try {
    const response = await fetch('api/password', {
      method: 'POST', headers: { 'Content-Type': 'application/json', 'X-XSH-Token': token },
      body: JSON.stringify({ session: passwordSession.uploadSession, current_password: document.querySelector('#password-current').value, password: next, confirm: confirmation })
    });
    if (!response.ok) {
      passwordUncertain = response.status === 504;
      throw new Error((await response.text()).trim());
    }
    const result = await response.json(); passwordChanged = Boolean(result.changed);
    passwordMessage(result.message);
    document.querySelector('#password-current').value = '';
    document.querySelector('#password-confirm').value = '';
  } catch (error) {
    if (error instanceof TypeError) passwordUncertain = true;
    const message = passwordUncertain ? '未收到明确的改密结果，请先验证远程密码。为避免重复修改，本次提交已停止；新密码仍可复制。' : error.message;
    passwordMessage(message, true);
  }
  finally {
    passwordBusy = false;
    for (const control of passwordDialog.querySelectorAll('button, input')) control.disabled = false;
    document.querySelector('#password-submit').disabled = passwordChanged || passwordUncertain;
    if (passwordChanged || passwordUncertain) {
      for (const control of passwordDialog.querySelectorAll('input, [data-generate], #password-current-toggle, #password-current-copy')) control.disabled = true;
    }
  }
});

hostDialog.addEventListener('close', () => { hostField('password').value = ''; hostField('private_key').value = ''; });


/* ---------------- 账号与配置分享 ---------------- */
async function accountRequest(path, method = 'GET', body) {
  const generation = authGeneration;
  const response = await fetch(path, { method, headers:{'Content-Type':'application/json','X-XSH-Token':token}, body:body ? JSON.stringify(body) : undefined });
  if (generation !== authGeneration) throw new DOMException('账号已切换', 'AbortError');
  if (response.status === 401) { await lockView(); throw new Error('登录已失效，请重新登录。'); }
  if (response.status === 423) { showScreenLock(); throw new DOMException('屏幕已锁定', 'AbortError'); }
  if (!response.ok) throw new Error((await response.text()).trim());
  const result = response.status === 204 ? null : await response.json();
  if (generation !== authGeneration) throw new DOMException('账号已切换', 'AbortError');
  return result;
}
const shareDialog = document.querySelector('#dlg-share'), inboxDialog = document.querySelector('#dlg-inbox'), accountDialog = document.querySelector('#dlg-settings');
const sharePicker = document.querySelector('#share-picker'), shareToggle = document.querySelector('#share-recipient-toggle'), sharePanel = document.querySelector('#share-recipient-panel'), shareSearch = document.querySelector('#share-recipient-search'), shareSubmit = document.querySelector('#share-submit');
let shareRecipients = [], selectedRecipients = new Set(), shareRevision = 0, shareSending = false;
function setSharePickerOpen(open) {
  sharePanel.hidden = !open; shareToggle.setAttribute('aria-expanded', String(open));
  if (open) shareSearch.focus();
}
function updateShareSelection() {
  const names = [...selectedRecipients];
  document.querySelector('#share-recipient-summary').textContent = names.length > 2 ? `${names.slice(0, 2).join('、')} 等 ${names.length} 个账号` : names.join('、') || '请选择接收账号';
  shareSubmit.disabled = shareSending || !names.length;
}
function renderShareRecipients() {
  const list = document.querySelector('#share-recipient-options'), query = shareSearch.value.trim().toLowerCase();
  list.replaceChildren();
  const visible = shareRecipients.filter(item => item.username.toLowerCase().includes(query));
  if (!visible.length) { const empty = document.createElement('p'); empty.className = 'share-picker-empty'; empty.textContent = shareRecipients.length ? '没有匹配的账号' : '暂无可分享账号'; list.append(empty); }
  for (const item of visible) {
    const label = document.createElement('label'), checkbox = document.createElement('input'), name = document.createElement('span');
    label.className = 'share-picker-option'; checkbox.type = 'checkbox'; checkbox.checked = selectedRecipients.has(item.username); name.textContent = item.username;
    checkbox.addEventListener('change', () => { if (checkbox.checked) selectedRecipients.add(item.username); else selectedRecipients.delete(item.username); updateShareSelection(); });
    label.append(checkbox, name); list.append(label);
  }
}
shareToggle.addEventListener('click', () => setSharePickerOpen(sharePanel.hidden));
shareSearch.addEventListener('input', renderShareRecipients);
shareSearch.addEventListener('keydown', event => { if (event.key === 'Enter') event.preventDefault(); });
sharePicker.addEventListener('keydown', event => { if (event.key === 'Escape' && !sharePanel.hidden) { event.preventDefault(); event.stopPropagation(); setSharePickerOpen(false); shareToggle.focus(); } });
document.addEventListener('click', event => { if (!sharePicker.contains(event.target)) setSharePickerOpen(false); });
shareDialog.addEventListener('close', () => { shareRevision++; selectedRecipients.clear(); shareRecipients = []; setSharePickerOpen(false); });
async function openShare(hostName) {
  if (screenLocked || !currentUser) return;
  sharingHost = hostName; closeDrawer(); if (hostDialog.open) hostDialog.close(); document.querySelector('#share-form').reset();
  const revision = ++shareRevision; selectedRecipients.clear(); shareRecipients = []; shareSending = false; updateShareSelection(); setSharePickerOpen(false); shareToggle.disabled = true;
  document.querySelector('#share-target').textContent = sharingHost; document.querySelector('#share-msg').textContent = ''; shareDialog.showModal();
  try { const items = await accountRequest('api/share-recipients'); if (revision !== shareRevision || !shareDialog.open) return; shareRecipients = items; renderShareRecipients(); shareToggle.disabled = false; }
  catch (error) { if (revision === shareRevision && error.name !== 'AbortError') document.querySelector('#share-msg').textContent = error.message; }
}
document.querySelector('#btn-share-host').addEventListener('click', () => openShare(editingOriginal));
document.querySelector('#share-form').addEventListener('submit', async event => {
  event.preventDefault(); if (shareSending || !selectedRecipients.size) return;
  const revision = shareRevision; shareSending = true; updateShareSelection(); document.querySelector('#share-msg').textContent = '';
  try { await accountRequest('api/shares','POST',{name:sharingHost,recipients:[...selectedRecipients]}); if (revision === shareRevision) shareDialog.close(); }
  catch (error) { if (revision === shareRevision && error.name !== 'AbortError') document.querySelector('#share-msg').textContent = error.message; }
  finally { if (revision === shareRevision) { shareSending = false; updateShareSelection(); } }
});
async function loadInbox() {
  const items = await accountRequest('api/shares'), list = document.querySelector('#inbox-list');
  list.replaceChildren();
  if (!items.length) { list.textContent = '暂无分享的配置'; return; }
  for (const item of items) {
    const row = document.createElement('div'); row.className = 'account-list-row';
    const text = document.createElement('div'); text.className = 'account-list-meta';
    const title = document.createElement('strong'); title.textContent = item.config.name;
    const detail = document.createElement('span'); detail.className = 'hint'; detail.textContent = `${item.config.hostname}:${item.config.port} · 来自 ${item.sender}`;
    text.append(title, detail);
    const accept = document.createElement('button'); accept.type = 'button'; accept.className = 'btn btn-primary'; accept.textContent = '添加';
    accept.onclick = () => { inboxDialog.close(); openHostModal({...item.config,user:'',auth_type:'password',save_password:false},item.id); };
    const reject = document.createElement('button'); reject.type = 'button'; reject.className = 'btn btn-subtle'; reject.textContent = '忽略';
    reject.onclick = async () => { try { await accountRequest('api/shares','DELETE',{id:item.id}); await loadInbox(); } catch(error) { document.querySelector('#inbox-msg').textContent = error.message; } };
    row.append(text,accept,reject); list.append(row);
  }
}
document.querySelector('#btn-shares').addEventListener('click', async () => {
  document.querySelector('#inbox-msg').textContent = ''; inboxDialog.showModal();
  try { await loadInbox(); } catch(error) { document.querySelector('#inbox-msg').textContent = error.message; }
});
function renderUsers() {
  const query = document.querySelector('#users-search').value.trim().toLowerCase(), filter = document.querySelector('#users-filter').value;
  const visible = settingsUsers.filter(user => user.username.toLowerCase().includes(query) && (filter === 'all' || (filter === 'disabled') === user.disabled));
  const body = document.querySelector('#account-users'); body.replaceChildren();
  document.querySelector('#users-count').textContent = settingsUsers.length;
  document.querySelector('#users-summary').textContent = `共 ${settingsUsers.length} 位用户 · ${settingsUsers.filter(user => !user.disabled).length} 位已启用`;
  if (!visible.length) {
    const row = document.createElement('tr'), cell = document.createElement('td'); cell.colSpan = 4; cell.className = 'users-empty'; cell.textContent = '没有匹配的用户'; row.append(cell); body.append(row); return;
  }
  for (const user of visible) {
    const row = document.createElement('tr');
    const identity = document.createElement('td'), person = document.createElement('div'); person.className = 'users-person';
    const avatar = document.createElement('span'); avatar.className = 'users-avatar'; avatar.textContent = user.username.charAt(0).toUpperCase();
    const name = document.createElement('span'); name.textContent = user.username; person.append(avatar,name);
    if (user.id === currentUser?.id) { const self = document.createElement('small'); self.className = 'users-self'; self.textContent = '你'; person.append(self); }
    identity.append(person);
    const role = document.createElement('td'), roleBadge = document.createElement('span'); roleBadge.className = 'users-role'; roleBadge.textContent = user.admin ? '管理员' : '成员'; role.append(roleBadge);
    const state = document.createElement('td'), status = document.createElement('span'); status.className = `users-state${user.disabled ? ' disabled' : ''}`; status.textContent = user.disabled ? '已停用' : '已启用'; state.append(status);
    const actions = document.createElement('td'); actions.className = 'users-actions';
    if (user.admin) { const label = document.createElement('span'); label.className = 'users-no-action'; label.textContent = '—'; actions.append(label); }
    else {
      const button = document.createElement('button'); button.type = 'button'; button.className = 'users-status-action'; button.textContent = user.disabled ? '启用' : '停用'; button.setAttribute('aria-label', `${button.textContent} ${user.username}`);
      button.onclick = async () => {
        button.disabled = true;
        try { await accountRequest('api/users','PUT',{id:user.id,disabled:!user.disabled}); await loadUsers(); }
        catch(error) { document.querySelector('#account-msg').textContent = error.message; button.disabled = false; }
      }; actions.append(button);
      const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'users-status-action users-delete-action'; remove.textContent = '删除'; remove.setAttribute('aria-label', `删除 ${user.username}`);
      remove.onclick = async () => {
        if (!window.confirm(`确认删除账号「${user.username}」？\n\n该账号的主机、保存的凭据及收发分享记录将永久删除，现有连接会断开。此操作无法撤销。`)) return;
        button.disabled = true; remove.disabled = true;
        document.querySelector('#account-msg').textContent = '';
        try { await accountRequest('api/users','DELETE',{id:user.id}); await loadUsers(); }
        catch(error) { document.querySelector('#account-msg').textContent = error.message; button.disabled = false; remove.disabled = false; }
      }; actions.append(remove);
    }
    row.append(identity,role,state,actions); body.append(row);
  }
}
async function loadUsers() { if (!currentUser?.admin) return; settingsUsers = await accountRequest('api/users'); renderUsers(); }
async function loadAbout() {
  const details = document.querySelector('.about-details');
  details.setAttribute('aria-busy', 'true');
  for (const id of ['version','revision','commit-time','platform','go-version']) document.getElementById(`about-${id}`).textContent = '读取中…';
  try {
    const info = await accountRequest('api/about');
    document.querySelector('#about-version').textContent = info.version === 'dev' ? '开发版本 (dev)' : info.version;
    document.querySelector('#about-revision').textContent = info.revision ? `${info.revision.slice(0, 12)}${info.modified ? '（含未提交修改）' : ''}` : '未提供';
    document.querySelector('#about-commit-time').textContent = info.commit_time ? new Date(info.commit_time).toLocaleString() : '未提供';
    document.querySelector('#about-platform').textContent = info.platform;
    document.querySelector('#about-go-version').textContent = info.go_version;
  } catch (error) {
    for (const id of ['version','revision','commit-time','platform','go-version']) document.getElementById(`about-${id}`).textContent = '读取失败';
    if (!document.querySelector('[data-settings-panel="about"]').hidden) document.querySelector('#account-msg').textContent = error.message;
  } finally { details.setAttribute('aria-busy', 'false'); }
}
async function selectSettingsTab(tab) {
  if (tab === 'users' && !currentUser?.admin) tab = 'account';
  for (const button of document.querySelectorAll('[data-settings-tab]')) { const active = button.dataset.settingsTab === tab; button.classList.toggle('active',active); if (active) button.setAttribute('aria-current','page'); else button.removeAttribute('aria-current'); }
  for (const panel of document.querySelectorAll('[data-settings-panel]')) panel.hidden = panel.dataset.settingsPanel !== tab;
  document.querySelector('#account-msg').textContent = '';
  if (tab === 'users') { try { await loadUsers(); } catch(error) { document.querySelector('#account-msg').textContent = error.message; } }
  if (tab === 'about') await loadAbout();
}
for (const button of document.querySelectorAll('[data-settings-tab]')) button.addEventListener('click', () => selectSettingsTab(button.dataset.settingsTab));
document.querySelector('#users-search').addEventListener('input',renderUsers);
document.querySelector('#users-filter').addEventListener('change',renderUsers);
document.querySelector('#btn-settings').addEventListener('click', () => {
  const username = currentUser?.username || '', role = currentUser?.admin ? '管理员' : '成员';
  for (const id of ['account-name','account-profile-name']) document.getElementById(id).textContent = username;
  for (const id of ['account-role','account-profile-role']) document.getElementById(id).textContent = role;
  for (const id of ['settings-avatar','account-profile-avatar']) document.getElementById(id).textContent = username.charAt(0).toUpperCase();
  document.querySelector('#settings-nav-users').hidden = !currentUser?.admin;
  selectSettingsTab('account'); accountDialog.showModal();
});
accountDialog.addEventListener('close', () => { document.querySelector('#account-password-form').reset(); settingsUsers = []; document.querySelector('#account-users').replaceChildren(); });
const createUserDialog = document.querySelector('#dlg-create-user');
document.querySelector('#btn-create-user').addEventListener('click', () => { document.querySelector('#user-create-form').reset(); document.querySelector('#user-create-password').type = 'password'; document.querySelector('#user-create-msg').textContent = ''; createUserDialog.showModal(); document.querySelector('#user-create-name').focus(); });
createUserDialog.addEventListener('close', () => document.querySelector('#user-create-form').reset());
document.querySelector('#user-create-form').addEventListener('submit', async event => {
  event.preventDefault(); const submit = event.submitter; submit.disabled = true;
  try { await accountRequest('api/users','POST',{username:document.querySelector('#user-create-name').value,password:document.querySelector('#user-create-password').value}); event.target.reset(); createUserDialog.close(); await loadUsers(); }
  catch(error) { document.querySelector('#user-create-msg').textContent = error.message; }
  finally { submit.disabled = false; }
});
document.querySelector('#account-password-form').addEventListener('submit', async event => {
  event.preventDefault(); const submit = event.submitter; submit.disabled = true;
  try { await accountRequest('api/account/password','POST',{current_password:document.querySelector('#account-current').value,password:document.querySelector('#account-new').value,confirm:document.querySelector('#account-confirm').value}); await lockView(); }
  catch(error) { document.querySelector('#account-msg').textContent = error.message; }
  finally { submit.disabled = false; }
});
