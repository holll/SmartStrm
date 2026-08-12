
let TOKEN = localStorage.getItem('ss_token') || '';
let TASKS = [], STORAGES = [], PLUGINS = [];

const dtf = new Intl.DateTimeFormat('zh-CN', {year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false});
function fmtTime(iso) {
  if (!iso) return '-';
  const d = new Date(iso);
  // Go 零值时间（0001 年）与无效时间显示 '-'
  if (isNaN(d) || d.getFullYear() < 2000) return '-';
  return dtf.format(d);
}
// 相对时间：如 "3 分钟后" / "2 小时后"，超过 3 天回退为完整时间
function fmtRel(iso) {
  if (!iso) return '-';
  const ms = new Date(iso) - Date.now();
  if (isNaN(ms)) return '-';
  const m = Math.round(ms / 60000);
  if (m <= 0) return '已到期';
  if (m < 60) return m + ' 分钟后';
  const h = Math.round(m / 60);
  if (h < 24) return h + ' 小时后';
  const d = Math.round(h / 24);
  if (d <= 3) return d + ' 天后';
  return fmtTime(iso);
}
// 数字滚动动画：从当前值平滑滚到目标值
function animateNum(el, to, dur = 700) {
  if (!el || el.textContent === String(to)) return;
  const from = parseInt(el.dataset.val || '0', 10) || 0;
  el.dataset.val = to;
  el.classList.add('ticking');
  const start = performance.now();
  function step(now) {
    const p = Math.min((now - start) / dur, 1);
    const eased = 1 - Math.pow(1 - p, 3); // easeOutCubic
    el.textContent = Math.round(from + (to - from) * eased);
    if (p < 1) requestAnimationFrame(step);
    else el.classList.remove('ticking');
  }
  requestAnimationFrame(step);
}

async function api(path, method='GET', body) {
  const opt = {method, headers: {'Content-Type':'application/json'}};
  if (TOKEN) opt.headers['Authorization'] = 'Bearer ' + TOKEN;
  if (body !== undefined) opt.body = JSON.stringify(body);
  const r = await fetch(path, opt);
  // 登录接口的 401 是"账号或密码错误"，不能覆盖为"未登录"并重定向回登录页
  if (r.status === 401 && path !== '/api/login') { showLogin(); throw new Error('未登录'); }
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
    const d = await api('/api/login', 'POST', {username: 'admin', password: document.getElementById('loginPass').value});
    TOKEN = d.token; localStorage.setItem('ss_token', TOKEN);
    document.getElementById('loginErr').textContent = '';
    showApp(); loadAll();
  } catch(e) { document.getElementById('loginErr').textContent = e.message; }
  btn.disabled = false; btn.textContent = '登录';
}
function doLogout() { localStorage.removeItem('ss_token'); TOKEN=''; api('/api/logout','POST').catch(()=>{}); showLogin(); }

function showPage(name) {
  ['overview','tasks','storages','browse','plugins','audit','settings'].forEach(p => {
    document.getElementById('page-'+p).style.display = p===name ? '' : 'none';
    const navBtn = document.getElementById('nav-'+p);
    navBtn.classList.toggle('active', p===name);
    if (p===name) navBtn.setAttribute('aria-current', 'page');
    else navBtn.removeAttribute('aria-current');
  });
  closeSidebar(); // 移动端导航后收起侧边栏
  // URL 反映当前页面，支持刷新后定位与分享
  if (location.hash !== '#/'+name) history.replaceState(null, '', '#/'+name);
  // 页面进入动画：卡片交错浮现
  const page = document.getElementById('page-'+name);
  page.classList.remove('page-enter');
  void page.offsetWidth; // 强制 reflow 以重启动画
  page.querySelectorAll('.card, .stat-card').forEach((el, i) => {
    el.style.animationDelay = (i * 70) + 'ms';
  });
  page.classList.add('page-enter');
  if (name==='overview') loadOverview();
  if (name==='tasks') loadTasks();
  if (name==='storages') loadStorages();
  if (name==='browse') loadBrowse();
  if (name==='plugins') loadPlugins();
  if (name==='audit') loadAudit();
  if (name==='settings') loadSettings();
}

// ============ 运行历史（数据库持久化） ============
async function loadHistory() {
  try {
    const runs = (await api('/api/runs?limit=50')) || [];
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
      <td><button class="btn btn-outline-info" title="查看日志" onclick="viewRunLog(${r.id},'${jsAttr(r.task)}')"><i class="bi bi-journal-text"></i></button></td></tr>`).join('');
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
  LOG_TASK = null; LOG_POLLING = true; LOG_HISTORY = true; // 停止实时轮询，展示历史日志
  document.getElementById('logModalTitle').textContent = '任务日志 - ' + task + ' #' + runId;
  const box = document.getElementById('taskLog');
  box.textContent = '加载中…';
  openDialog('taskLogModal');
  try {
    const lines = (await api('/api/runs/'+runId+'/log')) || [];
    box.textContent = '';
    appendLogLines(box, lines.map(l => fmtTime(l.time) + ' ' + levelTag(l.level) + l.msg), false);
    box.scrollTop = box.scrollHeight;
  } catch(e) { box.textContent = '加载失败: ' + e.message; }
}

// 关闭日志弹窗时停止实时刷新
function closeLogModal() {
  LOG_TASK = null;
  LOG_POLLING = false;
  if (LOG_INTERVAL) { clearInterval(LOG_INTERVAL); LOG_INTERVAL = null; }
  closeDialog('taskLogModal');
}

// 以行为单位追加日志（每行淡入动画，增量场景启用）
function appendLogLines(box, lines, animated) {
  const frag = document.createDocumentFragment();
  lines.forEach((line, i) => {
    const el = document.createElement('div');
    el.className = 'log-line';
    if (!animated) el.style.animation = 'none';
    else el.style.animationDelay = Math.min(i * 40, 400) + 'ms';
    el.textContent = line;
    frag.appendChild(el);
  });
  box.appendChild(frag);
  // 防内存膨胀：只保留最近 2000 行
  while (box.children.length > 2000) box.removeChild(box.firstChild);
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
  closeDialog('taskDialog'); // 先关闭对话框，避免全屏遮罩遮挡浏览页
  showPage('browse');
}

function browsePickPath() {
  if (!BROWSE.pick) return;
  document.getElementById('t-path').value = BROWSE.path;
  BROWSE.pick = null;
  document.getElementById('browsePickBtn').style.display = 'none';
  // 返回任务页并重新打开编辑对话框（表单值保留在 DOM 中，无需重新填充）
  showPage('tasks');
  openDialog('taskDialog');
}

function browseChangeStorage() {
  BROWSE.storage = document.getElementById('browseStorage').value;
  BROWSE.path = '/';
  loadBrowse();
}

async function loadBrowse() {
  if (!STORAGES.length) STORAGES = (await api('/api/storages')) || [];
  const sel = document.getElementById('browseStorage');
  const needFill = !sel.options.length || (BROWSE.storage && sel.value !== BROWSE.storage);
  if (needFill) {
    sel.innerHTML = STORAGES.map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
    sel.value = BROWSE.storage || STORAGES[0]?.name || '';
  }
  // 路径输入框回车跳转、搜索框实时过滤（只绑定一次）
  const pathInput = document.getElementById('browsePathInput');
  if (pathInput && !pathInput._bound) {
    pathInput._bound = true;
    pathInput.addEventListener('keydown', e => { if (e.key === 'Enter') browseGoPath(); });
  }
  const filterInput = document.getElementById('browseFilter');
  if (filterInput && !filterInput._bound) {
    filterInput._bound = true;
    filterInput.addEventListener('input', () => { if (BROWSE.storage) loadBrowse(); });
  }
  if (!BROWSE.storage) BROWSE.storage = sel.value;
  if (!BROWSE.storage) { document.getElementById('browseRows').innerHTML = '<tr><td colspan="3" class="empty">暂无存储，请先在「存储管理」中添加</td></tr>'; return; }
  document.getElementById('browseTitle').textContent = '存储浏览 - ' + BROWSE.storage + (BROWSE.pick ? '（选择扫描路径）' : '');
  document.getElementById('browsePathInput').value = BROWSE.path;
  try {
    const files = (await api('/api/storages/'+encodeURIComponent(BROWSE.storage)+'/list?path='+encodeURIComponent(BROWSE.path))) || [];
    const rows = document.getElementById('browseRows');
    // 前端搜索过滤
    const filter = (document.getElementById('browseFilter').value || '').trim().toLowerCase();
    const shown = filter ? files.filter(f => f.name.toLowerCase().includes(filter)) : files;
    const crumbs = [];
    if (BROWSE.path !== '/') {
      crumbs.push(`<tr class="cursor-pointer" onclick="browseNav('..')"><td><i class="me-2 bi bi-folder-fill text-warning"></i>..</td><td></td><td></td></tr>`);
    }
    if (!shown.length && !crumbs.length) {
      rows.innerHTML = '<tr><td colspan="3" class="empty">空目录</td></tr>';
    } else {
      rows.innerHTML = crumbs.concat(shown.map(f => `<tr>
        <td class="cursor-pointer">${f.is_dir
          ? '<div class="d-flex align-items-center"><div class="file-icon me-2"><i class="bi bi-folder-fill text-warning"></i></div><span class="text-break" onclick="browseNav(\''+jsAttr(f.name)+'\')">'+esc(f.name)+'</span></div>'
          : '<div class="d-flex align-items-center"><div class="file-icon me-2"><i class="bi bi-file-earmark text-muted"></i></div><span class="text-break">'+esc(f.name)+'</span></div>'}</td>
        <td class="num">${f.is_dir ? '-' : fmtSize(f.size)}</td>
        <td class="num">${fmtBrowseTime(f.modified)}</td></tr>`).join('')).join('');
    }
    document.getElementById('browseCount').textContent = files.length + ' 项' + (filter ? ' | 筛选 ' + shown.length + ' 项' : '');
    document.getElementById('browseFilterClear').style.display = filter ? '' : 'none';
  } catch(e) {
    document.getElementById('browseRows').innerHTML = `<tr><td colspan="3" class="empty">${esc(e.message)}</td></tr>`;
  }
}

// 浏览搜索过滤（原版前端过滤）
function clearBrowseFilter() {
  document.getElementById('browseFilter').value = '';
  loadBrowse();
}

function browseNav(name) {
  if (name === '..') {
    BROWSE.path = BROWSE.path === '/' ? '/' : BROWSE.path.slice(0, BROWSE.path.lastIndexOf('/')) || '/';
  } else {
    BROWSE.path = (BROWSE.path === '/' ? '' : BROWSE.path) + '/' + name;
  }
  loadBrowse();
}

// 路径输入框回车跳转
function browseGoPath() {
  const v = document.getElementById('browsePathInput').value.trim();
  if (!v) return;
  BROWSE.path = v.startsWith('/') ? v : '/' + v;
  loadBrowse();
}

// 复制当前路径
function copyPath() {
  navigator.clipboard.writeText(BROWSE.path).then(() => toast('已复制路径: ' + BROWSE.path)).catch(() => {});
}

function fmtSize(n) {
  if (!n && n !== 0) return '-';
  if (n < 1024) return n + ' B';
  const units = ['KB','MB','GB','TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length-1);
  return n.toFixed(1) + ' ' + units[i];
}

// 浏览列表的修改时间（原版格式 Y/M/D H:M:S，Go 零值时间显示 '-'）
function fmtBrowseTime(iso) {
  if (!iso) return '-';
  const d = new Date(iso);
  if (isNaN(d) || d.getFullYear() < 2000) return '-';
  const p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}/${d.getMonth()+1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function openDialog(id) {
  const dlg = document.getElementById(id);
  dlg.classList.add('show');
  dlg._lastFocus = document.activeElement;
  // 焦点圈定在对话框内（跳过禁用与隐藏元素）
  const first = dlg.querySelector('input:not([disabled]),select:not([disabled]),button:not([disabled])');
  if (first) first.focus();
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

// ============ 总览 ============
async function loadOverview() {
  try {
    const [tasks, storages, plugins, runs] = await Promise.all([
      api('/api/tasks').catch(() => []),
      api('/api/storages').catch(() => []),
      api('/api/plugins').catch(() => []),
      api('/api/runs?limit=200').catch(() => [])
    ]);
    const t = tasks || [], s = storages || [], p = plugins || [], r = runs || [];
    // 统计卡片（数字滚动动画）
    const running = t.filter(x => x.running).length;
    const enabledPlugins = p.filter(x => x.enabled).length;
    // 今日生成：取 start_at 在今天的所有运行求和
    const today = new Date().toDateString();
    const todayGen = r.filter(x => x.start_at && new Date(x.start_at).toDateString() === today)
      .reduce((sum, x) => sum + (x.generated || 0), 0);
    const qs = id => document.querySelector(id);
    animateNum(qs('#ov-tasks .stat-value'), t.length);
    const taskSub = qs('#ov-tasks-sub');
    taskSub.textContent = running ? running + ' 个运行中' : (t.length ? '全部空闲' : '暂无任务');
    taskSub.style.color = running ? 'var(--green)' : 'var(--text-dim)';
    animateNum(qs('#ov-storages .stat-value'), s.length);
    qs('#ov-storages .stat-sub').textContent = '已接入 ' + s.length + ' 个存储';
    animateNum(document.getElementById('ov-plugins-a'), enabledPlugins);
    document.getElementById('ov-plugins-b').textContent = p.length;
    qs('#ov-plugins .stat-sub').textContent = p.length + ' 个内置插件';
    animateNum(qs('#ov-gen .stat-value'), todayGen);
    qs('#ov-gen .stat-sub').textContent = '近 24 小时生成 STRM';
    // 任务状态速览
    const status = (await api('/api/tasks/status').catch(() => ({}))) || {};
    const rows = document.getElementById('ovStatusRows');
    if (!t.length) {
      rows.innerHTML = '<tr><td colspan="10" class="empty">暂无任务，点击右上角「添加任务」创建第一个 STRM 生成任务</td></tr>';
    } else {
      rows.innerHTML = t.map(x => {
        const st = status[x.name] || {};
        const isRunning = !!x.running;
        const err = st.error || '';
        return `<tr${isRunning ? ' class="row-running"' : ''}>
          <td>${isRunning ? '<span class="status-dot run" aria-hidden="true"></span>' : ''}<a style="cursor:pointer" onclick="selectLog('${jsAttr(x.name)}')">${esc(x.name)}</a></td>
          <td>${isRunning ? '<span class="tag on run">运行中</span>' : (err ? '<span class="tag off">失败</span>' : (fmtTime(st.start) !== '-' ? '<span class="tag on">就绪</span>' : '<span class="tag pending">未运行</span>'))}</td>
          <td class="num">${fmtTime(st.start)}</td>
          <td class="num">${fmtTime(st.end)}</td>
          <td class="num">${st.result ? st.result.generated : '-'}</td>
          <td class="num">${st.result ? st.result.copied : '-'}</td>
          <td class="num">${st.result ? st.result.removed : '-'}</td>
          <td class="num">${st.result ? st.result.skipped : '-'}</td>
          <td class="num">${isRunning ? '<span class="tip" style="color:var(--amber)">进行中</span>' : fmtRel(x.next_run)}</td>
          <td style="color:var(--red);font-size:12px;max-width:200px;word-break:break-all">${esc(err)}</td></tr>`;
      }).join('');
    }
    // 最近运行（展示最近 8 条，今日生成统计用前 200 条）
    const recent = r.slice(0, 8);
    const runRows = document.getElementById('ovRunRows');
    if (!recent.length) {
      runRows.innerHTML = '<tr><td colspan="9" class="empty">暂无运行记录，运行任务后显示</td></tr>';
    } else {
      runRows.innerHTML = recent.map(x => `<tr>
        <td class="num">${x.id}</td>
        <td>${esc(x.task)}</td>
        <td class="num">${fmtTime(x.start_at)}</td>
        <td class="num">${fmtDur(x.start_at, x.end_at)}</td>
        <td>${runStatusTag(x.status)}</td>
        <td class="num">${x.generated}</td>
        <td class="num">${x.copied}</td>
        <td class="num">${x.removed}</td>
        <td class="num">${x.skipped}</td></tr>`).join('');
    }
  } catch (e) { /* 总览页为聚合视图，局部失败不影响整体 */ }
}

// ============ 任务 ============
async function loadTasks() {
  TASKS = (await api('/api/tasks')) || [];
  const rows = document.getElementById('taskRows');
  if (!TASKS.length) {
    if (!STORAGES.length) {
      rows.innerHTML = '<tr><td colspan="6" class="empty">还没有存储，<a class="text-decoration-none" onclick="showPage(\'storages\');openStorage()">先去添加 <i class="bi bi-arrow-right"></i></a></td></tr>';
    } else {
      rows.innerHTML = '<tr><td colspan="6" class="empty">还没有任务，<a class="text-decoration-none" onclick="openTask()">添加 <i class="bi bi-arrow-right"></i></a></td></tr>';
    }
    loadStatus();
    return;
  }
  rows.innerHTML = TASKS.map(t => `<tr${t.running ? ' class="row-running"' : ''}>
    <td>${esc(t.name)}</td>
    <td><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="gotoBrowse('${jsAttr(t.storage)}','')">${esc(t.storage)}</a></td>
    <td class="mono"><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="gotoBrowse('${jsAttr(t.storage)}','${jsAttr(t.storage_path)}')">${esc(t.storage_path)}</a></td>
    <td class="mono">${esc(t.crontab || '未设置')}</td>
    <td class="num">${t.running ? '<span class="tip" style="color:var(--amber)">进行中</span>' : fmtRel(t.next_run)}</td>
    <td><div class="btn-group-sm">
      ${t.running
        ? `<button class="btn btn-outline-danger" title="停止" onclick="stopTask('${jsAttr(t.name)}')"><i class="bi bi-stop-fill"></i></button>`
        : `<button class="btn btn-outline-primary" title="运行" onclick="runTask('${jsAttr(t.name)}')"><i class="bi bi-play-fill"></i></button>`}
      <button class="btn btn-outline-secondary" title="编辑" onclick="openTask('${jsAttr(t.name)}')"><i class="bi bi-pencil"></i></button>
      <button class="btn btn-outline-secondary" title="替换" onclick="strmReplace('${jsAttr(t.name)}')"><i class="bi bi-arrow-repeat"></i></button>
      <button class="btn btn-outline-info" title="查看日志" onclick="selectLog('${jsAttr(t.name)}')"><i class="bi bi-journal-text"></i></button>
      <button class="btn btn-outline-danger" title="删除" onclick="delTask('${jsAttr(t.name)}')"><i class="bi bi-trash"></i></button>
    </div></td></tr>`).join('');
  loadStatus();
  loadHistory();
}

async function loadStatus() {
  try {
    const st = (await api('/api/tasks/status')) || {};
    const rows = document.getElementById('statusRows');
    const entries = Object.entries(st);
    if (!entries.length) { rows.innerHTML = '<tr><td colspan="9" class="empty">暂无运行记录</td></tr>'; return; }
    rows.innerHTML = entries.map(([name,s]) => `<tr>
      <td><a style="cursor:pointer;color:var(--primary);text-decoration:none" onclick="selectLog('${jsAttr(name)}')">${esc(name)}</a></td>
      <td>${s.running ? '<span class="tag on run">运行中</span>' : (s.error ? '<span class="tag off">失败</span>' : (fmtTime(s.start) !== '-' ? '<span class="tag on">完成</span>' : '<span class="tag pending">未运行</span>'))}</td>
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
let LOG_TASK = null, LOG_AFTER = 0, LOG_POLLING = false, LOG_HISTORY = false, LOG_INTERVAL = null;

function selectLog(name) {
  LOG_TASK = name; LOG_AFTER = 0; LOG_HISTORY = false;
  document.getElementById('logModalTitle').textContent = '任务日志 - ' + name;
  document.getElementById('taskLog').textContent = '';
  // 运行中显示停止按钮（原版交互）
  const stopBtn = document.getElementById('logStopBtn');
  const running = (TASKS.find(x => x.name === name) || {}).running;
  if (stopBtn) stopBtn.style.display = running ? '' : 'none';
  openDialog('taskLogModal');
  fetchLog();
  if (!LOG_POLLING) startLogPoll();
}
async function fetchLog() {
  if (!LOG_TASK) return;
  try {
    const d = await api('/api/tasks/'+encodeURIComponent(LOG_TASK)+'/log?after='+LOG_AFTER);
    const box = document.getElementById('taskLog');
    // 缓冲被新运行重置（after 回退到 0）：清空旧显示，从新运行开始
    if (d.after < LOG_AFTER) {
      box.textContent = '';
      LOG_AFTER = 0;
    }
    if (d.lines && d.lines.length) {
      const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
      appendLogLines(box, d.lines.map(l => {
        const t = fmtTime(l.time) + ' ';
        const tag = l.level === 'ERROR' ? '[ERR] ' : l.level === 'WARN' ? '[WARN] ' : l.level === 'DEBUG' ? '[DBG] ' : '[INFO] ';
        return t + tag + l.msg;
      }), d.lines.length <= 50);
      if (nearBottom) box.scrollTop = box.scrollHeight;
    } else if (LOG_AFTER === 0 && !box.children.length) {
      box.textContent = '暂无日志输出';
    }
    LOG_AFTER = d.after || LOG_AFTER;
  } catch(e) {}
}
function startLogPoll() {
  LOG_POLLING = true;
  LOG_INTERVAL = setInterval(fetchLog, 2000);
}

async function runTask(name) { try { await api('/api/tasks/'+encodeURIComponent(name)+'/run','POST'); loadTasks(); } catch(e){toast(e.message, 'danger');} }
async function stopTask(name) {
  if(!confirm('确认停止任务 '+name+'？')) return;
  try { await api('/api/tasks/'+encodeURIComponent(name)+'/stop','POST'); }
  catch(e){ toast(e.message, 'danger'); }
  loadTasks(); // 无论成功与否都刷新，避免任务刚结束时的 409 导致残留"运行中"
  // 若停止的是日志弹窗正在查看的任务，关闭弹窗
  if (LOG_TASK === name && document.getElementById('taskLogModal').classList.contains('show')) {
    closeLogModal();
  }
}
async function runAll() { try { await api('/api/tasks/run_all','POST'); loadTasks(); } catch(e){toast(e.message, 'danger');} }
async function delTask(name) { if(!confirm('确认删除任务 '+name+'？')) return; try { await api('/api/tasks/'+encodeURIComponent(name),'DELETE'); loadTasks(); } catch(e){toast(e.message, 'danger');} }

async function strmReplace(name) {
  const find = prompt('查找文本（支持正则，例如 alist\\.hollc\\.cn）');
  if (find === null) return;
  const replace = prompt('替换为');
  if (replace === null) return;
  const useRegex = confirm('使用正则模式？\n确定=正则  取消=纯文本');
  try { const d = await api('/api/tasks/'+encodeURIComponent(name)+'/strm_replace','POST',{find_text:find,replace_text:replace,regex_mode:useRegex}); toast('已替换 '+d.count+' 个文件'); } catch(e){toast(e.message, 'danger');}
}

let TASK_SAVE_DIR = null; // 生成根目录缓存（任务编辑弹窗显示保存路径用）

// 更新任务编辑弹窗中的保存路径提示（兼容 Windows 反斜杠分隔符）
function updateTaskSavePath() {
  const name = document.getElementById('t-name').value.trim();
  const pathEl = document.getElementById('taskSavePath');
  if (!pathEl) return;
  if (!name) { pathEl.textContent = '-'; return; }
  const base = TASK_SAVE_DIR || '';
  if (!base) { pathEl.textContent = name; return; }
  // 按 base 使用的分隔符拼接（Windows 反斜杠 / 其余正斜杠）
  const sep = base.includes('\\') ? '\\' : '/';
  pathEl.textContent = base.replace(/[\\/]+$/, '') + sep + name;
}

function openTask(name) {
  const st = document.getElementById('t-storage');
  // 存储列表尚未加载时先加载（首次点击「添加任务」可能在 loadAll 完成之前）
  if (!STORAGES.length) {
    loadStorages().then(() => { if (STORAGES.length) openTask(name); });
  }
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
  // 任务级插件配置（独立弹窗中）
  const pl = document.getElementById('t-plugins');
  pl.innerHTML = PLUGINS.map(p => {
    const tc = (t && t.plugins && t.plugins[p.id]) || {};
    const chk = tc.enabled !== undefined ? tc.enabled : false;
    return `<div class="d-flex justify-content-between align-items-center border rounded p-2 mb-2">
      <div class="form-check form-switch">
        <input type="checkbox" class="form-check-input" id="tp-${p.id}" ${chk?'checked':''}>
        <label class="form-check-label" for="tp-${p.id}" title="${esc(p.desc || '')}">${esc(p.name)}</label>
      </div>
    </div>`;
  }).join('');
  // 保存路径提示（懒加载生成根目录，任务名变化时联动更新）
  if (!TASK_SAVE_DIR) {
    api('/api/settings').then(s => { TASK_SAVE_DIR = (s.strm && s.strm.save_dir) || ''; updateTaskSavePath(); }).catch(() => {});
  }
  const nameInput = document.getElementById('t-name');
  if (!nameInput._bound) {
    nameInput._bound = true;
    nameInput.addEventListener('input', updateTaskSavePath);
  }
  // 扫描路径自动补全：输入时列出子目录，存储切换时清空重载
  const pathInput = document.getElementById('t-path');
  if (!pathInput._bound) {
    pathInput._bound = true;
    pathInput.addEventListener('input', loadTaskPathDirs);
    pathInput.addEventListener('change', loadTaskPathDirs);
  }
  const storageSel = document.getElementById('t-storage');
  if (!storageSel._bound) {
    storageSel._bound = true;
    storageSel.addEventListener('change', () => { clearPathDirs(); loadTaskPathDirs(); });
  }
  loadTaskPathDirs();
  // 增量模式联动：说明文字切换 + 同步模式才显示"保留刮削"
  const inc = document.getElementById('t-incremental');
  const syncTip = () => {
    document.getElementById('t-incremental-tip').textContent = inc.checked
      ? '使用增量生成，仅新增文件，不清理远端存储不存在的文件'
      : '使用同步生成，文件跟随远端，清理远端已不存在的文件';
    document.getElementById('t-keepAssetWrap').style.display = inc.checked ? 'none' : '';
  };
  if (!inc._bound) { inc._bound = true; inc.addEventListener('change', syncTip); }
  syncTip();
  updateTaskSavePath();
  openDialog('taskDialog');
  window._editTask = t ? t.name : null;
}

// 打开任务工具 / 任务插件（独立弹窗）
function openTaskTools() { openDialog('taskToolsModal'); }
function openTaskPlugins() { openDialog('taskPluginModal'); }

// 扫描路径：上一级目录
function taskPathUp() {
  const input = document.getElementById('t-path');
  const v = input.value.trim();
  if (!v || v === '/') { input.value = '/'; return; }
  input.value = v.slice(0, v.lastIndexOf('/')) || '/';
  loadTaskPathDirs();
}

// 扫描路径 datalist 自动补全：列出当前路径下的子目录（仿原版）
let pathDirsTimer = null;
function loadTaskPathDirs() {
  const storage = document.getElementById('t-storage').value;
  const path = document.getElementById('t-path').value.trim();
  const dl = document.getElementById('storagePath');
  if (!storage || !path) { if (dl) dl.innerHTML = ''; return; }
  clearTimeout(pathDirsTimer);
  pathDirsTimer = setTimeout(async () => {
    try {
      const files = (await api('/api/storages/'+encodeURIComponent(storage)+'/list?path='+encodeURIComponent(path))) || [];
      const dirs = files.filter(f => f.is_dir)
        .map(f => (path === '/' ? '' : path) + '/' + f.name)
        .slice(0, 50);
      dl.innerHTML = dirs.map(d => `<option value="${esc(d)}">`).join('');
    } catch(e) { /* 路径不存在或加载失败，静默 */ }
  }, 300);
}
function clearPathDirs() {
  const dl = document.getElementById('storagePath');
  if (dl) dl.innerHTML = '';
}

// 随机生成半夜执行时间（0-5 点）
function randomCron() {
  const h = Math.floor(Math.random() * 6);
  const m = Math.floor(Math.random() * 60);
  document.getElementById('t-cron').value = `${m} ${h} * * *`;
}

function clearCron() { document.getElementById('t-cron').value = ''; }

// 任务工具：STRM 内容替换（对话框内）
async function taskReplaceAction() {
  const taskName = window._editTask || document.getElementById('t-name').value.trim();
  const find = document.getElementById('tt-find').value.trim();
  const replace = document.getElementById('tt-replace').value;
  if (!taskName) { toast('请先填写任务名称', 'warning'); return; }
  if (!find) { toast('请填写查找文本', 'warning'); return; }
  try {
    const d = await api('/api/tasks/'+encodeURIComponent(taskName)+'/strm_replace','POST',{find_text:find,replace_text:replace,regex_mode:true});
    toast('已替换 ' + d.count + ' 个文件');
  } catch(e){ toast(e.message, 'danger'); }
}

// 任务工具：全量覆写（清空任务目录后重新生成，仿原版）
async function taskOverwriteAction() {
  const taskName = window._editTask || document.getElementById('t-name').value.trim();
  if (!taskName) { toast('请先填写任务名称', 'warning'); return; }
  if (!confirm('全量覆写将清空任务 ' + taskName + ' 目录并强制重新生成所有文件，确认继续？')) return;
  try {
    await api('/api/tasks/'+encodeURIComponent(taskName)+'/overwrite','POST');
    toast('已开始全量覆写');
    closeTaskToolModal();
  } catch(e){ toast(e.message, 'danger'); }
}

// 任务工具：一键清除（删除任务目录下所有文件，仿原版）
async function taskClearAction() {
  const taskName = window._editTask || document.getElementById('t-name').value.trim();
  if (!taskName) { toast('请先填写任务名称', 'warning'); return; }
  if (!confirm('一键清除将删除任务 ' + taskName + ' 目录下的所有文件，此操作不可恢复！确认继续？')) return;
  try {
    await api('/api/tasks/'+encodeURIComponent(taskName)+'/clear','POST');
    toast('已清除任务目录');
    closeTaskToolModal();
  } catch(e){ toast(e.message, 'danger'); }
}

// 关闭工具/编辑弹窗并刷新任务列表，使运行中状态与停止按钮及时出现
function closeTaskToolModal() {
  closeDialog('taskToolsModal');
  closeDialog('taskDialog');
  loadTasks();
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
  } catch(e){ toast(e.message, 'danger'); }
  btn.disabled = false; btn.textContent = '保存';
}

// ============ 存储 ============
async function loadStorages() {
  STORAGES = (await api('/api/storages')) || [];
  const rows = document.getElementById('storageRows');
  if (!STORAGES.length) {
    rows.innerHTML = '<tr><td colspan="4" class="empty">暂无存储，点击右上角「添加存储」接入 OpenList/AList</td></tr>';
    return;
  }
  rows.innerHTML = STORAGES.map(s => `<tr>
    <td>${esc(s.name)}</td><td>${esc(s.driver)}</td>
    <td class="mono">${esc(s.url)}</td>
    <td><div class="btn-group-sm">
      <button class="btn btn-outline-secondary" title="编辑" onclick="openStorage('${jsAttr(s.name)}')"><i class="bi bi-pencil"></i></button>
      <button class="btn btn-outline-danger" title="删除" onclick="delStorage('${jsAttr(s.name)}')"><i class="bi bi-trash"></i></button>
    </div></td></tr>`).join('');
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
  const body = {name:document.getElementById('st-name').value.trim(), driver:'openlist', url:document.getElementById('st-url').value.trim(), token:document.getElementById('st-token').value.trim()};
  if (!body.name || !body.url) { toast('名称与地址不能为空', 'warning'); return; }
  const btn = document.getElementById('saveStorageBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    if (window._editStorage) await api('/api/storages/'+encodeURIComponent(window._editStorage),'PUT',body);
    else await api('/api/storages','POST',body);
    closeDialog('storageDialog'); loadStorages();
  } catch(e){ toast(e.message, 'danger'); }
  btn.disabled = false; btn.textContent = '保存';
}
async function delStorage(name) { if(!confirm('确认删除存储 '+name+'？')) return; try { await api('/api/storages/'+encodeURIComponent(name),'DELETE'); loadStorages(); } catch(e){toast(e.message, 'danger');} }

// ============ 插件 ============
async function loadPlugins() {
  PLUGINS = (await api('/api/plugins')) || [];
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
        <button class="btn btn-outline-secondary btn-sm" style="margin-left:auto" title="设置" onclick="editPlugin('${jsAttr(p.id)}')"><i class="bi bi-pencil"></i>设置</button></h4>
      <div class="desc">${esc(p.desc || '')}</div>
      <div id="pf-${p.id}" style="display:none">
        ${forms[p.id] || ''}
        <div style="margin-top:8px"><button class="btn btn-primary btn-sm" id="savePluginBtn-${p.id}" onclick="savePlugin('${jsAttr(p.id)}')"><i class="bi bi-check-lg"></i>保存</button></div>
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
  try { await api('/api/plugins/'+id,'PUT',cfg); loadPlugins(); } catch(e){toast(e.message, 'danger');}
  if (btn) { btn.disabled = false; btn.textContent = '保存'; }
}

// ============ 统计报表 ============
const ACTION_NAMES = {
  login:'登录', login_failed:'登录失败', logout:'退出登录',
  task_create:'创建任务', task_update:'修改任务', task_delete:'删除任务',
  task_run:'运行任务', task_stop:'停止任务', task_run_all:'运行所有',
  storage_create:'创建存储', storage_update:'修改存储', storage_delete:'删除存储',
  settings_update:'修改设置', plugin_update:'修改插件', strm_replace:'替换STRM内容',
  task_trigger:'Webhook触发', emby_delete:'Emby删除同步', emby_delete_failed:'Emby删除失败',
  task_overwrite:'全量覆写', task_clear:'一键清除'
};

// ============ 审计日志 ============
async function loadAudit() {
  try {
    const list = (await api('/api/audit?limit=200')) || [];
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
    document.getElementById('s-mediaExt').value = s.strm.media_ext.join(',');
    document.getElementById('s-mediaSize').value = s.strm.media_size;
    document.getElementById('s-copyExt').value = s.strm.copy_ext.join(',');
    document.getElementById('s-saveDir').value = s.strm.save_dir;
    document.getElementById('s-strmBase').value = s.strm.strm_base || '';
    document.getElementById('s-urlEncode').checked = s.strm.url_encode;
    document.getElementById('s-genType').value = s.strm.gen_type || 'path';
    syncGenTypeTip();
    const wh = await api('/api/webhook/info');
    document.getElementById('webhookInfo').innerHTML =
      `<input type="text" class="form-control" value="${esc(wh.trigger)}" readonly title="任务触发与 Emby 删除同步共用地址">
       <button class="btn btn-outline-dark" type="button" title="重置" onclick="regenerateWebhook()"><i class="bi bi-arrow-repeat"></i></button>
       <button class="btn btn-outline-dark border-start-0" type="button" title="复制" onclick="copyText('${esc(wh.trigger)}')"><i class="bi bi-clipboard"></i></button>`;
    // Emby 删除同步配置
    const es = wh.emby_sync || {};
    document.getElementById('wh-embyEnabled').checked = !!es.enabled;
    document.getElementById('wh-strmInEmby').value = es.strm_in_emby || '';
    document.getElementById('wh-pathMap').value = es.storage_path_map || '';
    document.getElementById('wh-allowed').value = (es.allowed_prefix || []).join(',');
    const sel = document.getElementById('wh-storage');
    sel.innerHTML = STORAGES.map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
    sel.value = es.storage || '';
  } catch(e){}
}

// 生成类型说明联动（原版：fid_mode 动态提示）
function syncGenTypeTip() {
  const tip = document.getElementById('s-genType-tip');
  if (!tip) return;
  const v = document.getElementById('s-genType').value;
  tip.textContent = v === 'fid'
    ? '以文件编号替代路径写入 STRM 内容，可稍微提高起播速度（推荐）'
    : '以文件路径写入 STRM 内容，兼容性好，但可能起播速度稍慢';
}

async function saveWebhook() {
  const btn = document.getElementById('saveWhBtn');
  btn.disabled = true; btn.textContent = '保存中…';
  try {
    await api('/api/webhook', 'PUT', {
      emby_delete_sync: {
        enabled: document.getElementById('wh-embyEnabled').checked,
        strm_in_emby: document.getElementById('wh-strmInEmby').value.trim(),
        storage_path_map: document.getElementById('wh-pathMap').value.trim(),
        storage: document.getElementById('wh-storage').value,
        allowed_prefix: document.getElementById('wh-allowed').value.split(',').map(s=>s.trim()).filter(Boolean)
      }
    });
    toast('已保存');
  } catch(e){ toast(e.message, 'danger'); }
  btn.disabled = false; btn.textContent = '保存 Webhook 配置';
}

async function regenerateWebhook() {
  if (!confirm('重新生成后旧地址立即失效，需同步更新 Emby/QAS/CloudSaver 中的 Webhook 配置。确认继续？')) return;
  try {
    await api('/api/webhook/regenerate', 'POST');
    toast('新地址已生成，旧地址立即失效');
    loadSettings();
  } catch(e){ toast(e.message, 'danger'); }
}

// 折叠面板切换（仿原版 collapse，图标按面板 id 对应）
function toggleCollapse(id) {
  const el = document.getElementById(id);
  if (!el) return;
  const show = el.classList.toggle('show');
  const chevron = document.getElementById(id.replace('Panel', 'Chevron')) || document.getElementById('wh-chevron');
  if (chevron) chevron.classList.toggle('bi-chevron-up', show);
}

// 复制文本到剪贴板
function copyText(text) {
  navigator.clipboard.writeText(text).then(() => toast('已复制')).catch(() => {});
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
        media_ext: document.getElementById('s-mediaExt').value.split(',').map(s=>s.trim()).filter(Boolean),
        media_size: parseInt(document.getElementById('s-mediaSize').value)||0,
        copy_ext: document.getElementById('s-copyExt').value.split(',').map(s=>s.trim()).filter(Boolean),
        save_dir: document.getElementById('s-saveDir').value.trim(),
        url_encode: document.getElementById('s-urlEncode').checked,
        gen_type: document.getElementById('s-genType').value,
        strm_base: document.getElementById('s-strmBase').value.trim()
      }
    });
    toast('已保存');
  } catch(e){ toast(e.message, 'danger'); }
  btn.disabled = false; btn.textContent = '保存设置';
}

// ============ 工具 ============
// esc: HTML 文本上下文转义
function esc(s) { return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }
// 移动端侧边栏开关
function toggleSidebar() { document.getElementById('app').classList.toggle('sidebar-open'); }
function closeSidebar() { document.getElementById('app').classList.remove('sidebar-open'); }
// toast: 顶部居中轻提示（仿原版：header 图标 + 标题 + body），type: success/danger/warning/info
const TOAST_META = {
  success: { icon: 'bi-check-circle-fill', title: '成功' },
  danger:  { icon: 'bi-x-circle-fill',     title: '错误' },
  warning: { icon: 'bi-exclamation-triangle-fill', title: '提示' },
  info:    { icon: 'bi-info-circle-fill',  title: '信息' }
};
function toast(msg, type = 'success') {
  const container = document.querySelector('.toast-container');
  if (!container) return;
  const meta = TOAST_META[type] || TOAST_META.info;
  const el = document.createElement('div');
  el.className = 'toast text-bg-' + type;
  el.setAttribute('role', 'alert');
  el.setAttribute('aria-live', 'assertive');
  el.setAttribute('aria-atomic', 'true');
  el.innerHTML =
    `<div class="toast-header text-bg-${type}"><i class="bi ${meta.icon} me-2"></i><strong class="me-auto">${meta.title}</strong><button type="button" class="btn-close btn-close-white" aria-label="关闭" onclick="this.closest('.toast').remove()"></button></div>
     <div class="toast-body" style="white-space:pre-line">${esc(msg)}</div>`;
  container.appendChild(el);
  el.classList.add('show');
  setTimeout(() => { el.classList.remove('show'); setTimeout(() => el.remove(), 300); }, 3500);
}
// jsAttr: 用于 onclick="fn('...')" 内嵌参数的 JS 字符串转义
function jsAttr(s) { return String(s||'').replace(/\\/g,'\\\\').replace(/'/g,"\\'").replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }

async function loadAll() {
  await Promise.all([loadOverview(), loadTasks(), loadStorages(), loadPlugins()]);
  setInterval(() => {
    if (document.getElementById('page-tasks').style.display !== 'none') loadStatus();
    if (document.getElementById('page-overview').style.display !== 'none') loadOverview();
  }, 5000);
}

// 初始化：hash 定位页面，无 hash 默认总览（等 app 显示后再切页，保证入场动画生效）
(function init() {
  const m = location.hash.match(/^#\/(overview|tasks|storages|browse|plugins|audit|settings)$/);
  const target = m ? m[1] : 'overview';
  if (TOKEN) {
    api('/api/settings').then(() => { showApp(); showPage(target); loadAll(); }).catch(() => showLogin());
  } else { showLogin(); }
  // 移动端：点击遮罩/内容区关闭侧边栏
  document.addEventListener('click', e => {
    const app = document.getElementById('app');
    if (app.classList.contains('sidebar-open') &&
        !e.target.closest('header.sidebar') && !e.target.closest('#sidebarToggle')) {
      app.classList.remove('sidebar-open');
    }
  });
})();
