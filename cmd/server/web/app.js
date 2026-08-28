
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

function showLogin() {
  document.getElementById('login').style.display='flex';
  document.getElementById('app').style.display='none';
  // 登录页不显示移动端侧边栏开关
  const toggle = document.getElementById('sidebarToggle');
  if (toggle) toggle.style.display = 'none';
}
function showApp() {
  document.getElementById('login').style.display='none';
  document.getElementById('app').style.display='block';
  // 恢复移动端侧边栏开关（回退到 CSS 控制：桌面隐藏、移动端显示）
  const toggle = document.getElementById('sidebarToggle');
  if (toggle) toggle.style.display = '';
}

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
function doLogout() { localStorage.removeItem('ss_token'); TOKEN=''; closeStateStream(); api('/api/logout','POST').catch(()=>{}); showLogin(); }

function showPage(name) {
  ['overview','tasks','storages','browse','organize','plugins','audit','webhooklog','settings','about'].forEach(p => {
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
  if (name==='organize') loadOrganize();
  if (name==='plugins') loadPlugins();
  if (name==='audit') loadAudit();
  if (name==='webhooklog') loadWebhookLogs();
  if (name==='settings') loadSettings();
  if (name==='about') loadAbout();
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
  LOG_TASK = null; LOG_HISTORY = true; // 展示历史日志，不建立实时流
  closeLogStream();
  logReset();
  document.getElementById('logModalTitle').textContent = '任务日志 - ' + task + ' #' + runId;
  const box = document.getElementById('taskLog');
  box.textContent = '加载中…';
  openDialog('taskLogModal');
  try {
    const lines = (await api('/api/runs/'+runId+'/log')) || [];
    // 请求在途时用户可能已切换查看对象（开实时流/关弹窗），此时丢弃结果
    if (!LOG_HISTORY) return;
    LOG_LINES = lines.slice(-LOG_MAX_LINES).map(formatLogLine);
    logFlush(box);
  } catch(e) {
    if (LOG_HISTORY) box.textContent = '加载失败: ' + e.message;
  }
}

// 关闭日志弹窗时关闭 SSE 连接
function closeLogModal() {
  LOG_TASK = null;
  LOG_HISTORY = null;
  logReset();
  closeLogStream();
  closeDialog('taskLogModal');
}

// 日志渲染：内存行缓冲 + rAF 合并写入单文本节点（DOM 节点数恒为 1）。
// 逐行到达的 SSE 行在下一帧合并渲染，日志爆发时自然节流，避免逐行创建元素卡顿
const LOG_MAX_LINES = 2000; // 缓冲截断上限，防内存与渲染持续膨胀
let LOG_LINES = [];
let LOG_FLUSH_RAF = null;

// 取消待渲染帧并清空缓冲（关闭弹窗/切换查看对象时调用，防止悬挂 rAF 写入旧内容）
function logReset() {
  if (LOG_FLUSH_RAF) {
    cancelAnimationFrame(LOG_FLUSH_RAF);
    LOG_FLUSH_RAF = null;
  }
  LOG_LINES = [];
}

function logFlush(box) {
  LOG_FLUSH_RAF = null;
  box.textContent = LOG_LINES.join('\n');
  box.scrollTop = box.scrollHeight; // 始终跟随最新
}

function logAppend(box, line) {
  LOG_LINES.push(line);
  if (LOG_LINES.length > LOG_MAX_LINES) {
    LOG_LINES.splice(0, LOG_LINES.length - LOG_MAX_LINES);
  }
  if (LOG_FLUSH_RAF) return; // 本帧已有待渲染内容，合并写入
  LOG_FLUSH_RAF = requestAnimationFrame(() => logFlush(box));
}

// 日志级别标签（仿原版：INFO 不显示标签，仅警示级别带标签）
function levelTag(l) {
  return l === 'ERROR' ? '[ERR] ' : l === 'WARN' ? '[WARN] ' : l === 'DEBUG' ? '[DBG] ' : '';
}

// 日志行时间格式（仿原版：MM-DD HH:MM:SS，无年份）
function fmtLogTime(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  // Go 零值时间（0001 年）与无效时间显示空
  if (isNaN(d) || d.getFullYear() < 2000) return '';
  const p = n => String(n).padStart(2, '0');
  return `${p(d.getMonth()+1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
// 日志行渲染：时间 + 级别标签 + 消息（实时与历史日志共用）
function formatLogLine(l) {
  return fmtLogTime(l.time) + ' ' + levelTag(l.level) + l.msg;
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

async function loadBrowse(refresh) {
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
    const qs = '?path=' + encodeURIComponent(BROWSE.path) + (refresh ? '&refresh=1' : '');
    const files = (await api('/api/storages/'+encodeURIComponent(BROWSE.storage)+'/list'+qs)) || [];
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

// ============ 任务日志（SSE 实时流） ============
// EventSource 无法携带 Authorization header，故用 fetch + ReadableStream 手动解析 SSE
let LOG_TASK = null, LOG_HISTORY = false, LOG_STREAM_CTRL = null;

function selectLog(name) {
  LOG_TASK = name; LOG_HISTORY = false;
  document.getElementById('logModalTitle').textContent = '任务日志 - ' + name;
  // 运行中显示停止按钮（原版交互）
  const stopBtn = document.getElementById('logStopBtn');
  const running = (TASKS.find(x => x.name === name) || {}).running;
  if (stopBtn) stopBtn.style.display = running ? '' : 'none';
  openDialog('taskLogModal');
  startLogStream(name);
}

// SSE 实时日志流：连接建立时拉全量历史，随后增量推送（毫秒级实时）
function startLogStream(name) {
  closeLogStream();
  const ctrl = new AbortController();
  LOG_STREAM_CTRL = ctrl;
  const box = document.getElementById('taskLog');
  logReset();
  box.textContent = '';
  fetch('/api/tasks/'+encodeURIComponent(name)+'/log/stream?after=0', {
    headers: { 'Authorization': 'Bearer ' + TOKEN },
    signal: ctrl.signal
  }).then(async resp => {
    if (!resp.ok) {
      // 401（token 过期/已改密）：切回登录页，不进入重连循环
      if (resp.status === 401) { showLogin(); return; }
      throw new Error('HTTP ' + resp.status);
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const m = frame.match(/^event: (\w+)\ndata: (.+)$/m);
        if (m && m[1] === 'line') {
          try {
            // abort 异步生效，已缓冲的行仍会到达：查看对象已切换时丢弃，防串写
            if (LOG_TASK !== name) return;
            const l = JSON.parse(m[2]);
            logAppend(box, formatLogLine(l));
          } catch(err) { /* 忽略解析失败的行 */ }
        }
      }
    }
  }).catch(e => {
    // 连接断开（任务结束/网络抖动）：弹窗仍打开且任务未切换时自动重连
    if (e.name !== 'AbortError' && LOG_TASK === name &&
        document.getElementById('taskLogModal').classList.contains('show')) {
      setTimeout(() => { if (LOG_TASK === name) startLogStream(name); }, 2000);
    }
  });
}
function closeLogStream() {
  if (LOG_STREAM_CTRL) {
    LOG_STREAM_CTRL.abort();
    LOG_STREAM_CTRL = null;
  }
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

// ============ Webhook 日志 ============
const WH_RESULT = {
  ok:['成功','success'], failed:['失败','danger'],
  partial:['部分失败','warning'], skipped:['跳过','secondary']
};
async function loadWebhookLogs() {
  try {
    const list = (await api('/api/webhook/logs?limit=200')) || [];
    const rows = document.getElementById('webhookLogRows');
    if (!list.length) { rows.innerHTML = '<tr><td colspan="7" class="empty">暂无 Webhook 日志，收到通知后自动记录</td></tr>'; return; }
    rows.innerHTML = list.map(l => {
      const [resText, resCls] = WH_RESULT[l.result] || [l.result, 'secondary'];
      const kind = l.kind === 'task'
        ? '<span class="badge text-bg-info">任务</span>'
        : '<span class="badge text-bg-primary">Emby</span>';
      const payloadBlock = l.payload
        ? `<details class="wh-payload"><summary>查看原始通知</summary><pre style="max-height:180px;overflow:auto;font-size:11px;padding:8px;background:var(--bg,#f6f6f6);border-radius:6px;white-space:pre-wrap;word-break:break-all">${esc(l.payload)}</pre></details>`
        : '';
      const detail = l.detail
        ? `<div class="small text-danger" style="max-width:280px;word-break:break-all">${esc(l.detail)}</div>`
        : '';
      return `<tr>
        <td class="num">${fmtTime(l.time)}</td>
        <td>${kind}</td>
        <td><code>${esc(ACTION_NAMES[l.action] || l.action)}</code><div class="small text-muted">${esc(l.event)}</div></td>
        <td style="max-width:200px;word-break:break-all">${esc(l.target || '-')}</td>
        <td style="max-width:200px;word-break:break-all;color:var(--muted)">${l.remote_path ? esc(l.remote_path) : '-'}</td>
        <td><span class="badge text-bg-${resCls}">${resText}</span></td>
        <td style="max-width:340px">${detail}${payloadBlock}</td></tr>`;
    }).join('');
  } catch(e) {}
}

// ============ 目录整理 ============
let ORG_POLL = null; // 整理进度状态轮询定时器

async function loadOrganize() {
  if (!STORAGES.length) STORAGES = (await api('/api/storages')) || [];
  const sel = document.getElementById('org-storage');
  sel.innerHTML = (STORAGES||[]).map(s => `<option value="${esc(s.name)}">${esc(s.name)}</option>`).join('');
  // 页面（重）打开时恢复后台正在进行的整理进度，或显示上次完成结果
  pollOrganizeStatus();
}

// 统一更新整理进度条（兼容 SSE progress 帧与 /api/organize/status）
function updateOrgProgressUI(st) {
  const progWrap = document.getElementById('orgProgressWrap');
  const progBar = document.getElementById('orgProgressBar');
  const progText = document.getElementById('orgProgressText');
  if (!st) return;
  progWrap.classList.remove('d-none');
  const pct = st.total ? Math.round(st.done / st.total * 100) : 0;
  progBar.style.width = pct + '%';
  progBar.setAttribute('aria-valuenow', pct);
  const stage = st.stage === 'move' ? '移动分类' : '整理入库';
  progText.textContent = `（${stage}）${st.done}/${st.total}  ${st.old} → ${st.new}`;
}

// 渲染整理结果表格
function renderOrganizeResult(plan, errors, dryRun) {
  const rows = document.getElementById('orgPlanRows');
  document.getElementById('orgPlanCount').textContent = `共 ${plan.length} 项` + (dryRun ? '（预览）' : '（已执行）');
  rows.innerHTML = plan.map(p => `<tr><td style="word-break:break-all">${esc(p.old)}</td><td style="word-break:break-all">${esc(p.new)}</td></tr>`).join('')
    || '<tr><td colspan="2" class="empty">无可整理项</td></tr>';
  const errBox = document.getElementById('orgErrors');
  if (errors && errors.length) {
    errBox.style.display = ''; errBox.style.whiteSpace = 'pre-wrap';
    errBox.innerHTML = '<strong>处理失败：</strong><div>' + errors.map(esc).join('\n') + '</div>';
  } else { errBox.style.display = 'none'; }
}

// 轮询整理进度：网页关闭后整理在后台继续执行，重开页面据此恢复进度与结果
function pollOrganizeStatus() {
  clearTimeout(ORG_POLL); ORG_POLL = null;
  api('/api/organize/status').then(st => {
    if (st.running) {
      updateOrgProgressUI(st);
      ORG_POLL = setTimeout(pollOrganizeStatus, 1500);
      return;
    }
    // 非运行中：停止轮询
    if (st.finished) {
      // 页面关闭期间整理已完成：展示结果并填满进度条
      const progBar = document.getElementById('orgProgressBar');
      const progText = document.getElementById('orgProgressText');
      document.getElementById('orgProgressWrap').classList.remove('d-none');
      progBar.style.width = '100%'; progBar.setAttribute('aria-valuenow', 100);
      progText.textContent = `完成，共 ${(st.plan||[]).length} 项`;
      renderOrganizeResult(st.plan || [], st.errors || [], false);
    } else {
      // 无活动中整理：隐藏进度条
      const wrap = document.getElementById('orgProgressWrap');
      if (wrap && !wrap.classList.contains('d-none')) wrap.classList.add('d-none');
    }
  }).catch(() => {
    // 网络异常：延迟重试
    ORG_POLL = setTimeout(pollOrganizeStatus, 5000);
  });
}

async function organizeRun(dryRun) {
  const planBtn = document.getElementById('orgPlanBtn');
  const runBtn = document.getElementById('orgRunBtn');
  const btn = dryRun ? planBtn : runBtn;
  btn.disabled = true; const old = btn.innerHTML;
  btn.innerHTML = '<span class="spinner-border spinner-border-sm me-1"></span>' + (dryRun ? '预览中…' : '执行中…');
  const progWrap = document.getElementById('orgProgressWrap');
  const progBar = document.getElementById('orgProgressBar');
  const progText = document.getElementById('orgProgressText');
  // 执行模式显示进度条；预览通常秒级完成，不显示
  if (!dryRun) {
    progWrap.classList.remove('d-none');
    progBar.style.width = '0%';
    progBar.setAttribute('aria-valuenow', 0);
    progText.textContent = '准备中…';
  }
  try {
    const resp = await fetch('/api/organize', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + TOKEN },
      body: JSON.stringify({
        storage: document.getElementById('org-storage').value,
        path: document.getElementById('org-path').value.trim(),
        mode: document.getElementById('org-mode').value,
        id_mode: document.getElementById('org-idmode').value,
        dry_run: dryRun,
        overwrite: document.getElementById('org-overwrite').checked,
      })
    });
    if (resp.status === 401) { showLogin(); throw new Error('未登录'); }
    if (!resp.ok) {
      const ed = await resp.json().catch(() => ({}));
      throw new Error(ed.error || ('HTTP ' + resp.status));
    }
    // 流式解析 SSE：progress 帧更新进度，done 帧取最终结果
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = '', plan = [], errors = [];
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, idx); buf = buf.slice(idx + 2);
        let event = 'message', data = '';
        for (const line of frame.split('\n')) {
          if (line.startsWith('event: ')) event = line.slice(7);
          else if (line.startsWith('data: ')) data += line.slice(6);
        }
        if (!data) continue;
        let d;
        try { d = JSON.parse(data); } catch { continue; }
        if (event === 'progress') {
          updateOrgProgressUI(d); // stage/done/total/old/new 与 status 结构一致
        } else if (event === 'done') {
          plan = d.plan || []; errors = d.errors || [];
        } else if (event === 'error') {
          throw new Error(d.error || '执行失败');
        }
      }
    }
    renderOrganizeResult(plan, errors, dryRun);
    if (!dryRun) {
      progBar.style.width = '100%';
      progBar.setAttribute('aria-valuenow', 100);
      progText.textContent = `完成，共 ${plan.length} 项`;
    }
    toast(dryRun ? '预览完成' : '执行完成');
  } catch(e) {
    const errBox = document.getElementById('orgErrors');
    errBox.style.display = ''; errBox.innerHTML = esc(e.message || String(e));
    toast(e.message || String(e), 'danger');
    if (!dryRun) progWrap.classList.add('d-none');
  } finally { btn.disabled = false; btn.innerHTML = old; }
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
        strm_base: document.getElementById('s-strmBase').value.trim()
      }
    });
    // 刷新保存目录缓存：任务编辑弹窗的「STRM 将保存在」提示立即使用新目录
    TASK_SAVE_DIR = document.getElementById('s-saveDir').value.trim();
    toast('已保存');
  } catch(e){ toast(e.message, 'danger'); }
  btn.disabled = false; btn.textContent = '保存设置';
}

// ============ 关于 ============
// 进入关于页时加载（带缓存，避免每次进入都请求 GitHub）
async function loadAbout() {
  try { renderAbout(await api('/api/about')); } catch(e) {}
}

// 手动检查更新（忽略服务端缓存）
async function checkUpdate(force) {
  const el = document.getElementById('ab-update');
  if (!el) return;
  el.innerHTML = '检查中…';
  try { renderAbout(await api('/api/about' + (force ? '?refresh=1' : ''))); }
  catch(e) { el.innerHTML = '<span style="color:var(--red)">检查失败: ' + esc(e.message) + '</span>'; }
}

function renderAbout(d) {
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.textContent = v || '–'; };
  set('ab-version', d.version);
  set('ab-commit', d.commit ? '#' + d.commit : '–');
  set('ab-build', d.build_time);
  set('ab-checked', fmtTime(d.checked_at));
  const repo = document.getElementById('ab-repo');
  if (repo) { repo.href = d.repo_url || '#'; repo.textContent = d.repo_url || '–'; }
  const el = document.getElementById('ab-update');
  if (!el) return;
  if (d.error) {
    el.innerHTML = '<span style="color:var(--red)">检查失败: ' + esc(d.error) + '</span>';
  } else if (d.is_latest) {
    el.innerHTML = '<span class="tag on">已是最新版本</span>';
  } else {
    el.innerHTML = '<span class="tag off">发现新版本 ' + esc(d.latest || '') + '</span> ' +
      `<a href="${esc(d.latest_url || d.repo_url || '#')}" target="_blank" rel="noopener"><i class="bi bi-box-arrow-up-right"></i> 前往下载</a>`;
  }
}

// ============ 任务状态推送（SSE，替代 5 秒轮询） ============
// EventSource 无法携带 Authorization header，故用 fetch + ReadableStream 手动解析 SSE（与日志流同法）
let STATE_STREAM_CTRL = null;

// 收到状态事件后刷新当前可见页面的任务状态
function refreshState() {
  if (document.getElementById('page-tasks').style.display !== 'none') loadStatus();
  if (document.getElementById('page-overview').style.display !== 'none') loadOverview();
}

function startStateStream() {
  closeStateStream();
  const ctrl = new AbortController();
  STATE_STREAM_CTRL = ctrl;
  fetch('/api/events/stream', {
    headers: { 'Authorization': 'Bearer ' + TOKEN },
    signal: ctrl.signal
  }).then(async resp => {
    if (!resp.ok) {
      // 401（token 过期/已改密）：切回登录页，不进入重连循环
      if (resp.status === 401) { showLogin(); return; }
      throw new Error('HTTP ' + resp.status);
    }
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) !== -1) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        if (frame.startsWith('event: state')) refreshState();
      }
    }
  }).catch(e => {
    // 连接断开（网络抖动/服务重启）：2 秒后重连，重连后服务端会立即推一帧补齐状态
    if (e.name !== 'AbortError' && TOKEN) {
      setTimeout(startStateStream, 2000);
    }
  });
}

function closeStateStream() {
  if (STATE_STREAM_CTRL) {
    STATE_STREAM_CTRL.abort();
    STATE_STREAM_CTRL = null;
  }
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
  startStateStream(); // 任务状态变化由 SSE 推送，不再轮询
}

// 初始化：hash 定位页面，无 hash 默认总览（等 app 显示后再切页，保证入场动画生效）
(function init() {
  const m = location.hash.match(/^#\/(overview|tasks|storages|browse|organize|plugins|audit|webhooklog|settings|about)$/);
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
