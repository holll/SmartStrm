// No dependencies, browser server or real storage: exercise the shipped script in a VM.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
function deferred() { let resolve, reject; const promise = new Promise((a,b)=>{resolve=a;reject=b;}); return {promise,resolve,reject}; }
const tick = () => new Promise(resolve => setImmediate(resolve));
function harness() {
 const nodes=new Map(), timers=new Map(), frames=new Map(); let seq=0;
 function element(id) {
  if(nodes.has(id)) return nodes.get(id);
  const classes=new Set(), listeners={};
  const el={id,value:'',checked:false,disabled:false,textContent:'',innerHTML:'',options:[],dataset:{},style:{},listeners,
   classList:{add:c=>classes.add(c),remove:c=>classes.delete(c),contains:c=>classes.has(c),toggle(c,on){if(on===undefined)on=!classes.has(c);on?classes.add(c):classes.delete(c);return on;}},
   addEventListener(k,fn){listeners[k]=fn;},removeEventListener(k){delete listeners[k];},focus(){},querySelector(){return null;},querySelectorAll(){return [];},setAttribute(){},removeAttribute(){},appendChild(){},remove(){}};
  nodes.set(id,el);return el;
 }
 const modal=element('taskLogModal');
 const document={getElementById:element,querySelector:()=>null,querySelectorAll:q=>q==='.dialog'?[modal]:[],addEventListener(){},createElement:()=>element('created'),activeElement:null};
 const context=vm.createContext({console,document,localStorage:{getItem:()=>'',setItem(){},removeItem(){}},location:{hash:''},history:{replaceState(){}},navigator:{},Intl,Date,AbortController,TextDecoder,performance:{now:()=>0},confirm:()=>true,
  fetch:()=>Promise.reject(new Error('unexpected fetch')),
  setTimeout:(f,ms)=>{const id=++seq;timers.set(id,{f,ms});return id;},clearTimeout:id=>timers.delete(id),requestAnimationFrame:f=>{const id=++seq;frames.set(id,f);return id;},cancelAnimationFrame:id=>frames.delete(id)});
 context.window=context;
 vm.runInContext(source,context,{filename:'app.js'});
 const run=s=>vm.runInContext(s,context);
 run("TOKEN='test'; toast=()=>{};");
 return {context,run,element,timers,frames};
}
function response(data,status=200) {return {ok:status<400,status,json:async()=>data};}

test('history log responses cannot overwrite newer history or closed modal', async()=>{
 const h=harness(),a=deferred(),b=deferred();h.context.api=p=>p.includes('/1/')?a.promise:b.promise;
 const first=h.run("viewRunLog(1,'old')"),second=h.run("viewRunLog(2,'new')");
 b.resolve([{msg:'NEW'}]);await second;a.resolve([{msg:'OLD'}]);await first;
 assert.match(h.element('taskLog').textContent,/NEW/);assert.doesNotMatch(h.element('taskLog').textContent,/OLD/);
 const c=deferred();h.context.api=()=>c.promise;const third=h.run("viewRunLog(3,'closed')");h.run('closeLogModal()');c.resolve([{msg:'CLOSED'}]);await third;
 assert.doesNotMatch(h.element('taskLog').textContent,/CLOSED/);
});

test('browse results and errors are ordered independently of history logs',async()=>{
 const h=harness(),a=deferred(),b=deferred();h.run("STORAGES=[{name:'disk/one'}]; BROWSE.storage='disk/one';");h.element('browseStorage').options=[{}];h.element('browseStorage').value='disk/one';
 h.context.api=p=>p.includes('path=%2Fold')?a.promise:b.promise;
 h.run("BROWSE.path='/old'");const first=h.run('loadBrowse()');h.run("BROWSE.path='/new'");const second=h.run('loadBrowse()');
 b.resolve([{name:'NEW',is_dir:false,size:2}]);await second;a.reject(new Error('OLD ERROR'));await first;
 assert.match(h.element('browseRows').innerHTML,/NEW/);assert.doesNotMatch(h.element('browseRows').innerHTML,/OLD/);
});

test('settings wait for storage and prohibit both saves on incomplete or failed load',async()=>{
 const h=harness(),storage=deferred();let puts=0;
 h.context.api=(p,m)=>{if(m==='PUT'){puts++;return Promise.resolve({});}if(p==='/api/storages')return storage.promise;if(p==='/api/settings')return Promise.resolve({strm:{media_ext:[],copy_ext:[],save_dir:'/tmp'}});return Promise.resolve({emby_sync:{storage:'missing/name'}});};
 const loading=h.run('loadSettings()');await tick();assert.equal(h.element('saveWhBtn').disabled,true);assert.equal(h.element('saveSettingsBtn').disabled,true);
 await h.run('saveSettings()');await h.run('saveWebhook()');assert.equal(puts,0);
 storage.resolve([{name:'disk/one'}]);await loading;assert.equal(h.element('wh-storage').value,'missing/name');assert.match(h.element('wh-storage').innerHTML,/不可用/);assert.equal(h.element('saveSettingsBtn').disabled,false);
 h.context.api=()=>Promise.reject(new Error('offline'));await h.run('loadSettings()');assert.equal(h.run('SETTINGS_READY'),false);assert.equal(h.element('saveWhBtn').disabled,true);assert.equal(h.element('saveSettingsBtn').disabled,true);
});

for(const stream of ['state','log']) test(`${stream} SSE reconnects on normal EOF and close cancels retry`,async()=>{
 const h=harness();let calls=0;h.context.fetch=async()=>{calls++;return {ok:true,body:{getReader:()=>({read:async()=>({done:true})})}};};
 if(stream==='log')h.run("selectLog('task')");else h.run('startStateStream()');await tick();
 assert.equal(calls,1);assert.equal(h.timers.size,1);const retry=[...h.timers.values()][0];assert.equal(retry.ms,2000);h.timers.clear();retry.f();await tick();assert.equal(calls,2);
 h.run(stream==='log'?'closeLogModal()':'closeStateStream()');assert.equal(h.timers.size,0);
});

for(const close of ['button','escape','backdrop'])test(`log ${close} close aborts stream, timer and queued rendering`,()=>{
 const h=harness();h.context.fetch=()=>new Promise(()=>{});h.run("selectLog('task'); logAppend(document.getElementById('taskLog'),'pending'); LOG_RETRY=setTimeout(()=>{},2000)");
 const ctrl=h.run('LOG_STREAM_CTRL');assert.equal(h.frames.size,1);
 if(close==='button')h.run('closeLogModal()');if(close==='escape')h.run("trapKey({key:'Escape',currentTarget:document.getElementById('taskLogModal')})");if(close==='backdrop')h.element('taskLogModal').listeners.click({target:h.element('taskLogModal')});
 assert.equal(ctrl.signal.aborted,true);assert.equal(h.run('LOG_TASK'),null);assert.equal(h.frames.size,0);assert.equal(h.timers.size,0);assert.equal(h.element('taskLogModal').classList.contains('show'),false);
});

test('state refresh rebuilds running/stopped action buttons',async()=>{
 const h=harness();let running=true;h.context.api=async()=>[{name:'task',storage:'disk/one',running}];h.run('loadStatus=async()=>{};loadHistory=async()=>{};');h.run('refreshState()');await tick();assert.match(h.element('taskRows').innerHTML,/stopTask/);assert.doesNotMatch(h.element('taskRows').innerHTML,/onclick="runTask/);
 running=false;h.run('refreshState()');await tick();assert.match(h.element('taskRows').innerHTML,/onclick="runTask/);
});

test('task save preserves unknown plugins, partial override and copies a renamed task',async()=>{
 const h=harness();let saved;h.run("PLUGINS=[{id:'known'}];TASK_PLUGIN_DRAFT={unknown:{enabled:true,secret:42},known:{enabled:true,find_text:'old'}};window._editTask='original';loadTasks=()=>{};");
 for(const [id,v]of Object.entries({'tp-known':'custom','tp-config-known':'{"replace_text":"override"}','t-name':'copy','t-storage':'disk/one','t-path':'/','t-cron':'0 8 * * *'}))h.element(id).value=v;
 h.context.api=async(p,m,b)=>{saved={p,m,b};};await h.run('saveTask()');assert.equal(saved.m,'POST');assert.equal(saved.b.plugins.unknown.secret,42);assert.equal(saved.b.plugins.known.replace_text,'override');assert.equal('enabled' in saved.b.plugins.known,false);
 h.element('tp-known').value='inherit';await h.run('saveTask()');assert.equal('known' in saved.b.plugins,false);
});

test('global plugin save retains advanced unknown fields and explicit regex toggle',async()=>{
 const h=harness();let saved;h.run("PLUGINS=[{id:'content_replace',config:{unknown:{x:1},regex_mode:true}}];loadPlugins=()=>{};");h.element('p-content_replace-find').value='a';h.element('p-content_replace-replace').value='b';h.element('p-content_replace-regex_mode').checked=false;
 h.context.api=async(p,m,b)=>{saved=b;};await h.run("savePlugin('content_replace')");assert.equal(saved.unknown.x,1);assert.equal(saved.regex_mode,false);
});

test('logout sends old token before clearing local session and cancels streams and retries',async()=>{
 const h=harness(),network=deferred();let logout,tokenAtSend,removed=false;
 h.context.localStorage.removeItem=key=>{assert.equal(key,'ss_token');removed=true;};
 h.context.fetch=(p,opt)=>{if(p==='/api/logout'){logout=opt;tokenAtSend=h.run('TOKEN');assert.equal(removed,false);return network.promise;}return new Promise(()=>{});};
 h.run("startStateStream();selectLog('task');STATE_RETRY=setTimeout(()=>startStateStream(),2000);LOG_RETRY=setTimeout(()=>startLogStream('task'),2000)");
 const state=h.run('STATE_STREAM_CTRL'),log=h.run('LOG_STREAM_CTRL');const pending=h.run('doLogout()');
 assert.equal(tokenAtSend,'test');assert.equal(removed,true);assert.equal(h.run('TOKEN'),'');assert.equal(state.signal.aborted,true);assert.equal(log.signal.aborted,true);assert.equal(h.timers.size,0);assert.equal(logout.headers.Authorization,'Bearer test');assert.equal(logout.method,'POST');
 network.resolve(response({}));await pending;
});

for(const failure of ['reject','throw','http'])test(`logout ${failure} still clears session and warns revocation is unconfirmed`,async()=>{
 const h=harness();let warning;h.context.toast=(text,level)=>{warning={text,level};};
 h.context.fetch=()=>{if(failure==='throw')throw new Error('offline');if(failure==='reject')return Promise.reject(new Error('offline'));return Promise.resolve(response({},500));};
 await h.run('doLogout()');assert.equal(h.run('TOKEN'),'');assert.equal(h.element('login').style.display,'flex');assert.equal(warning.level,'warning');assert.match(warning.text,/撤销未确认/);
});

test('rename confirmation cancels all writes, confirmed copy leaves A intact, unchanged name uses PUT',async()=>{
 const h=harness(),tasks={A:{name:'A',storage:'disk/one',storage_path:'/original'}};const calls=[];
 h.run("window._editTask='A';loadTasks=()=>{};");
 for(const [id,value]of Object.entries({'t-name':'B','t-storage':'disk/one','t-path':'/changed'}))h.element(id).value=value;
 h.context.fetch=async(p,opt)=>{calls.push({p,opt});const body=JSON.parse(opt.body);if(opt.method==='POST')tasks[body.name]=body;else tasks[new URL(p,'http://mock').searchParams.get('name')]=body;return response({});};
 h.context.confirm=()=>false;await h.run('saveTask()');assert.equal(calls.length,0);assert.equal(tasks.B,undefined);
 h.context.confirm=message=>{assert.match(message,/原任务及文件保留/);return true;};await h.run('saveTask()');
 assert.equal(calls[0].p,'/api/tasks');assert.equal(calls[0].opt.method,'POST');assert.equal(tasks.A.storage_path,'/original');assert.equal(tasks.B.storage_path,'/changed');
 h.element('t-name').value='A';await h.run('saveTask()');assert.equal(calls[1].p,'/api/tasks?name=A');assert.equal(calls[1].opt.method,'PUT');
});

test('task editor waits for storage and does not reopen a closed or newer editor',async()=>{
 const h=harness(),a=deferred(),b=deferred();let calls=0;
 h.run("TASKS=[{name:'old',storage:'disk/one',storage_path:'/'},{name:'new',storage:'disk/one',storage_path:'/new'}];TASK_SAVE_DIR='/tmp';loadTaskPathDirs=()=>{};");
 h.context.api=()=>++calls===1?a.promise:b.promise;
 const first=h.run("openTask('old')"),second=h.run("openTask('new')");
 assert.equal(h.element('taskDialog').classList.contains('show'),false);
 b.resolve([{name:'disk/one'}]);await second;assert.equal(h.element('t-name').value,'new');
 h.element('t-path').value='/edited';a.resolve([{name:'stale'}]);await first;
 assert.equal(h.element('t-name').value,'new');assert.equal(h.element('t-path').value,'/edited');
 const c=deferred();h.context.api=()=>c.promise;h.run('STORAGES=[]');const pending=h.run("openTask('old')");h.run("closeDialog('taskDialog')");c.resolve([{name:'disk/one'}]);await pending;assert.equal(h.element('taskDialog').classList.contains('show'),false);
});

test('late API success from a revoked session is rejected',async()=>{
 const h=harness(),d=deferred();h.context.fetch=()=>d.promise;const pending=h.run("api('/api/settings')");h.run('expireSession()');d.resolve(response({strm:{}}));await assert.rejects(pending,/会话已切换/);
});
