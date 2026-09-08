'use strict';

const fm = document.querySelector('#file-manager');
const fEl = id => document.getElementById(id);
const fileState = {
  target: null, remotePath: '', remote: [], selected: new Set(),
  busy: false, loading: false, generation: 0, xhr: null, controller: null
};
const joinRemote = (directory, name) => `${directory.replace(/\/$/, '')}/${name}`;

function fileMessage(message, error = false) {
  fEl('files-status').textContent = message;
  fEl('files-status').classList.toggle('error', error);
}
function fileSize(size) {
  if (size < 1024) return `${size} B`;
  const unit = Math.min(3, Math.floor(Math.log(size) / Math.log(1024)));
  return `${(size / 1024 ** unit).toFixed(1)} ${['B', 'KB', 'MB', 'GB'][unit]}`;
}
function fileControls() {
  const locked = fileState.busy;
  fm.querySelectorAll('.fm-path button, .fm-path input, .fm-header-actions button, .fm-table-wrap input, .fm-name').forEach(element => { element.disabled = locked; });
  fEl('files-download').disabled = locked || fileState.loading || fileState.selected.size !== 1;
  fEl('remote-up').disabled = locked || fileState.loading || !fileState.remotePath || fileState.remotePath === '/';
  fEl('files-close').disabled = locked;
  fEl('files-pick').disabled = locked || fileState.loading || !fileState.remotePath;
  fEl('files-cancel').hidden = !locked;
}
function renderRemote() {
  const entries = fileState.remote;
  entries.sort((a, b) => Number(b.directory) - Number(a.directory) || a.name.localeCompare(b.name, 'zh-CN', { numeric: true }));
  const body = fEl('remote-entries');
  body.innerHTML = entries.map((entry, index) => `<tr data-index="${index}" class="${fileState.selected.has(entry.name) ? 'selected' : ''}">
    <td>${entry.regular ? `<input type="radio" name="remote-file" aria-label="选择 ${escapeHtml(entry.name)}" ${fileState.selected.has(entry.name) ? 'checked' : ''}>` : ''}</td>
    <td><button class="fm-name" type="button" title="${escapeHtml(entry.name)}"><span class="fm-kind">${entry.directory ? '▸' : '·'}</span>${escapeHtml(entry.name)}</button></td>
    <td>${entry.directory ? '—' : fileSize(entry.size)}</td><td>${entry.directory ? '文件夹' : entry.regular ? '文件' : '特殊文件'}</td>
    <td>${entry.modified ? new Date(entry.modified).toLocaleString('zh-CN', { hour12: false }) : '—'}</td></tr>`).join('');
  if (!entries.length) body.innerHTML = '<tr><td colspan="5" class="fm-empty">此目录为空，可将文件拖到这里上传</td></tr>';
  body.querySelectorAll('tr[data-index]').forEach(row => {
    const entry = entries[Number(row.dataset.index)];
    const checkbox = row.querySelector('input');
    const toggle = () => {
      if (fileState.busy || !entry.regular) return;
      if (fileState.selected.has(entry.name)) fileState.selected.clear(); else fileState.selected = new Set([entry.name]);
      renderRemote();
    };
    if (checkbox) checkbox.onchange = toggle;
    const button = row.querySelector('button');
    button.onclick = () => { if (!entry.directory) toggle(); };
    const open = () => {
      if (!fileState.busy && entry.directory) fileAction(() => loadRemote(joinRemote(fileState.remotePath, entry.name)));
    };
    button.ondblclick = open;
    button.onkeydown = event => { if (event.key === 'Enter' && entry.directory) { event.preventDefault(); open(); } };
    if (entry.directory) {
      row.addEventListener('dragover', event => { event.preventDefault(); event.stopPropagation(); row.classList.add('drop-target'); });
      row.addEventListener('dragleave', event => { if (!row.contains(event.relatedTarget)) row.classList.remove('drop-target'); });
      row.addEventListener('drop', event => {
        event.preventDefault(); event.stopPropagation(); row.classList.remove('drop-target'); clearDropState();
        fileAction(() => uploadFiles(droppedFiles(event), joinRemote(fileState.remotePath, entry.name)));
      });
    }
  });
  fEl('remote-count').textContent = `${entries.length} 项 · 已选 ${fileState.selected.size} 个文件`;
  fileControls();
}
async function fileAction(action) {
  try { await action(); } catch (error) { if (error.name !== 'AbortError') fileMessage(error.message, true); }
}
async function loadRemote(directory = fileState.remotePath || '.') {
  const generation = ++fileState.generation;
  fileState.loading = true; fileControls();
  try {
    const query = new URLSearchParams({ session: fileState.target.uploadSession, path: directory });
    const response = await fetch(`/api/files?${query}`);
    if (!response.ok) throw new Error((await response.text()).trim());
    const data = await response.json();
    if (generation !== fileState.generation) return;
    fileState.remotePath = data.path; fileState.remote = data.entries;
    fileState.target.filePath = data.path; fileState.selected.clear();
    fEl('remote-path').value = data.path;
    fEl('upload-target').textContent = data.path;
    renderRemote();
  } catch (error) {
    if (generation === fileState.generation) { fEl('remote-path').value = fileState.remotePath; throw error; }
  } finally {
    if (generation === fileState.generation) { fileState.loading = false; fileControls(); }
  }
}

fEl('btn-upload').onclick = () => fileAction(async () => {
  const target = sessions.get(activeSessionId);
  if (target?.state !== 'connected') return;
  fileState.target = target; fileState.remotePath = ''; fileState.remote = []; fileState.selected.clear();
  fEl('files-host').textContent = `SSH · ${target.host}`;
  fEl('remote-path').value = ''; fEl('upload-target').textContent = '加载中…';
  fEl('files-progress').hidden = true;
  fileMessage('双击目录打开；选择文件或拖拽文件到列表即可上传。');
  renderRemote(); fm.showModal();
  await loadRemote(target.filePath || '.');
});
function closeFiles() {
  if (fileState.busy) { fileMessage('请先等待传输完成或取消传输。'); return; }
  fileState.generation++; fm.close();
}
fEl('files-close').onclick = closeFiles;
fm.addEventListener('cancel', event => { event.preventDefault(); closeFiles(); });
fEl('remote-path-form').onsubmit = event => { event.preventDefault(); if (!fileState.busy) fileAction(() => loadRemote(fEl('remote-path').value)); };
fEl('remote-refresh').onclick = () => fileAction(() => loadRemote());
fEl('remote-up').onclick = () => fileAction(() => loadRemote(joinRemote(fileState.remotePath, '..')));
function uploadFile(file, directory, label) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest(); fileState.xhr = xhr;
    const query = new URLSearchParams({ session: fileState.target.uploadSession, directory, name: file.name });
    xhr.open('POST', `/api/upload?${query}`); xhr.setRequestHeader('X-XSH-Token', token);
    xhr.upload.onprogress = event => {
      const percent = event.lengthComputable && event.total ? event.loaded / event.total * 100 : 0;
      fEl('files-progress').value = percent;
      fileMessage(`${label} · ${Math.round(percent)}%${percent === 100 ? '，等待远程保存…' : ''}`);
    };
    xhr.onload = () => xhr.status === 204 ? resolve() : reject(new Error(xhr.responseText.trim() || '上传失败。'));
    xhr.onerror = () => reject(new Error('网络连接中断。'));
    xhr.onabort = () => reject(new DOMException('已取消上传。', 'AbortError'));
    if (fileState.controller.signal.aborted) { reject(new DOMException('已取消上传。', 'AbortError')); return; }
    xhr.send(file);
  });
}
async function uploadFiles(fileList, directory = fileState.remotePath) {
  if (fileState.busy || fileState.loading) return;
  const files = Array.from(fileList || []);
  if (!files.length) { fileMessage('拖拽目录暂不支持，请拖入文件。', true); return; }
  if (fileState.target?.state !== 'connected') { fileMessage('终端已断开，请重新连接。', true); return; }
  fileState.busy = true; fileState.controller = new AbortController(); fileControls();
  fEl('files-progress').hidden = false;
  let completed = 0;
  try {
    for (const file of files) {
      fileState.controller.signal.throwIfAborted();
      const label = `上传 ${completed + 1}/${files.length} · ${file.name}`;
      fEl('files-progress').value = 0; fileMessage(`${label} → ${directory}`);
      await uploadFile(file, directory, label); completed++;
    }
    fEl('files-progress').value = 100;
    fileMessage(`已上传 ${completed} 个文件到 ${directory}。`);
  } catch (error) {
    fileMessage(`已完成 ${completed}/${files.length}；${files[completed]?.name || ''}：${error.name === 'AbortError' ? '已取消上传。' : error.message}`, true);
  } finally {
    fileState.busy = false; fileState.xhr = null; fileState.controller = null; fileControls();
    await fileAction(() => loadRemote());
  }
}
fEl('files-pick').onclick = () => fEl('upload-file-picker').click();
fEl('upload-file-picker').onchange = () => {
  const files = Array.from(fEl('upload-file-picker').files);
  fEl('upload-file-picker').value = '';
  fileAction(() => uploadFiles(files));
};
function clearDropState() {
  fEl('remote-dropzone').classList.remove('is-dragging');
  fm.querySelectorAll('.drop-target').forEach(element => element.classList.remove('drop-target'));
}
function droppedFiles(event) {
  const items = Array.from(event.dataTransfer?.items || []);
  if (items.some(item => item.webkitGetAsEntry?.()?.isDirectory)) return [];
  return event.dataTransfer?.files || [];
}
const dropzone = fEl('remote-dropzone');
dropzone.addEventListener('dragenter', event => { event.preventDefault(); if (!fileState.busy) dropzone.classList.add('is-dragging'); });
dropzone.addEventListener('dragover', event => { event.preventDefault(); if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'; });
dropzone.addEventListener('dragleave', event => { if (!dropzone.contains(event.relatedTarget)) clearDropState(); });
dropzone.addEventListener('drop', event => {
  event.preventDefault(); clearDropState();
  fileAction(() => uploadFiles(droppedFiles(event)));
});
window.addEventListener('dragend', clearDropState);
window.addEventListener('drop', clearDropState);
fEl('files-download').onclick = () => {
  const name = Array.from(fileState.selected)[0];
  if (!name) return;
  const query = new URLSearchParams({ session: fileState.target.uploadSession, path: joinRemote(fileState.remotePath, name) });
  const link = document.createElement('a'); link.href = `/api/download?${query}`; link.download = name;
  document.body.append(link); link.click(); link.remove();
  fileMessage(`已将 ${name} 交给浏览器下载，请在浏览器下载列表查看结果。`);
};
fEl('files-cancel').onclick = () => { fileState.controller?.abort(); fileState.xhr?.abort(); };
