
let TOKEN = localStorage.getItem('ss_token') || '';
let TASKS = [], STORAGES = [], PLUGINS = [];

const dtf = new Intl.DateTimeFormat('zh-CN', {year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false});
function fmtTime(iso) { if (!iso) return '-'; const d = new Date(iso); return isNaN(d) ? '-' : dtf.format(d); }

async function api(path, method='GET', body) {
  const opt = {method, headers: {'Content-Type':'application/json'}};
  if (TOKEN) opt.headers['Authorization'] = 'Bearer ' + TOKEN;
  if (body !== undefined) opt.body = JSON.stringify(body);
  const r = await fetch(path, opt);
  if (r.status === 401) { showLogin(); throw new Error('未登录'); }
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
  return d;
}

function showLogin() { document.getElementById('login').style.display='flex'; document.getElementById('app').style.display='none'; }
function showApp() { document.getElementById('login').style.display='none'; document.getElementById('app').style.display='block'; }

async function doLogin() {
  const btn = document.getElementById('loginBtn');
  btn.disabled = true; btn.textContent = '登录中…';
  try {
    const d = await api('/api/login', 'POST', {username: 'admin', password: loginPass.value});
    TOKEN = d.token; localStorage.setItem('ss_token', TOKEN);
    loginErr.textContent = '';
    showApp(); loadAll();
  } catch(e) { loginErr.textContent = e.message; }
  btn.disabled = false; btn.textContent = '登录';
}
function doLogout() { localStorage.removeItem('ss_token'); TOKEN=''; api('/api/logout','POST').catch(()=>{}); showLogin(); }

function showPage(name) {
  ['tasks','storages','browse','plugins','stats','audit','settings'].forEach(p => {
    document.getElementById('page-'+p).style.display = p===name ? '' : 'none';
    document.getElementById('nav-'+p).classList.toggle('active', p===name);
  });
  // URL 反映当前页面，支持刷新后定位与分享
  if (location.hash !== '#/'+name) history.replaceState(null, '', '#/'+name);
  if (name==='tasks') loadTasks();
  if (name==='storages') loadStorages();
  if (name==='browse') loadBrowse();
  if (name==='plugins') loadPlugins();
  if (name==='stats') loadStats();
  if (name==='audit') loadAudit();
  if (name==='settings') loadSettings();
}

// ============ 运行历史（数据库持久化） ============
async function loadHistory() {
  try {
    const runs = await api('/api/runs?limit=50');
    const rows = document.getElementById('historyRows');
    if (!runs.length) { rows.innerHTML = '<tr><td colspan="11" class="empty">暂无运行记录</td></tr>'; return; }
    rows.innerHTML = runs.map(r => `<tr>
      <td class="num">${r.id}</td>
      <td>${esc(r.task)}</td>
      <td class="num">${fmtTime(r.start_at)}</td>
      <td class="num">${fmtDur(r.start_at, r.end_at)}</td>
      <td>${runStatusTag(r.status)}</td>
      <td class="num">${r.generated}</td>
      <td class="num">${r.copied}</td>
      <td class="num">${r.removed}</td>
      <td class="num">${r.skipped}</td>
      <td style="color:var(--red);font-size:12px;max-width:200px;word-break:break-all">${esc(r.error||'')}</td>
      <td><button class="btn gray" onclick="viewRunLog(${r.id},'${jsAttr(r.task)}')">日志</button></td></tr>`).join('');
  } catch(e) {}
}

function runStatusTag(s) {
  if (s === 'running') return '<span class="tag on">运行中</span>';
  if (s === 'success') return '<span class="tag on">成功</span>';
  if (s === 'stopped') return '<span class="tag off">已停止</span>';
  return '<span class="tag off">失败</span>';
}

function fmtDur(start, end) {
  if (!start || !end) return '-';
  const ms = new Date(end) - new Date(start);
  if (isNaN(ms) || ms < 0) return '-';
  if (ms < 1000) return ms + 'ms';
  return (ms/1000).toFixed(1) + 's';
}

// 查看某次运行的完整日志（数据库）
async function viewRunLog(runId, task) {
  LOG_TASK = null; LOG_POLLING = true; // 停止实时轮询，展示历史日志
  document.getElementById('logTaskName').textContent = task + ' #' + runId;
  const box = document.getElementById('taskLog');
  box.textContent = '加载中…';
  try {
    const lines = await api('/api/runs/'+runId+'/log');
    box.textContent = lines.map(l => fmtTime(l.time) + ' ' + levelTag(l.level) + l.msg).join('\n');
    box.scrollTop = box.scrollHeight;
  } catch(e) { box.textContent = '加载失败: ' + e.message; }
}

function levelTag(l) {
  return l === 'ERROR' ? '[ERR] ' : l === 'WARN' ? '[WARN] ' : l === 'DEBUG' ? '[DBG] ' : '[INFO] ';
}

// ============ 存储浏览 ============
let BROWSE = {storage:'', path:'/', pick:null}; // pick: 任务编辑回填模式

function gotoBrowse(storage, path) {
  BROWSE.storage = storage;
  BROWSE.path = path || '/';
  BROWSE.pick = null;
  document.getElementById('browsePickBtn').style.display = 'none';
  showPage('browse');
}

// 任务编辑中点击「浏览…」：选择路径后回填
function browseForTask() {
  BROWSE.storage = document.getElementById('t-storage').value;
  BROWSE.path = '/';
  BROWSE.pick = 'task';
  document.getElementById('browsePickBtn').style.display = '';
  showPage('browse');
}

function browsePickPath() {
  if (!BROWSE.pick) return;
  document.getElementById('t-path').value = BROWSE.path;
  BROWSE.pick = null;
  document.getElementById('browsePickBtn').style.display = 'none';
  // 任务编辑对话框保持打开，直接返回任务页
  showPage('tasks');
}

function browseChangeStorage() {
  BROWSE.storage = document.getElementById('browseStorage').value;
  BROWSE.path = '/';
  loadBrowse();
}

async function loadBrowse() {
  if (!STORAGES.length) STORAGES = await api('/api/storages');
  const sel = document.getElementById('browseStorage');
  const needFill = !sel.options.length || (BROWSE.storage && sel.value !== BROWSE.storage);
  if (needFill) {
    sel.innerHTML = STORAGES.map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
    sel.value = BROWSE.storage || STORAGES[0]?.name || '';
  }
  if (!BROWSE.storage) BROWSE.storage = sel.value;
  if (!BROWSE.storage) { document.getElementById('browseRows').innerHTML = '<tr><td colspan="3" class="empty">暂无存储，请先在「存储管理」中添加</td></tr>'; return; }
  document.getElementById('browseTitle').textContent = '存储浏览 - ' + BROWSE.storage + (BROWSE.pick ? '（选择扫描路径）' : '');
  document.getElementById('browsePath').textContent = '当前路径: ' + BROWSE.path;
  try {
    const files = await api('/api/storages/'+encodeURIComponent(BROWSE.storage)+'/list?path='+encodeURIComponent(BROWSE.path));
    const rows = document.getElementById('browseRows');
    const crumbs = [];
    if (BROWSE.path !== '/') {
      crumbs.push(`<tr><td colspan="3"><a style="cursor:pointer;color:var(--primary)" onclick="browseNav('..')">..</a></td></tr>`);
    }
    if (!files.length && !crumbs.length) {
      rows.innerHTML = '<tr><td colspan="3" class="empty">空目录</td></tr>';
    } else {
      rows.innerHTML = crumbs.concat(files.map(f => `<tr>
        <td>${f.is_dir ? '<span style="color:var(--primary)">📁</span> ' : ''}<a style="cursor:pointer;${f.is_dir?'color:var(--primary)':''}" onclick="${f.is_dir ? `browseNav('${jsAttr(f.name)}')` : 'void(0)'}">${esc(f.name)}</a></td>
        <td class="num">${f.is_dir ? '-' : fmtSize(f.size)}</td>
        <td class="num">${f.modified ? String(f.modified).slice(0,19).replace('T',' ') : '-'}</td></tr>`).join('')).join('');
    }
  } catch(e) {
    document.getElementById('browseRows').innerHTML = `<tr><td colspan="3" class="empty">${esc(e.message)}</td></tr>`;
  }
}

function browseNav(name) {
  if (name === '..') {
    BROWSE.path = BROWSE.path === '/' ? '/' : BROWSE.path.slice(0, BROWSE.path.lastIndexOf('/')) || '/';
  } else {
    BROWSE.path = (BROWSE.path === '/' ? '' : BROWSE.path) + '/' + name;
  }
  loadBrowse();
}

function fmtSize(n) {
  if (!n && n !== 0) return '-';
  if (n < 1024) return n + ' B';
  const units = ['KB','MB','GB','TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length-1);
  return n.toFixed(1) + ' ' + units[i];
}

function openDialog(id) {
  const dlg = document.getElementById(id);
  dlg.classList.add('show');
  dlg._lastFocus = document.activeElement;
  // 焦点圈定在对话框内
  dlg.querySelector('input,select,button').focus();
  dlg.addEventListener('keydown', trapKey);
}
function closeDialog(id) {
  const dlg = document.getElementById(id);
  if (!dlg) return;
  dlg.classList.remove('show');
  dlg.removeEventListener('keydown', trapKey);
  if (dlg._lastFocus) dlg._lastFocus.focus();
}
function trapKey(e) {
  if (e.key === 'Escape') { closeDialog(e.currentTarget.id); return; }
  if (e.key !== 'Tab') return;
  const focusables = e.currentTarget.querySelectorAll('button,input,select,[tabindex]:not([tabindex="-1"])');
  if (!focusables.length) return;
  const first = focusables[0], last = focusables[focusables.length-1];
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
}
// 点击遮罩关闭
document.querySelectorAll('.dialog').forEach(d => d.addEventListener('click', e => { if (e.target === d) closeDialog(d.id); }));

// ============ 任务 ============
async function loadTasks() {
  TASKS = await api('/api/tasks');
  const rows = document.getElementById('taskRows');
  if (!TASKS.length) {
    rows.innerHTML = '<tr><td colspan="7" class="empty">暂无任务，点击右上角「添加任务」创建第一个 STRM 生成任务</td></tr>';
    loadStatus();
    return;
  }
  rows.innerHTML = TASKS.map(t => `<tr>
    <td>${esc(t.name)} ${t.running ? '<span class="tag on">运行中</span>' : ''}</td>
    <td><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="gotoBrowse('${jsAttr(t.storage)}','')">${esc(t.storage)}</a></td>
    <td class="mono"><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="gotoBrowse('${jsAttr(t.storage)}','${jsAttr(t.storage_path)}')">${esc(t.storage_path)}</a></td>
    <td class="mono">${esc(t.crontab || '未设置')}</td>
    <td class="num">${fmtTime(t.next_run)}</td>
    <td><span class="status-dot ${t.running?'run':''}" aria-hidden="true"></span></td>
    <td>
      <button class="btn green" onclick="runTask('${jsAttr(t.name)}')">运行</button>
      ${t.running ? `<button class="btn red" onclick="stopTask('${jsAttr(t.name)}')">停止</button>` : ''}
      <button class="btn" onclick="openTask('${jsAttr(t.name)}')">编辑</button>
      <button class="btn amber" onclick="strmReplace('${jsAttr(t.name)}')">替换</button>
      <button class="btn gray" onclick="selectLog('${jsAttr(t.name)}')">日志</button>
      <button class="btn red" onclick="delTask('${jsAttr(t.name)}')">删除</button>
    </td></tr>`).join('');
  loadStatus();
  loadHistory();
}

async function loadStatus() {
  try {
    const st = await api('/api/tasks/status');
    const rows = document.getElementById('statusRows');
    const entries = Object.entries(st);
    if (!entries.length) { rows.innerHTML = '<tr><td colspan="9" class="empty">暂无运行记录</td></tr>'; return; }
    // 自动选中第一个运行中的任务查看日志
    if (!LOG_TASK) {
      const run = entries.find(([,s]) => s.running);
      if (run) { selectLog(run[0]); }
    }
    rows.innerHTML = entries.map(([name,s]) => `<tr>
      <td><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="selectLog('${jsAttr(name)}')">${esc(name)}</a></td>
      <td>${s.running ? '<span class="tag on">运行中</span>' : (s.error ? '<span class="tag off">失败</span>' : '<span class="tag on">完成</span>')}</td>
      <td class="num">${fmtTime(s.start)}</td>
      <td class="num">${fmtTime(s.end)}</td>
      <td class="num">${s.result ? s.result.generated : '-'}</td>
      <td class="num">${s.result ? s.result.copied : '-'}</td>
      <td class="num">${s.result ? s.result.removed : '-'}</td>
      <td class="num">${s.result ? s.result.skipped : '-'}</td>
      <td style="color:var(--red);font-size:12px;max-width:300px;word-break:break-all">${esc(s.error||'')}</td></tr>`).join('');
  } catch(e) {}
}

// ============ 任务日志（增量轮询） ============
let LOG_TASK = null, LOG_AFTER = 0, LOG_POLLING = false;

function selectLog(name) {
  LOG_TASK = name; LOG_AFTER = 0;
  document.getElementById('logTaskName').textContent = name;
  document.getElementById('taskLog').textContent = '';
  fetchLog();
  if (!LOG_POLLING) startLogPoll();
}
async function fetchLog() {
  if (!LOG_TASK) return;
  try {
    const d = await api('/api/tasks/'+encodeURIComponent(LOG_TASK)+'/log?after='+LOG_AFTER);
    if (d.lines && d.lines.length) {
      const box = document.getElementById('taskLog');
      const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      box.textContent += d.lines.map(l => {
        const t = fmtTime(l.time) + ' ';
        const tag = l.level === 'ERROR' ? '[ERR] ' : l.level === 'WARN' ? '[WARN] ' : l.level === 'DEBUG' ? '[DBG] ' : '[INFO] ';
        return t + tag + l.msg;
      }).join('\n') + '\n';
      if (nearBottom) box.scrollTop = box.scrollHeight;
    }
    LOG_AFTER = d.after || LOG_AFTER;
  } catch(e) {}
}
function startLogPoll() {
  LOG_POLLING = true;
  setInterval(fetchLog, 2000);
}

async function runTask(name) { try { await api('/api/tasks/'+encodeURIComponent(name)+'/run','POST'); loadTasks(); } catch(e){alert(e.message);} }
async function stopTask(name) { if(!confirm('确认停止任务 '+name+'？')) return; try { await api('/api/tasks/'+encodeURIComponent(name)+'/stop','POST'); loadTasks(); } catch(e){alert(e.message);} }
async function runAll() { try { await api('/api/tasks/run_all','POST'); loadTasks(); } catch(e){alert(e.message);} }
async function delTask(name) { if(!confirm('确认删除任务 '+name+'？')) return; try { await api('/api/tasks/'+encodeURIComponent(name),'DELETE'); loadTasks(); } catch(e){alert(e.message);} }

async function strmReplace(name) {
  const find = prompt('查找文本（支持正则，例如 alist\\.hollc\\.cn）');
  if (find === null) return;
  const replace = prompt('替换为');
  if (replace === null) return;
  const useRegex = confirm('使用正则模式？\n确定=正则  取消=纯文本');
  try { const d = await api('/api/tasks/'+encodeURIComponent(name)+'/strm_replace','POST',{find_text:find,replace_text:replace,regex_mode:useRegex}); alert('已替换 '+d.count+' 个文件'); } catch(e){alert(e.message);}
}

function openTask(name) {
  const st = document.getElementById('t-storage');
  st.innerHTML = STORAGES.map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
  const t = name ? TASKS.find(x => x.name === name) : null;
  document.getElementById('taskDialogTitle').textContent = t ? '编辑任务' : '添加任务';
  document.getElementById('t-name').value = t ? t.name : '';
  st.value = t ? t.storage : '';
  document.getElementById('t-path').value = t ? t.storage_path : '';
  document.getElementById('t-cron').value = t ? (t.crontab||'') : '';
  document.getElementById('t-incremental').checked = t ? t.incremental : true;
  document.getElementById('t-dircheck').checked = t ? t.dir_time_check : true;
  document.getElementById('t-keepAsset').checked = t ? !!t.keep_local_asset : false;
  // 任务级插件配置
  const pl = document.getElementById('t-plugins');
  pl.innerHTML = PLUGINS.map(p => {
    const tc = (t && t.plugins && t.plugins[p.id]) || {};
    const chk = tc.enabled !== undefined ? tc.enabled : false;
    return `<div class="check-row"><input type="checkbox" id="tp-${p.id}" ${chk?'checked':''}><label for="tp-${p.id}">${esc(p.name)}</label></div>`;
  }).join('');
  openDialog('taskDialog');
  window._editTask = t ? t.name : null;
}

async function saveTask() {
  const plugins = {};
  PLUGINS.forEach(p => {
    const el = document.getElementById('tp-'+p.id);
    if (el && el.checked) plugins[p.id] = {enabled:true};
  });
  const body = {
    name: document.getElementById('t-name').value.trim(),
    storage: document.getElementById('t-storage').value,
    storage_path: document.getElementById('t-path').value.trim(),
    crontab: document.getElementById('t-cron').value.trim(),
    incremental: document.getElementById('t-incremental').checked,
    dir_time_check: document.getElementById('t-dircheck').checked,
    keep_local_asset: document.getElementById('t-keepAsset').checked,
    plugins
  };
  const btn = document.getElementById('saveTaskBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    if (window._editTask) { await api('/api/tasks/'+encodeURIComponent(window._editTask),'PUT',body); }
    else { await api('/api/tasks','POST',body); }
    closeDialog('taskDialog'); loadTasks();
  } catch(e){ alert(e.message); }
  btn.disabled = false; btn.textContent = '保存';
}

// ============ 存储 ============
async function loadStorages() {
  STORAGES = await api('/api/storages');
  const rows = document.getElementById('storageRows');
  if (!STORAGES.length) {
    rows.innerHTML = '<tr><td colspan="4" class="empty">暂无存储，点击右上角「添加存储」接入 OpenList/AList</td></tr>';
    return;
  }
  rows.innerHTML = STORAGES.map(s => `<tr>
    <td>${esc(s.name)}</td><td>${esc(s.driver)}</td>
    <td class="mono">${esc(s.url)}</td>
    <td><button class="btn" onclick="openStorage('${jsAttr(s.name)}')">编辑</button>
        <button class="btn red" onclick="delStorage('${jsAttr(s.name)}')">删除</button></td></tr>`).join('');
}
function openStorage(name) {
  const s = name ? STORAGES.find(x => x.name === name) : null;
  document.getElementById('storageDialogTitle').textContent = s ? '编辑存储' : '存储配置';
  document.getElementById('st-name').value = s ? s.name : '';
  document.getElementById('st-driver').value = s ? s.driver : 'openlist';
  document.getElementById('st-url').value = s ? s.url : '';
  document.getElementById('st-token').value = s ? s.token : '';
  openDialog('storageDialog');
  window._editStorage = s ? s.name : null;
}
async function saveStorage() {
  const body = {name:st_name.value.trim(), driver:'openlist', url:st_url.value.trim(), token:st_token.value.trim()};
  if (!body.name || !body.url) { alert('名称与地址不能为空'); return; }
  const btn = document.getElementById('saveStorageBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    if (window._editStorage) await api('/api/storages/'+encodeURIComponent(window._editStorage),'PUT',body);
    else await api('/api/storages','POST',body);
    closeDialog('storageDialog'); loadStorages();
  } catch(e){ alert(e.message); }
  btn.disabled = false; btn.textContent = '保存';
}
async function delStorage(name) { if(!confirm('确认删除存储 '+name+'？')) return; try { await api('/api/storages/'+encodeURIComponent(name),'DELETE'); loadStorages(); } catch(e){alert(e.message);} }

// ============ 插件 ============
async function loadPlugins() {
  PLUGINS = await api('/api/plugins');
  const list = document.getElementById('pluginList');
  const forms = {
    content_replace: `查找文本<input type="text" id="p-content_replace-find"><div class="tip">生成时替换 STRM 内文本</div>替换文本<input type="text" id="p-content_replace-replace">`,
    custom_strm_name: `自定义文件名（变量 {name} {ext}，如 {name}.strm）<input type="text" id="p-custom_strm_name-name">`,
    scan_filter: `媒体文件后缀（留空用全局）<input type="text" id="p-scan_filter-media_ext"><div class="tip">逗号分隔</div>复制文件后缀<input type="text" id="p-scan_filter-copy_ext">`,
    skip_keyword: `关键词（逗号分隔，支持正则）<input type="text" id="p-skip_keyword-keywords">`,
    task_delay: `目录列出后延时(ms)<input type="number" id="p-task_delay-list"><div class="tip">降低风控</div>文件复制后延时(ms)<input type="number" id="p-task_delay-copy">`
  };
  list.innerHTML = PLUGINS.map(p => `
    <div class="plugin-card">
      <h4>${esc(p.name)} <span class="tag ${p.enabled?'on':'off'}">${p.enabled?'已启用':'未启用'}</span>
        <button class="btn" style="margin-left:auto" onclick="editPlugin('${jsAttr(p.id)}')">设置</button></h4>
      <div class="desc">${esc(p.desc || '')}</div>
      <div id="pf-${p.id}" style="display:none">
        ${forms[p.id] || ''}
        <div style="margin-top:8px"><button class="btn green" id="savePluginBtn-${p.id}" onclick="savePlugin('${jsAttr(p.id)}')">保存</button></div>
      </div>
    </div>`).join('');
}
function editPlugin(id) {
  const cfg = PLUGINS.find(p => p.id === id).config || {};
  const el = document.getElementById('pf-'+id);
  el.style.display = el.style.display === 'none' ? '' : 'none';
  const map = {['p-'+id+'-find']:cfg.find_text, ['p-'+id+'-replace']:cfg.replace_text, ['p-'+id+'-name']:cfg.custom_name,
    ['p-'+id+'-media_ext']:cfg.media_ext, ['p-'+id+'-copy_ext']:cfg.copy_ext, ['p-'+id+'-keywords']:cfg.keywords,
    ['p-'+id+'-list']:cfg.list_delay, ['p-'+id+'-copy']:cfg.copy_delay};
  Object.entries(map).forEach(([k,v]) => { if (v!==undefined) { const e = document.getElementById(k); if (e) e.value = v; } });
}
async function savePlugin(id) {
  const enabled = confirm('启用该插件？\n确定=启用  取消=停用');
  const cfg = {enabled};
  const read = (sid) => { const e = document.getElementById('p-'+id+'-'+sid); return e ? e.value.trim() : ''; };
  if (id==='content_replace') { cfg.find_text=read('find'); cfg.replace_text=read('replace'); }
  if (id==='custom_strm_name') { cfg.custom_name=read('name') || '{name}.({ext}).strm'; }
  if (id==='scan_filter') { cfg.media_ext=read('media_ext'); cfg.copy_ext=read('copy_ext'); }
  if (id==='skip_keyword') { cfg.keywords=read('keywords'); }
  if (id==='task_delay') { cfg.list_delay=parseInt(read('list')||'200'); cfg.copy_delay=parseInt(read('copy')||'200'); }
  const btn = document.getElementById('savePluginBtn-'+id);
  if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
  try { await api('/api/plugins/'+id,'PUT',cfg); loadPlugins(); } catch(e){alert(e.message);}
  if (btn) { btn.disabled = false; btn.textContent = '保存'; }
}

// ============ 统计报表 ============
const ACTION_NAMES = {
  login:'登录', login_failed:'登录失败', logout:'退出登录',
  task_create:'创建任务', task_update:'修改任务', task_delete:'删除任务',
  task_run:'运行任务', task_stop:'停止任务', task_run_all:'运行所有',
  storage_create:'创建存储', storage_update:'修改存储', storage_delete:'删除存储',
  settings_update:'修改设置', plugin_update:'修改插件', strm_replace:'替换STRM内容',
  task_trigger:'Webhook触发', emby_delete:'Emby删除同步', emby_delete_failed:'Emby删除失败'
};

async function loadStats() {
  try {
    const days = document.getElementById('statsDays').value;
    const daily = await api('/api/stats/daily?days='+days);
    const chart = document.getElementById('trendChart');
    if (!daily.length) { chart.innerHTML = '<div class="empty" style="width:100%">暂无数据，运行任务后生成统计</div>'; }
    else {
      const maxRun = Math.max(...daily.map(d => d.runs), 1);
      const maxGen = Math.max(...daily.map(d => d.generated), 1);
      chart.innerHTML = daily.map(d => {
        const hRun = Math.round(d.runs / maxRun * 100), hGen = Math.round(d.generated / maxGen * 100);
        return `<div style="flex:1;display:flex;flex-direction:column;align-items:center;gap:2px;height:100%;justify-content:flex-end" title="${esc(d.day)}: ${d.runs}次/${d.generated}个">
          <div style="width:60%;background:var(--primary);height:${hRun}%;border-radius:2px 2px 0 0;opacity:.85"></div>
          <div style="width:60%;background:var(--green);height:${hGen}%;border-radius:2px 2px 0 0;opacity:.7"></div>
          <div style="font-size:10px;color:var(--muted-2);white-space:nowrap">${esc(d.day.slice(5))}</div>
        </div>`;
      }).join('');
    }
    const totals = await api('/api/stats/tasks');
    const rows = document.getElementById('taskTotalsRows');
    if (!totals.length) { rows.innerHTML = '<tr><td colspan="6" class="empty">暂无数据</td></tr>'; }
    else {
      rows.innerHTML = totals.map(t => `<tr>
        <td>${esc(t.task)}</td>
        <td class="num">${t.runs}</td>
        <td class="num">${t.success}</td>
        <td class="num">${t.runs - t.success}</td>
        <td class="num">${t.runs ? Math.round(t.success/t.runs*100) : 0}%</td>
        <td class="num">${t.generated}</td></tr>`).join('');
    }
  } catch(e) {}
}

// ============ 审计日志 ============
async function loadAudit() {
  try {
    const list = await api('/api/audit?limit=200');
    const rows = document.getElementById('auditRows');
    if (!list.length) { rows.innerHTML = '<tr><td colspan="5" class="empty">暂无审计记录</td></tr>'; return; }
    rows.innerHTML = list.map(a => `<tr>
      <td class="num">${fmtTime(a.time)}</td>
      <td>${esc(a.user || '-')}</td>
      <td>${esc(ACTION_NAMES[a.action] || a.action)}</td>
      <td>${esc(a.target || '')}</td>
      <td style="font-size:12px;max-width:300px;word-break:break-all;color:var(--muted)">${esc(a.detail || '')}</td></tr>`).join('');
  } catch(e) {}
}

// ============ 设置 ============
async function loadSettings() {
  try {
    const s = await api('/api/settings');
    s_mediaExt.value = s.strm.media_ext.join(',');
    s_mediaSize.value = s.strm.media_size;
    s_copyExt.value = s.strm.copy_ext.join(',');
    s_saveDir.value = s.strm.save_dir;
    s_strmBase.value = s.strm.strm_base || '';
    s_urlEncode.checked = s.strm.url_encode;
    s_genType.value = s.strm.gen_type || 'path';
    const wh = await api('/api/webhook/info');
    document.getElementById('webhookInfo').innerHTML =
      `<div class="form-row"><label>任务触发 Webhook 地址（QAS/CloudSaver 转存后推送）</label><div class="mono">${esc(wh.trigger)}</div></div>
       <div class="form-row"><label>Emby 删除同步地址（Emby 通知→删除远端文件）</label><div class="mono">${esc(wh.emby)}</div></div>
       <div class="form-row"><label>POST JSON：</label><div class="mono">{&quot;strmtask&quot;: &quot;fc2,av&quot;}</div></div>`;
    // Emby 删除同步配置
    const es = wh.emby_sync || {};
    wh_embyEnabled.checked = !!es.enabled;
    wh_strmInEmby.value = es.strm_in_emby || '';
    wh_pathMap.value = es.storage_path_map || '';
    wh_allowed.value = (es.allowed_prefix || []).join(',');
    const sel = document.getElementById('wh-storage');
    sel.innerHTML = STORAGES.map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
    sel.value = es.storage || '';
  } catch(e){}
}

async function saveWebhook() {
  const btn = document.getElementById('saveWhBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    await api('/api/webhook', 'PUT', {
      emby_delete_sync: {
        enabled: wh_embyEnabled.checked,
        strm_in_emby: wh_strmInEmby.value.trim(),
        storage_path_map: wh_pathMap.value.trim(),
        storage: wh_storage.value,
        allowed_prefix: wh_allowed.value.split(',').map(s=>s.trim()).filter(Boolean)
      }
    });
    alert('已保存');
  } catch(e){ alert(e.message); }
  btn.disabled = false; btn.textContent = '保存 Webhook 配置';
}

async function regenerateWebhook() {
  if (!confirm('重新生成后旧地址立即失效，需同步更新 Emby/QAS/CloudSaver 中的 Webhook 配置。确认继续？')) return;
  try {
    const d = await api('/api/webhook/regenerate', 'POST');
    alert('新地址已生成:\n触发: ' + d.trigger + '\nEmby: ' + d.emby);
    loadSettings();
  } catch(e){ alert(e.message); }
}

async function changePassword() {
  const oldPwd = document.getElementById('cp-old').value;
  const newPwd = document.getElementById('cp-new').value;
  const confirmPwd = document.getElementById('cp-confirm').value;
  const msg = document.getElementById('cpMsg');
  msg.style.color = 'var(--red)';
  if (!oldPwd || !newPwd) { msg.textContent = '请填写旧密码与新密码'; return; }
  if (newPwd.length < 6) { msg.textContent = '新密码至少 6 位'; return; }
  if (newPwd !== confirmPwd) { msg.textContent = '两次输入的新密码不一致'; return; }
  const btn = document.getElementById('cpBtn');
  btn.disabled = true; btn.textContent = '修改中…';
  try {
    await api('/api/password', 'POST', {old_password: oldPwd, new_password: newPwd});
    msg.style.color = 'var(--green)';
    msg.textContent = '密码已修改，请使用新密码重新登录';
    document.getElementById('cp-old').value = '';
    document.getElementById('cp-new').value = '';
    document.getElementById('cp-confirm').value = '';
  } catch(e) { msg.textContent = e.message; }
  btn.disabled = false; btn.textContent = '修改密码';
}
async function saveSettings() {
  const btn = document.getElementById('saveSettingsBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    await api('/api/settings','PUT',{
      strm: {
        media_ext: s_mediaExt.value.split(',').map(s=>s.trim()).filter(Boolean),
        media_size: parseInt(s_mediaSize.value)||0,
        copy_ext: s_copyExt.value.split(',').map(s=>s.trim()).filter(Boolean),
        save_dir: s_saveDir.value.trim(),
        url_encode: s_urlEncode.checked,
        gen_type: s_genType.value,
        strm_base: s_strmBase.value.trim()
      }
    });
    alert('已保存');
  } catch(e){ alert(e.message); }
  btn.disabled = false; btn.textContent = '保存设置';
}

// ============ 工具 ============
// esc: HTML 文本上下文转义
function esc(s) { return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }
// jsAttr: 用于 onclick="fn('...')" 内嵌参数的 JS 字符串转义
function jsAttr(s) { return String(s||'').replace(/\\/g,'\\\\').replace(/'/g,"\\'").replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }

async function loadAll() {
  await Promise.all([loadTasks(), loadStorages(), loadPlugins()]);
  setInterval(() => { if (document.getElementById('page-tasks').style.display !== 'none') loadStatus(); }, 5000);
}

// 初始化：hash 定位页面
(function init() {
  if (TOKEN) {
    api('/api/settings').then(() => { showApp(); loadAll(); }).catch(() => showLogin());
  } else { showLogin(); }
  const m = location.hash.match(/^#\/(tasks|storages|browse|plugins|stats|audit|settings)$/);
  if (m) showPage(m[1]);
})();
