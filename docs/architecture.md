# SmartStrm-Go 技术文档

Go 语言实现的 STRM 生成工具（仅 STRM 相关功能）：OpenList/AList 网盘目录 → 本地 `.strm` 文件映射，供 Emby/Jellyfin 等媒体服务器入库播放。

> 快速上手见 [README](../README.md)，接口对接见 [api.md](api.md)

---

## 1. 总体架构

```
┌─────────────────────────── SmartStrm-Go ───────────────────────────┐
│                                                                     │
│   ┌──────────┐   ┌─────────┐   ┌─────────┐   ┌──────────┐         │
│   │ 内置 Web  │   │  REST    │   │ Webhook │   │ Cron     │         │
│   │ 管理页     │──▶│  API     │──▶│ 处理    │──▶│ 调度     │         │
│   │ (embed)   │   │ :8024   │   │         │   │          │         │
│   └──────────┘   └────┬─────┘   └────┬─────┘   └────┬─────┘         │
│                       │              │              │               │
│                       ▼              ▼              ▼               │
│                 ┌──────────────────────────────────────────────┐    │
│                 │             task.Manager                      │    │
│                 │  任务 CRUD / 运行状态 / 并发互斥 / Cron / 日志 │    │
│                 └───┬──────────────────────────────────────────┘    │
│                     ▼                                                │
│        ┌───────────────────────┐      ┌──────────────────────────┐  │
│        │   generator.Generator │      │   organize.Organizer     │  │
│        │  扫描→过滤→插件→生成    │      │  番号识别/命名规范/整理/移动 │  │
│        └──────────┬────────────┘      └────────────┬─────────────┘  │
│                   ▼                                ▼                 │
│        ┌────────────────────────────────────────────────────────┐  │
│        │            driver.Driver (OpenList / AList)            │  │
│        │   List(分页) / Remove / Rename / Mkdir / Move          │  │
│        └──────────────────────────┬─────────────────────────────┘  │
└───────────────────────────────────┼─────────────────────────────────┘
                                    ▼
                      OpenList/AList 服务（/api/fs/*）
```

**分层职责**：

| 层 | 包 | 职责 |
|---|---|---|
| 入口 | `cmd/server` | 装配、配置加载（从 DB）、embed 管理页注册、信号退出 |
| 接口 | `internal/api` | 管理 REST API + Bearer token 认证 + SSE 日志/状态推送 |
| 接口 | `internal/webhook` | 外部触发（任务触发 / Emby 删除同步），统一入口按 body 分派 |
| 调度 | `internal/task` | 任务模型、Cron 调度（robfig/cron）、运行状态、日志缓冲 |
| 核心 | `internal/generator` | STRM 生成主流程（扫描→过滤→插件链→生成/清理） |
| 扩展 | `internal/plugins` | 插件接口 + 5 个内置插件 |
| 整理 | `internal/organize` | 网盘目录整理：番号识别、命名规范化、cli 整理 + 按首字母分类移动 |
| 驱动 | `internal/driver` | 存储驱动抽象与 OpenList 实现（含按 base 共享的 API 限速器） |
| 数据 | `internal/db` | SQLite 存储层：配置表 + 运行/日志/审计/webhook 日志 + 目录缓存 |
| 版本 | `internal/version` | 构建版本信息 + GitHub 更新检查（10min 缓存） |

**设计原则**：
- **配置即数据库**：全部配置（存储/任务/Webhook/插件/全局设置/账号）持久化于 SQLite `data/data.db`，无配置文件；API 修改即写回，任务变更触发 Cron 热重载，无需重启
- **单二进制交付**：内置 Web 管理页 Go embed（`//go:embed web`），无前端构建链、无第三方前端框架
- **纯 Go 无 CGO**：SQLite 用 `modernc.org/sqlite`，Windows 部署友好
- **驱动接口隔离**：生成器/整理器只依赖 `driver.Driver` 接口，便于未来扩展驱动

---

## 2. STRM 生成主流程

```
触发（Cron / Webhook / UI / 手动 / overwrite）
        │
        ▼
task.Manager.Run(name)：并发互斥 → 创建 context.WithCancel 存入 cancel map
        │                                → 创建运行记录(runs/status=running)
        ▼
EnvForTask：合并全局+任务级插件配置 → 应用 scan_filter 覆盖 → 应用 task_delay
        │
        ▼
generator.Run(ctx)
        │
        ├─ loadDirCache()：全量载入该任务 dir_cache 快照到内存
        ▼
walkDir(根路径, "", mtime=0)                    递归：
  ├─ driver.List(path)（per_page=100 循环翻页）  1. 列出目录
  ├─ 限速器 + task_delay.list_delay             2. 请求排队 + 延时（降风控）
  ├─ 记录目录 mtime → 内存快照                   3. 目录时间检查收集
  └─ 遍历条目：
       ├─ 插件过滤（skip_keyword.ShouldSkip）    4. 关键词过滤
       ├─ 目录：dir_time_check 对比缓存跳过      5. 目录时间检查（mtime 解析失败不跳过）
       │        └─ 递归 walkDir
       ├─ 媒体文件（后缀∈media_ext 且 size≥阈值）
       │        ├─ 命名：默认 {name}.({ext}).strm → custom_strm_name 覆盖
       │        ├─ 内容：{base}/d/{路径}（直链解析交给 OpenList 按需处理）
       │        ├─ URL 编码（可选，路径逐段 PathEscape 保留 /）
       │        ├─ content_replace 插件链修改内容
       │        └─ 写入本地 .tmp+rename 原子写 → remoteSet 登记
       └─ 刮削文件（后缀∈copy_ext）
                ├─ 大小一致则跳过（复用）
                └─ 下载到本地（/d/ 直链，重名 .tmp 原子写）
        │
        ▼
saveDirCache()：事务批量 upsert 到 dir_cache（按任务分区）
        │
        ▼
非增量模式（incremental=false）：cleanupLocal()
  删除 remoteSet 之外的本地文件（keep_local_asset=true 时保留非 .strm；
  .smartstrm 元数据目录一律不清理）
        │
        ▼
FinishRun：更新 runs 记录（success/error/stopped + generated/copied/removed/skipped）
  日志批量 InsertLogs → task_logs
```

### 2.1 生成内容格式

| 项 | 说明 |
|---|---|
| 内容模板 | `{strm_base}/d{远端路径}`（路径兼容模式，OpenList 在请求时解析直链） |
| `strm_base` | 留空使用存储自身地址 `存储.URL`；可指向 302/代理服务 |
| `url_encode` | 为 true 时对路径逐段 `url.PathEscape`（保留 `/`），默认开启 |
| 文件命名 | 默认 `{name}.({ext}).strm`；`custom_strm_name` 可改（改后丢失扩展名信息，Emby 同步删除失效） |

### 2.2 目录时间检查

- 远端目录 mtime 取自 OpenList `/api/fs/list` 返回的 `modified` 字段（随列表附带，无额外请求）；`parseOpenListTime` 兼容 RFC3339 纳秒/无时区/纯日期/unix 秒等格式，解析失败返回零值
- 每次扫描把目录 mtime 快照写入 SQLite `dir_cache` 表 `(task, remote_path, mtime 秒)`，主键按任务分区；启动全量载入、结束事务批量 upsert
- 下次运行：`dir_time_check=true` 且远端 mtime ≤ 缓存 → 跳过递归
- **mtime 解析失败（零值）时不跳过**，保证安全

### 2.3 增量 vs 同步

| 模式 | 行为 |
|---|---|
| `incremental=true` | 只生成/更新新增文件，不清理 |
| `incremental=false` | 同步：扫描结束后清理本地 `remoteSet` 之外的 `.strm`（远端已删除联动清理） |

### 2.4 任务辅助操作

- **全量覆写**（`overwrite`）：删任务目录 → 删该任务 `dir_cache`（强制忽略"目录未变化"重新全扫）→ 重新 `Run`
- **一键清除**（`clear`）：仅删除任务目录下所有文件，不动缓存与配置
- **STRM 批量替换**（`strm_replace`）：遍历任务目录下 `.strm`，按文本/正则替换内容（换域名/端口后无需重新生成），返回实际改动文件数

---

## 3. 插件系统

### 3.1 接口设计

```go
// 基础接口：所有插件实现
type Plugin interface {
    ID() string
    Name() string
    Version() string
    Enabled(cfg config.PluginConfig) bool
}

// 可选钩子（生成器按类型断言调用）
type Filter          interface{ ShouldSkip(env *Env, name string, isDir bool) bool }
type ContentModifier interface{ ModifyContent(env *Env, content string) string }
type NameFormatter   interface{ FormatName(env *Env, name, ext string) string }
type Delay           interface{ ListDelay(env *Env) time.Duration; CopyDelay(env *Env) time.Duration }
type ScanOverride    interface{ Override(env *Env, mediaExt []string, mediaSize int, copyExt []string) ([]string, int, []string) }
```

### 3.2 配置合并规则

```
合并配置 = 全局 Plugins map ∪ 任务级 Plugins map（任务级覆盖同名插件）
env.PluginConfig(p) = 合并后 [p.ID()] 对应配置
```

- 插件未在任务级配置时继承全局配置
- 任务级只写 `{enabled:true}` 时仅启用，其余字段沿用全局
- 任务级 `plugins` 以 JSON 存于 `tasks.plugins` 列；全局存于 `settings["plugins"]`

### 3.3 内置插件

| ID | 名称 | 钩子 | 配置项 | 说明 |
|---|---|---|---|---|
| `content_replace` | STRM内容替换 | ContentModifier | enabled, regex_mode, find_text, replace_text | 正则/纯文本替换 STRM 内容 |
| `custom_strm_name` | 自定义STRM文件名 | NameFormatter | enabled, custom_name | 模板变量 `{name}`、`{ext}`（原扩展名，不含点） |
| `scan_filter` | 高级扫描过滤设置 | ScanOverride | enabled, media_ext, media_size_min, copy_ext | 留空字段沿用全局 |
| `skip_keyword` | 文件名关键词过滤 | Filter | enabled, only_dir, regex_mode, filter_mode, keywords | filter_mode=true 反转（筛选模式）；only_dir 仅过滤目录 |
| `task_delay` | 任务请求延时 | Delay | enabled, list_delay, copy_delay (ms) | 列表/复制后延时，降低 115 等风控 |

执行点汇总：

| 阶段 | 钩子 | 插件 |
|---|---|---|
| Env 构建 | ScanOverride / Delay | scan_filter, task_delay |
| 目录/文件遍历 | Filter | skip_keyword |
| 生成内容 | ContentModifier | content_replace |
| 生成文件名 | NameFormatter | custom_strm_name |
| 复制/列表后 | Delay | task_delay |

---

## 4. 存储驱动

### 4.1 接口

```go
type Driver interface {
    List(ctx, path) ([]File, error)                    // 分页拉取目录
    Remove(ctx, path) error                            // 删除路径（文件或目录）
    Rename(ctx, path, newName string) error            // 重命名
}

// 整理器额外需要的操作（organize.Client 扩展接口，OpenList 满足）
type Client interface {
    driver.Driver
    Mkdir(ctx, path string) error
    Move(ctx, srcDir, dstDir string, names []string) error
}
```

### 4.2 OpenList 实现要点

- 认证：`Authorization: {token}`（openlist- 前缀 token，AList 兼容）
- **限速器**：按 base 共享的 `rateLimiter`（见 `internal/driver/ratelimit.go`），所有 API 请求先排队
- 列表分页：`per_page=100` 循环翻页；`total > 0` 时用 total 判断是否还有下一页，避免整页时多余空请求
- `Data == null` → `ErrNotFound`
- 错误统一：非 200 code 返回 `code + message`
- **Remove**：OpenList 的 `/api/fs/remove` 要求 `{"dir": 父目录, "names": [名称]}`（AList 新版格式），传路径数组会报 "Empty file names"
- `DownloadURL(path)` → `{base}/d{path}`（STRM 内容来源）

---

## 5. 目录整理（internal/organize）

移植自 alist_organizer：对网盘目录做番号识别 → 命名规范 → 按类归整。通过 `POST /api/organize` 触发（详见 api.md），不参与 STRM 任务调度链。

### 5.1 三种模式

| Mode | 行为 |
|---|---|
| `organize` | **cli 内部整理**：遍历目录下散落的视频文件，识别番号后归入 `{目标目录}/{番号}/`，同时重命名为规范名 |
| `move` | **分类移动**：把已整理好的番号文件夹移入分类库（AV→`{目标目录去 -cli}/{首字母}/`，FC2→`{目标目录去 -cli}/`） |
| `all` | 先 `organize` 再 `move`（move 前重新扫描以反映整理后状态） |

### 5.2 番号识别与命名规则（rules.go）

- **AV**：`parseAvVideoID` 匹配 `^[a-z]{1,10}-?\d{2,5}v?`（可选 `-标签`；标签 `c/u/uc/cu` 视为单部）。`normalizeAvCode` 统一为 `前缀-数字`（不足千位补零，如 `hmn00035 → HMN-035`）
- **FC2**：`parseFc2VideoID` 匹配 `fc2[-_\s]?ppv[-_\s]?\d{5,8}`，规范为 `FC2-PPV-{数字}`（无码源标记 `-c/-u/-uc/-cu` 视为单部）
- 文件名含 `uncensored` 且单部时补 `-U` 标签
- **单部**：目录与文件名均取规范化番号（含单部标签）；**分集**：固定保留分集编号（`{番号}-{标签}`）
- 边界防护：番号匹配结束后紧跟 `[0-9a-zA-Z_]` 视为不匹配（如 `ADN-468-x`），避免误吞

### 5.3 运行要点

- 多次 `Mkdir`/`List`/`Remove` 结果缓存于内存（`Organizer` 结构），避免重复 `fs_list`
- `Move` 失败且 `Overwrite=true` 时先删目标同名再重试；重命名失败会回滚
- `DryRun=true` 只产出计划不落盘（返回 `{plan:[{old,new}], errors:[]}`），前端可预览
- 错误收集进 `errors` 数组而非中断整体

---

## 6. 数据与配置（SQLite）

驱动 `modernc.org/sqlite`（纯 Go 无 CGO）。库文件固定 `data/data.db`（自动创建 data 目录），**整个 `data/` 目录即备份单元**。

### 6.1 表结构一览

| 表 | 主键 | 内容 | 用途 |
|---|---|---|---|
| `admin` | `id=1` | username, password_hash (bcrypt) | 单管理员账号 |
| `settings` | `key` | `strm`、`plugins` 等 JSON 配置 | 全局设置持久化 |
| `storages` | `name` | 驱动/URL/token | 存储配置 |
| `tasks` | `name` | 任务模型（plugins 为 JSON 列） | 任务配置 |
| `webhook` | `id=1` | token, emby_delete_sync(JSON) | Webhook 配置 |
| `dir_cache` | `(task, remote_path)` | mtime（unix 秒） | 目录时间检查缓存 |
| `runs` | `id` AUTOINC | 每次运行记录（状态/统计） | 运行历史、统计报表 |
| `task_logs` | `id` AUTOINC | run_id 关联的日志行 | 历史日志回看 |
| `audit_logs` | `id` AUTOINC | 登录/配置变更/运行/Webhook 审计 | 审计追责 |
| `webhook_logs` | `id` AUTOINC | 收到通知/动作/远端路径/结果 | Webhook 处理日志详情 |

### 6.2 存储约定

- **单写者串行**：`SetMaxOpenConns(1)`，杜绝 SQLite 锁冲突
- **时间格式**：统一存 `"2006-01-02 15:04:05"` 字符串（SQLite `date()` 可聚合读取），读回由驱动转 `time.Time`；`dir_cache.mtime` 为 unix 秒整数
- **运行记录状态**：`running / success / stopped / error`；启动异常退出遗留的 `running` 记录由 `CleanupStaleRuns` 归为 error
- **Webhook payload**：入库截断 2048 字符，防超大通知撑爆库
- null 列一律 `COALESCE(...,'')` / `sql.NullTime` 接收，避免 NULL 扫描报错

### 6.3 本地产物

```
{save_dir}/{任务名}/
└── 目录结构镜像远端/
    ├── xxx.(mp4).strm      # 生成的 STRM（默认命名）
    └── xxx.nfo / xxx.jpg   # 复制的刮削文件
```

`save_dir` 默认相对路径 `./strm`（Go 程序无 Docker 挂载限制）；Docker 部署在 Web 设置中改为容器内可写路径。

---

## 7. HTTP 接口概览

细化：认证/全部路由/SSE 协议/Webhook 详见 **[api.md](api.md)**。要点：

- 认证：无内置会话 cookie，`POST /api/login` 换内存 Bearer token（有效期 7 天），请求头 `Authorization: Bearer {token}`
- 管理页为 SPA：静态文件逐文件注册 + ETag（304）+ NoRoute 兜底返回 index.html；`/api`、`/webhook` 前缀的非 API 路径返回 JSON 404
- gzip 中间件对 `/api/tasks/:name/log/stream`、`/api/events/stream` 排除（实时流式输出）
- **SSE**：任务实时日志 `/api/tasks/:name/log/stream`（`event: line, data: {json}`）；任务状态推送 `/api/events/stream`（`event: state, data: {}` 空帧，前端收到后按需拉取）；改密/登出主动断开活跃 SSE 连接
- Webhook 统一入口 `/webhook/:token`（按请求体含 `Event` 字段自动分派 Emby/触发），`/webhook/emby/:token` 保留兼容旧地址
- 关于页 `/api/about`：版本 + GitHub 更新检查（holll/SmartStrm releases/latest，10min 缓存，`?refresh=1` 强制）

---

## 8. 并发、停止与日志

- `task.Manager` 用 `running map` 互斥：同一任务并发触发返回「任务正在运行中」；Cron 链 `SkipIfStillRunning` 跳过重叠
- **停止**：每次 `Run` 创建 `context.WithCancel` 存入 `cancel map`；`Stop` 调 cancel → 生成器 `walkDir` 每轮检查 `ctx.Err()` 中断，net/http 的 `NewRequestWithContext` 一并取消进行中的网络请求；已停止任务错误固定「任务已停止」
- **任务日志缓冲**（`task.LogBuffer`）：环形缓冲上限 50000 行，按任务隔离，每次运行开始重置；生成器经 `SetLogger` 注入；支持 SSE 订阅者（订阅时快照 + 增量实时推送，无丢失无乱序）；前端也可轮询 `?after={seq}`
- **状态推送**：Manager `SetOnState` 钩子，RunAll 逐任务开始/结束合并为 500ms 节流一次广播

---

## 9. 安全设计

| 面 | 措施 |
|---|---|
| 管理 API | 内存 Bearer token（7 天）；口令 bcrypt 入库；密码 ≥6 位，改密后清空全部 token 并断开全部 SSE |
| Webhook | 路径 token 鉴权（`/webhook/{token}`），UI 可一键重新生成、旧地址立即失效；请求体限制 1MB |
| Emby 删除 | 三重防护：前缀校验（必须命中 `strm_in_emby`）+ 路径映射 + 白名单（必须命中 `allowed_prefix`），事件类型白名单 |
| UI XSS | `esc()`（HTML 文本）与 `jsAttr()`（onclick 参数）双转义；用户可控字段拼接前转义 |
| 文件写入 | `.tmp` + rename 原子写，失败不留残片 |
| 资源 | `debug.SetMemoryLimit(96MiB)` + GCPercent 200（无负载 RSS ~20MB）；限速器防触发远端风控 |

---

## 10. 启动流程（cmd/server/main.go）

```
解析 -port / -reset-password
  → 建 data/ 目录，db.Open("data/data.db")（自动建表 10 张）
  → -reset-password：生成随机密码 bcrypt 入库后退出
  → initAdmin：无账号则生成随机密码并打印（首次启动）
  → CleanupStaleRuns：清理上次异常退出遗留的 running 记录
  → buildConfig：从 DB 组装 cfg（无记录时写默认 STRM/plugins、生成 webhook token）
  → gin：gzip（排除 SSE 路径）→ task.Manager{Start()} → api.Register → webhook.Register → registerWeb
  → ListenAndServe；Ctrl+C 直接退出（SSE 长连接不等待优雅关闭）
```

---

## 11. 测试

以包内单元测试为主，覆盖：OpenList 列表/分页、限速器、目录时间缓存、目录整理（番号解析/命名/整理计划）、Webhook 触发与 Emby 删除同步、任务停止（context 取消）、插件、版本比较。开发期另有 mock OpenList 服务器 + 冒烟脚本做全链路验证，正式运行不依赖。

---

## 12. 已知边界与后续扩展

- 驱动仅实现 OpenList/AList（`driver.New` 校验，接口已抽象，WebDAV/本地可扩展）
- `Rename` 已实现，供目录整理重命名使用
- 管理页为单 HTML 无构建产物；如需复杂 UI 可独立前端对接 REST API
- `strm_base` 指向的直链需对 Emby 可达；签名直链（OpenList `/d/` url 参数）未实现
- organize 的 `move` 模式 AV/FC2 均支持：AV 按首字母分库（`{库}/{首字母}/`），FC2 直接入库根（`{库}/`，无首字母分类）
