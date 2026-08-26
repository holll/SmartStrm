# SmartStrm-Go API 文档

本项目 HTTP 接口完整参考：管理 REST API（`/api/*`）、SSE 推送（`/api/events/stream`、`/api/tasks/:name/log/stream`）、外部 Webhook（`/webhook/*`）。

> 架构说明见 [architecture.md](architecture.md)，用户手册见 [README](../README.md)
> 路由定义源码：`internal/api/api.go`、`internal/webhook/webhook.go`；数据模型见文末附录。

---

## 1. 认证

所有 `/api/*` 接口（除 `/api/login` 外）需要请求头：

```
Authorization: Bearer {token}
```

- token 由 `POST /api/login` 换取，**内存存储，有效期 7 天**（重启失效需重新登录）
- 单管理员账号，用户名固定 `admin`，密码 bcrypt 入库
- 修改密码后**全部 token 立即失效**，活跃 SSE 连接被强制断开

### POST /api/login
请求：
```json
{ "username": "admin", "password": "******" }
```
响应 200：
```json
{ "token": "a1b2c3d4e5f60718293a4b5c" }
```
失败：401 `{"error":"账号或密码错误"}`。错误用户名/密码会写入审计日志（含来源 IP，便于发现暴力尝试）。

### POST /api/logout
注销当前 token（仅该会话失效，其活跃 SSE 连接同步断开）。响应 `{"ok":true}`。

### POST /api/password
修改密码（旧密码校验）。
请求：
```json
{ "old_password": "旧密码", "new_password": "新密码" }
```
- 新密码至少 6 位，否则 400
- 旧密码错误返回 403 `{"error":"旧密码错误"}`
- 成功后清空全部 token、断开全部 SSE，响应 `{"ok":true}`

### 401 处理
token 缺失/过期：`401 {"error":"未登录"}`。过期 token 在每次请求时惰性清理（map 超 100 条才全量清扫）。

---

## 2. 通用约定

- **Base URL**：`http://localhost:8024`（默认端口，`-port` 可改）
- **Content-Type**：请求/响应均为 `application/json`
- **错误响应**：统一 `{"error": "中文描述"}`，配合对应 HTTP 状态码（400 参数/404 不存在/409 冲突/502 上游错误）
- **时间格式**：除日志行外，JSON 中时间字段为 Go `time.Time`（RFC3339，如 `2026-08-19T10:30:00+08:00`）；日志行的 `time` 字段序列化为该格式
- **分页**：列表类接口带 `?limit=` 查询参数，均有上下限钳制

---

## 3. 系统设置

### GET /api/settings
响应 200：
```json
{
  "strm": {
    "media_ext": ["mp4","mkv","mov","avi","wmv"],
    "media_size": 20,
    "copy_ext": ["nfo","jpg","png","ass","srt"],
    "save_dir": "./strm",
    "url_encode": true,
    "strm_base": ""
  },
  "server": {}
}
```
字段含义：

| 字段 | 说明 |
|---|---|
| `media_ext` | 媒体后缀，这些文件生成 STRM |
| `media_size` | 媒体大小阈值（MB），小于此不生成 |
| `copy_ext` | 复制到本地的刮削文件后缀 |
| `save_dir` | STRM 生成根目录（默认相对 `./strm`） |
| `url_encode` | 对 STRM 内 URL 路径编码（逐段 PathEscape 保留 /） |
| `strm_base` | STRM 内容直链前缀；留空使用存储自身地址 |

### PUT /api/settings
请求：`{"strm": {上述字段择需设置}}`（字段缺失按零值覆盖，请传完整对象）。
响应 `{"ok":true}`，写入审计 `settings_update`。

---

## 4. 存储

### GET /api/storages
响应 200：
```json
[
  { "name": "115", "driver": "openlist", "url": "https://alist.example.com", "token": "openlist-xxx" }
]
```

### POST /api/storages
请求：`{"name":"115","url":"https://...","token":"...","driver":"openlist"}`
- `driver` 缺省为 `openlist`；目前仅支持 openlist/alist
- 名称/地址必填；重名 409 `{"error":"存储名已存在"}`

### PUT /api/storages/:name
请求体同 POST（`name` 以 URL 为准，忽略 body 中的 name）。404 = 存储不存在。

### DELETE /api/storages/:name
删除存储。404 = 不存在。

### GET /api/storages/:name/list?path=/xx
存储浏览：调用驱动 `List` 列出目录。`path` 缺省 `/`。
响应 200：
```json
[
  { "name": "FC2", "size": 0, "is_dir": true, "modified": "2026-08-19T10:30:00+08:00" }
]
```
上游错误 502 `{"error":"..."}`。用于任务编辑「浏览…」与任务行路径直达。

---

## 5. 目录整理

### POST /api/organize
网盘目录整理（番号识别→命名规范化→按类归整，移植自 alist_organizer）。
请求：
```json
{
  "storage": "115",
  "path": "/115/AV-cli",
  "mode": "organize",      // organize | move | all
  "id_mode": "AV",         // AV | FC2
  "dry_run": true,         // true 仅预览计划
  "overwrite": false
}
```
响应 200：
```json
{
  "plan": [
    { "old": "/115/AV-cli/ADN-468 - 电影.mp4", "new": "/115/AV-cli/ADN-468/ADN-468.mp4" }
  ],
  "errors": []
}
```
- 模式说明：`organize`=一步入库（散落视频识别番号后直接归入分类库并规范命名：AV→`{目标目录去 -cli}/{首字母}/{番号}/`，FC2→`{目标目录去 -cli}/{番号}/`）；`move`=把已整理好的番号文件夹移入分类库（AV→`{目标目录去 -cli}/{首字母}/`，FC2→`{目标目录去 -cli}/`）；`all`=organize 一步入库 + 移动 TargetPath 下遗留的标准番号文件夹
- `dry_run=true` 只返回计划不落盘，适合先预览
- 执行会写入审计（含 mode/dry_run/处理项数）
- 失败：400（参数/模式未知）、404（存储不存在）

---

## 6. 任务

### 任务模型（config.Task）

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 任务名（主键） |
| `storage` | string | 存储名 |
| `storage_path` | string | 存储中媒体根路径 |
| `crontab` | string | 定时表达式；空则不定时 |
| `incremental` | bool | true=增量；false=同步（清理远端已删除的本地文件） |
| `dir_time_check` | bool | 目录时间检查（mtime 未变跳过递归） |
| `keep_local_asset` | bool | 同步模式下保留本地非 .strm 文件 |
| `plugins` | map | 任务级插件配置（覆盖全局同名插件） |

### GET /api/tasks
响应 200：任务数组，每项为任务模型 +：
```json
{ "...任务字段": "...",
  "next_run": "2026-08-20T00:00:00+08:00",   // 无定时则缺省
  "running": false }
```

### POST /api/tasks
请求：任务模型 JSON。`name/storage/storage_path` 必填，重名 409。成功后触发 Cron 热重载。

### PUT /api/tasks/:name / DELETE /api/tasks/:name
修改（body 为完整任务模型，name 以 URL 为准）/ 删除。均触发 Cron 热重载；404 = 不存在。

### POST /api/tasks/:name/run
立即运行（**异步**：立刻返回 `{"ok":true}`，任务在后台执行）。同名任务已在运行 → **409** `{"error":"任务正在运行中"}`。

### POST /api/tasks/:name/stop
停止运行中任务（context 取消，扫描中的网络请求一并中断）。未在运行 → 409。

### POST /api/tasks/run_all
运行全部任务。有失败（如已在运行）时返回 409 `{"error":"a: 任务正在运行中; b: ..."}`，全部成功返回 `{"ok":true}`。

### GET /api/tasks/status
任务状态快照（前端主数据源）：
```json
{
  "fc2": {
    "running": false,
    "start": "2026-08-19T08:00:00+08:00",
    "end": "2026-08-19T08:03:20+08:00",
    "result": { "generated": 3, "copied": 2, "removed": 0, "skipped": 1,
                "skipped_dirs": 0, "list_failed": 0, "errors": [] },
    "next_run": "2026-08-20T00:00:00+08:00"
  }
}
```
- `result` 仅在运行结束后存在；正在运行时为 `null`/缺省
- `next_run` 是该任务下次 Cron 触发时间；未运行过（无 result）的任务也有 `start/end` 零值

### GET /api/tasks/:name/log?after={seq}
任务日志**增量轮询**。`after` 为上次拿到的 `after` 值，缺省 0（取全部）。
响应 200：
```json
{
  "after": 42,
  "lines": [
    { "seq": 40, "time": "2026-08-19T08:00:05+08:00", "level": "INFO", "msg": "扫描目录 /FC2" }
  ]
}
```
`level` 取值为 INFO/WARN/ERROR 等；`seq` 单调递增，前端用 `after` 续传。

### GET /api/tasks/:name/log/stream?after={seq}
任务日志 **SSE 流**（替代轮询）：连接建立先发 `(after, 快照]` 区间历史，再实时推送增量。事件格式见 [§10](#10-sse-协议)。框架会保持连接，业务侧不超时，**需跳过 gzip**（服务端已自动排除）。

### POST /api/tasks/:name/strm_replace
批量替换该任务已生成 STRM 的内容（换域名/端口后无需重新生成）。
请求：
```json
{ "find_text": "https://old.example.com", "replace_text": "https://new.example.com", "regex_mode": false }
```
响应 `{"ok":true,"count":12}`（count=实际发生替换的文件数）。正则无效返回 400。

### POST /api/tasks/:name/overwrite
**全量覆写**：删除任务目录 → 删除该任务 `dir_cache`（强制忽略目录时间检查）→ 重新生成。响应 `{"ok":true}`。

### POST /api/tasks/:name/clear
**一键清除**：删除任务目录下所有文件（不动配置/缓存）。响应 `{"ok":true}`。

---

## 7. 插件

### GET /api/plugins
全局插件列表与配置：
```json
[
  { "id": "content_replace", "name": "STRM内容替换", "version": "0.1",
    "enabled": false, "config": {} }
]
```
`config` 为该插件**全局**配置（任意 JSON，字段见下表）。

### PUT /api/plugins/:id
请求：`{"enabled":true,"find_text":"...","replace_text":"...","regex_mode":false}`
保存为**全局**插件配置。404 = 插件不存在。

内置插件配置项：

| id | 配置项 |
|---|---|
| `content_replace` | enabled, regex_mode, find_text, replace_text |
| `custom_strm_name` | enabled, custom_name（模板 `{name}`/`{ext}`） |
| `scan_filter` | enabled, media_ext, media_size_min, copy_ext（留空沿用全局） |
| `skip_keyword` | enabled, only_dir, regex_mode, filter_mode, keywords |
| `task_delay` | enabled, list_delay, copy_delay (ms) |

---

## 8. Webhook 配置管理

### GET /api/webhook/info
```json
{
  "trigger": "http://localhost:8024/webhook/abc123xyz",
  "emby": "http://localhost:8024/webhook/emby/abc123xyz",
  "token": "abc123xyz",
  "emby_sync": { "enabled": false, "strm_in_emby": "", "storage_path_map": "",
                 "storage": "", "allowed_prefix": [] }
}
```

### PUT /api/webhook
保存 Emby 删除同步配置：
```json
{ "emby_delete_sync": {
    "enabled": true,
    "strm_in_emby": "/data/docker/smartstrm/strm",
    "storage_path_map": "/115",
    "storage": "115",
    "allowed_prefix": ["/115/FC2", "/115/AV"] } }
```
响应 `{"ok":true}`。

### POST /api/webhook/regenerate
重新生成 webhook token（**旧地址立即失效**）。响应：
```json
{ "ok": true, "token": "newtok", "trigger": "http://host/webhook/newtok", "emby": "http://host/webhook/emby/newtok" }
```

### GET /api/webhook/logs?limit=100
最近 Webhook 处理日志：
```json
[
  { "id": 1, "time": "2026-08-19T09:00:00+08:00", "kind": "emby", "event": "item.deleted",
    "payload": "{\"Event\":...}", "action": "emby_delete", "target": "/data/.../ADN-468.strm",
    "remote_path": "/115/FC2/A", "result": "ok", "detail": "已删除远端路径" }
]
```
字段：`kind`=emby/task；`action`=emby_delete / emby_delete_failed / task_trigger；`result`=ok / failed / skipped / partial。`payload` 入库截断 2048 字符。

---

## 9. 运行历史 / 审计 / 关于

### GET /api/runs?limit=50
最近运行记录（倒序）：
```json
[
  { "id": 1, "task": "fc2", "start_at": "2026-08-19T08:00:00+08:00",
    "end_at": "2026-08-19T08:03:20+08:00", "status": "success",
    "generated": 3, "copied": 2, "removed": 0, "skipped": 1, "error": "" }
]
```
`status` 取值：`running / success / stopped / error`。

### GET /api/runs/:id/log
某次运行的**完整**日志（从数据库读，独立于内存缓冲的实时日志）：
```json
[ { "time": "...", "level": "INFO", "msg": "..." } ]
```

### GET /api/tasks/:name/history?limit=20
某任务运行历史（结构同 /api/runs，按任务过滤）。

### GET /api/audit?limit=100
审计日志：
```json
[ { "id": 1, "time": "...", "user": "admin", "action": "login", "target": "", "detail": "" } ]
```
`action` 取值：login / login_failed / logout / password_change / settings_update / storage_* / task_* / plugin_update / webhook_update / webhook_regenerate / organize / emby_delete / emby_delete_failed。

### GET /api/about?refresh=1
版本与 GitHub 更新检查：
```json
{
  "version": "0.1.0", "commit": "abc1234", "build_time": "2026-08-19 10:00:00",
  "repo_url": "https://github.com/holll/SmartStrm",
  "is_latest": true, "latest": "0.1.0", "latest_url": "https://.../releases/tag/v0.1.0",
  "published_at": "...", "checked_at": "...", "error": ""
}
```
`?refresh=1` 忽略 10 分钟缓存强制检查；GitHub 不可达时 `error` 非空、`is_latest` 缺省 false，不影响其余字段。

---

## 10. SSE 协议

两个只读流式接口，均为 `text/event-stream`，`Cache-Control: no-cache`，服务端已对这两条路径排除 gzip。

### 事件格式

任务日志流（`/api/tasks/:name/log/stream`）：
```
event: line
data: {"seq":40,"time":"2026-08-19T08:00:05+08:00","level":"INFO","msg":"扫描目录 /FC2"}

```
- 连接建立先发 `(after, 快照]` 区间历史（`after` 查询参数，缺省 0），再实时推送增量行，顺序无丢失

任务状态流（`/api/events/stream`）：
```
event: state
data: {}

```
- **空帧信号**：不携带数据，前端收到后按需拉取可见页面最新状态（`/api/tasks/status`）
- 连接建立即推一帧（前端据此完成首次/重连刷新）
- 任务状态变化（开始/结束/panic 收尾）广播；多次变化合并为 500ms 内一次推送

### 连接生命周期
- **鉴权**：与 REST 相同，请求头 `Authorization: Bearer {token}`；无效 token 会在后续登录检查/日志订阅中失效
- **登出**：只断开该 token 的 SSE 连接；**改密**：断开全部 SSE 连接（旧会话立即失效）
- 前端重连：断线后重连并传 `after` 续传日志；状态流重连即推首帧

---

## 11. 外部 Webhook（无需登录）

> token 即密钥：路径中的 token 必须与 `webhook.token` 一致，否则 403 `{"error":"无效的 token"}`。
> 两入口共用同一套处理逻辑，按请求体自动分派。

### POST /webhook/{token}
任务触发（对接 QAS / CloudSaver / 脚本转存后触发）。
请求：
```json
{ "strmtask": "fc2,av" }
```
也兼容 `{"task":"fc2"}`。逗号分隔多个任务名，逐个触发。
响应：
- 全部成功：200 `{"ok":true}`
- 部分失败：409 `{"error":"fc2: 任务正在运行中"}`（失败任务全部返回同样结构）

该接口会写审计（`webhook/task_trigger`）与 webhook_logs（result=ok/partial/failed）。

### POST /webhook/emby/{token}
Emby 删除同步。兼容旧地址 `/webhook/{token}`——统一入口按 body 含 `Event` 字段判定进入本流程。
请求（Emby 通知）：
```json
{ "Event": "item.deleted", "Item": { "Path": "/data/docker/smartstrm/strm/FC2/A/ADN-468.strm" } }
```
处理流程（`internal/webhook/webhook.go`）：
1. 未启用同步 → 200 `ignored: emby 删除同步未启用`
2. 事件类型白名单：仅 `item.deleted` / `ItemDeleted` / `library.deleted`，否则 200 `ignored`
3. `Item.Path` 前缀必须命中 `emby_sync.strm_in_emby`，否则 500（防任意路径）
4. `.strm` 文件取父目录（统一按文件夹删除）
5. 拼接 `storage_path_map + 相对路径` 得远端路径
6. 白名单校验：远端路径必须命中 `allowed_prefix` 之一，否则 500（防误删）
7. `driver.Remove(存储, 远端路径)`

响应：成功 200 `ok`；失败 500 `{"error":"..."}`。每次调用写 webhook_logs（含收到的完整通知/动作/远端路径/结果）与审计。

---

## 12. 附录：数据模型

config 结构源码：`internal/config/config.go`（`STRMConfig` / `EmbyDeleteSync` / `WebhookConfig` / `Storage` / `Task` / `PluginConfig`）。

### 远端文件（driver.File）
`{name, size, is_dir, modified}`（modified 为 OpenList 返回时间，兼容解析多格式）。

### 任务日志行（task.LogLine）
`{seq, time, level, msg}`；内存环形缓冲上限 50000 行，按任务隔离，运行开始重置。

### 运行统计（generator.Result）
`{generated, copied, removed, skipped, skipped_dirs, list_failed, errors}`。

### 数据库表
`admin` / `settings` / `storages` / `tasks` / `webhook` / `dir_cache` / `runs` / `task_logs` / `audit_logs` / `webhook_logs`（10 张，详见 architecture.md §6）。

### OpenList 上游 API（只读参考）
未见官方后端的异构响应时，可参考仓库根 [`openlist-api-llms.txt`](../openlist-api-llms.txt)（OpenList 服务端接口签名，非本项目 API）。
