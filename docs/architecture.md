# SmartStrm-Go 技术文档

Go 语言实现的 STRM 生成工具（仅 STRM 相关功能），OpenList/AList 网盘目录映射为本地 `.strm` 文件，供 Emby/Jellyfin 等媒体服务器入库播放。

---

## 1. 总体架构

```
┌─────────────────────────── SmartStrm-Go ───────────────────────────┐
│                                                                     │
│   ┌──────────┐   ┌─────────┐   ┌──────────┐   ┌──────────┐        │
│   │ 内置 Web  │   │  REST    │   │  Webhook │   │  Cron    │        │
│   │ 管理页     │──▶│  API     │──▶│  处理    │   │  调度    │        │
│   │ (embed)   │   │ :8024   │   │          │   │          │        │
│   └──────────┘   └────┬─────┘   └────┬─────┘   └────┬─────┘        │
│                       │              │              │              │
│                       ▼              ▼              ▼              │
│                 ┌──────────────────────────────────────────┐       │
│                 │              task.Manager                │       │
│                 │  任务 CRUD / 运行状态 / 并发互斥          │       │
│                 └──────────────────┬───────────────────────┘       │
│                                    ▼                               │
│                 ┌──────────────────────────────────────────┐       │
│                 │           generator.Generator            │       │
│                 │  扫描 → 过滤 → 插件链 → 生成 .strm        │       │
│                 └──────────────────┬───────────────────────┘       │
│                                    ▼                               │
│                 ┌──────────────────────────────────────────┐       │
│                 │          driver.Driver (OpenList)        │       │
│                 │  List / GetDirectLink / Remove / Rename  │       │
│                 └──────────────────┬───────────────────────┘       │
└────────────────────────────────────┼────────────────────────────────┘
                                     ▼
                       OpenList/AList 服务（/api/fs/*）
```

**分层职责**：

| 层 | 包 | 职责 |
|---|---|---|
| 入口 | `cmd/server` | 装配、配置加载、embed 管理页注册 |
| 接口 | `internal/api` | 管理 REST API + Bearer token 认证 |
| 接口 | `internal/webhook` | 外部触发（任务触发 / Emby 删除同步） |
| 调度 | `internal/task` | 任务模型、Cron 调度（robfig/cron）、运行状态 |
| 核心 | `internal/generator` | STRM 生成主流程 |
| 扩展 | `internal/plugins` | 插件接口 + 5 个内置插件 |
| 驱动 | `internal/driver` | 存储驱动抽象与 OpenList 实现 |
| 数据 | `internal/config` | config.yaml 加载 / 保存（运行时热改） |

**设计原则**：
- 配置即持久化：任务/存储/插件配置全部存于 `config.yaml`，API 修改后写回并热重载 Cron
- 无数据库、无前端构建链：单二进制交付（管理页 Go embed）
- 驱动接口隔离：生成器只依赖 `driver.Driver` 接口，便于未来扩展驱动

---

## 2. STRM 生成主流程

```
触发（Cron / Webhook / UI / 手动）
        │
        ▼
EnvForTask：合并全局+任务级插件配置 → 应用 scan_filter 覆盖 → 应用 task_delay
        │
        ▼
walkDir(根路径, "", mtime=0)                    递归：
  ├─ driver.List(path)（分页 200/页）            1. 列出目录
  ├─ 请求延时（task_delay.list_delay）           2. 延 时
  ├─ 记录目录 mtime → dir_cache.json            3. 目录时间检查缓存
  └─ 遍历条目：
       ├─ 插件过滤（skip_keyword.ShouldSkip）    4. 关键词过滤
       ├─ 目录：dir_time_check 对比缓存跳过      5. 目录时间检查
       │        └─ 递归 walkDir
       ├─ 媒体文件（后缀∈media_ext 且 size≥阈值）
       │        ├─ 命名：默认 {name}.({ext}).strm → custom_strm_name 覆盖
       │        ├─ 内容：{base}/d/{路径} 或 fid 模式 fs/get 直链
       │        ├─ URL 编码（可选）
       │        └─ content_replace 插件链修改内容
       │        └─ 写入本地文件 → remoteSet 登记
       └─ 刮削文件（后缀∈copy_ext）
                ├─ 大小一致则跳过（复用）
                └─ 下载到本地（/d/ 直链，重名加 .tmp 原子写入）
        │
        ▼
非增量模式：cleanupLocal() 删除 remoteSet 之外的本地文件
（keep_local_asset=true 时保留非 .strm 文件；.smartstrm 元数据目录不清理）
```

### 2.1 生成内容格式

| 模式 | 内容 | 说明 |
|---|---|---|
| path（默认） | `{strm_base}/d{远端路径}` | 直接走 OpenList `/d/` 下载直链，兼容性好 |
| fid | `driver.GetDirectLink(path)` 返回的 `raw_url` | 调用 `/api/fs/get`，获取失败回退 path 模式 |

- `strm_base` 留空时使用存储自身地址（`存储.URL`）
- `url_encode=true` 时对路径逐段 `url.PathEscape`（保留 `/`）

### 2.2 目录时间检查

- 每次扫描后把目录 mtime 写入 `{任务目录}/.smartstrm/dir_cache.json`（`map[远端路径]unix时间戳`）
- 下次运行：`dir_time_check=true` 且远端 mtime ≤ 缓存值 → 跳过递归
- mtime 解析失败（零值）时**不跳过**，保证安全

### 2.3 增量 vs 同步

| 模式 | 行为 |
|---|---|
| incremental=true | 只生成/更新新增文件，不清理 |
| incremental=false | 同步：扫描结束后清理本地 `remoteSet` 之外的 `.strm`（远端已删除的联动清理） |

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
type Filter          interface { ShouldSkip(env *Env, name string, isDir bool) bool }
type ContentModifier interface { ModifyContent(env *Env, content string) string }
type NameFormatter   interface { FormatName(env *Env, name, ext string) string }
type Delay           interface { ListDelay(env *Env) time.Duration; CopyDelay(env *Env) time.Duration }
type ScanOverride    interface { Override(env *Env, mediaExt []string, mediaSize int, copyExt []string) ([]string, int, []string) }
```

### 3.2 配置合并规则

```
合并配置 = 全局 Plugins map ∪ 任务级 Plugins map（任务级覆盖同名插件）
env.PluginConfig(p) = 合并后 [p.ID()] 对应配置
```

- 插件未在任务级配置时继承全局配置
- 任务级只写 `{enabled:true}` 时仅启用，其余字段沿用全局

### 3.3 内置插件

| ID | 名称 | 钩子 | 配置项 | 说明 |
|---|---|---|---|---|
| `content_replace` | STRM内容替换 | ContentModifier | enabled, regex_mode, find_text, replace_text | 正则/纯文本替换 STRM 内容（换域名、指向 302 服务） |
| `custom_strm_name` | 自定义STRM文件名 | NameFormatter | enabled, custom_name | 模板变量 `{name}`（文件名）、`{ext}`（原扩展名） |
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
    GetDirectLink(ctx, path) (string, error)           // /api/fs/get raw_url
    Remove(ctx, path) error                            // /api/fs/remove
    Rename(ctx, path, newName string) error            // /api/fs/rename（预留）
}
```

### 4.2 OpenList 实现要点

- 认证：`Authorization: {token}`（openlist- 前缀 token，AList 兼容）
- 列表分页：`per_page=200` 循环翻页，不足 200 条终止
- `Data == null` → `ErrNotFound`
- 错误统一：非 200 code 返回 `code + message`

---

## 5. HTTP API

认证：`POST /api/login` 换取 Bearer token（内存有效期 7 天），请求头 `Authorization: Bearer {token}`。

### 5.1 管理 API（/api）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/login` | 登录，body `{username, password}` → `{token}` |
| POST | `/api/logout` | 注销 |
| GET / PUT | `/api/settings` | 读取 / 保存 STRM 设置（媒体后缀、阈值、复制后缀、save_dir、url_encode、gen_type、strm_base）与 Webhook 配置 |
| GET / POST | `/api/storages` | 存储列表 / 新增 |
| PUT / DELETE | `/api/storages/:name` | 修改 / 删除存储 |
| GET | `/api/storages/:name/list?path=/` | 存储浏览（驱动 List） |
| GET / POST | `/api/tasks` | 任务列表（含 next_run、running）/ 新增 |
| PUT / DELETE | `/api/tasks/:name` | 修改 / 删除（触发 Cron 热重载） |
| POST | `/api/tasks/:name/run` | 立即运行（异步，重复触发返回 409） |
| POST | `/api/tasks/run_all` | 运行所有任务 |
| GET | `/api/tasks/status` | 运行状态与结果（生成/复制/清理/跳过/错误） |
| POST | `/api/tasks/:name/stop` | 停止运行中的任务（context 取消，网络请求一并中断） |
| GET | `/api/tasks/:name/log?after={seq}` | 任务日志增量拉取，返回 `{after, lines:[{seq,time,level,msg}]}` |
| POST | `/api/tasks/:name/strm_replace` | 批量替换该任务已生成 STRM 内容，body `{find_text, replace_text, regex_mode}` |
| GET / PUT | `/api/plugins` / `/api/plugins/:id` | 插件列表 / 全局插件配置 |
| GET | `/api/webhook/info` | Webhook 地址信息 |
| GET | `/api/runs?limit=50` | 最近运行记录（数据库持久化） |
| GET | `/api/runs/:id/log` | 某次运行的完整日志（数据库） |
| GET | `/api/tasks/:name/history?limit=20` | 任务运行历史 |
| GET | `/api/stats/daily?days=30` | 按天聚合（运行次数/成功/失败/停止/生成量） |
| GET | `/api/stats/tasks` | 任务汇总（总运行/成功/生成） |
| GET | `/api/audit?limit=100` | 审计日志（登录/配置变更/运行/Webhook） |

### 5.2 Webhook（无认证之外的公开端点，token 即密钥）

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/webhook/{token}` | 任务触发，body `{"strmtask":"fc2,av"}`；也可 `{"task":"fc2"}` |
| POST | `/webhook/emby/{token}` | Emby 删除同步（见下） |

### 5.3 Emby 删除同步流程

```
Emby 通知（item.deleted / ItemDeleted / library.deleted）
        │  {"Event": "...", "Item": {"Path": "/data/docker/smartstrm/strm/FC2/A/ADN-468.strm"}}
        ▼
1. 前缀校验：Path 必须以 strm_in_emby 开头（防任意路径）
2. .strm 文件取父目录（统一按文件夹删除）
3. 映射：storage_path_map + 相对路径 → /115/FC2/A
4. 白名单：必须命中 allowed_prefix 之一（防误删）
5. driver.Remove(存储, 远端目录)
```

### 5.4 并发、停止与日志

- `task.Manager` 用 `running map` 互斥：同一任务并发触发返回 `任务正在运行中`
- Cron 链 `SkipIfStillRunning`：定时触发时跳过重叠运行
- 生成器 `remoteSet` 为单次运行内存态，不跨运行持久化
- **停止**：每次 `Run` 创建 `context.WithCancel` 存入 `cancel map`；`Stop` 调用 cancel → 生成器 `walkDir` 每轮检查 `ctx.Err()` 中断，进行中的网络请求一并取消；已停止任务的错误信息固定为「任务已停止」
- **任务日志**：`task.LogBuffer` 环形缓冲（上限 5000 行，按任务隔离，每次运行开始时重置）；生成器通过 `SetLogger` 注入，记录扫描/生成/复制/清理/过滤/停止/错误；前端轮询 `?after={seq}` 增量追加，自动滚底，运行中自动选中第一个运行中的任务

---

## 6. 数据与配置

### 6.1 config.yaml（运行时持久化）

- 首次启动自动生成默认配置（含随机 Webhook token）
- API 修改立即 `Save()` 写回，任务增删改触发 `mgr.Reload()` 重注册 Cron
- 配置热更新不重启进程

### 6.2 SQLite 数据库（smartstrm.db，配置文件同目录）

| 表 | 内容 | 用途 |
|---|---|---|
| `runs` | 每次运行记录（任务/起止时间/状态/生成统计/错误） | 运行历史、统计报表、趋势查询 |
| `task_logs` | 按 run_id 关联的完整日志段 | 历史日志回看（实时增量仍走内存缓冲） |
| `audit_logs` | 登录/配置变更/运行操作/Webhook/Emby 删除审计 | 审计追责 |

- 驱动：`modernc.org/sqlite`（纯 Go，无 CGO，Windows 部署友好）
- 单写者串行（`SetMaxOpenConns(1)`），无锁冲突
- 运行记录创建（running）→ 结束更新（success/error/stopped）；stopped 判定须在 `cancel()` 之前检查 `ctx.Err()`
- 时间统一存储为 `"2006-01-02 15:04:05"` 字符串（SQLite `date()` 可聚合），读取经驱动自动转回 `time.Time`
- 备份 = 复制 smartstrm.db 文件

### 6.3 本地产物

```
{save_dir}/{任务名}/
├── .smartstrm/dir_cache.json   # 目录时间检查缓存（清理时保留）
├── 目录结构镜像远端/
│   ├── xxx.(mp4).strm          # 生成的 STRM（默认命名）
│   └── xxx.nfo / xxx.jpg       # 复制的刮削文件
```

---

## 7. 安全设计

| 面 | 措施 |
|---|---|
| 管理 API | Bearer token 登录认证（配置账号密码） |
| Webhook | 路径 token 鉴权（`/webhook/{token}`），泄露可在配置中更换 |
| Emby 删除 | 前缀校验 + 路径映射 + 白名单三重防护 |
| UI XSS | `esc()`（HTML 文本）与 `jsAttr()`（onclick 参数）双转义函数；所有用户可控字段拼接前转义 |
| 文件写入 | 下载用 `.tmp` + rename 原子写，失败不留残片 |

---

## 8. 测试

开发期使用自研 mock OpenList 服务器 + 冒烟脚本做过全链路验证（生成/过滤/复制/删除同步/Webhook 触发/目录时间检查），已随正式代码剔除（正式运行不依赖）。

---

## 9. 已知边界与后续扩展

- 驱动仅实现 OpenList/AList（接口已抽象，WebDAV/本地可扩展）
- `Rename` 接口保留但插件未使用（非法文件名修正不在范围内）
- 管理页为单 HTML 无构建产物；如需复杂 UI 可独立前端对接 REST API
- `strm_base` 指向 OpenList 需对 Emby 可达；如需签名直链可扩展 `url` 参数（当前未实现）
