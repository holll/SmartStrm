# SmartStrm-Go

Go 语言实现的 STRM 生成工具，仅保留 STRM 相关功能：OpenList/AList 网盘目录 → 本地 `.strm` 文件映射，供 Emby/Jellyfin 等媒体服务器入库播放。

> 技术细节见 [docs/architecture.md](docs/architecture.md)
> 开源协议：MIT（见 [LICENSE](LICENSE)）

## 功能

- **存储管理**：OpenList/AList 驱动（列表 / 直链 / 删除 / 重命名）
- **任务管理**：Cron 定时、手动 / Webhook 触发、增量 / 同步生成、目录时间检查、**运行中可停止**、**任务日志实时刷新**（按任务隔离，增量轮询）
- **插件系统**（全局 + 任务级覆盖，5 个内置插件）：
  - STRM 内容替换（支持正则）
  - 自定义 STRM 文件名（`{name}.strm` / `{name}.({ext}).strm`）
  - 高级扫描过滤（覆盖全局后缀 / 大小范围）
  - 文件名关键词过滤（支持正则、筛选反转）
  - 任务请求延时（降低网盘风控）
- **Webhook**：
  - 任务触发：`POST /webhook/{token}` body `{"strmtask":"fc2,av"}`（对接 QAS / CloudSaver）
  - Emby 删除同步：`POST /webhook/emby/{token}`，路径映射 + 白名单防误删
- **工具箱**：批量替换已生成 STRM 内容（换域名 / 端口后无需重新生成）
- **存储浏览**：浏览存储目录、任务扫描路径可视化选择（任务编辑「浏览…」回填，任务行路径点击直达）
- **数据库（SQLite）**：配置存储（存储/任务/Webhook 全 UI 管理）、运行历史持久化（重启不丢失）、按天运行趋势统计、任务汇总、完整审计日志（登录/配置变更/运行/Webhook/Emby 删除）
- **Webhook 安全**：地址含随机 token（Emby 通知不支持鉴权头），泄露可在 UI 一键重新生成，旧地址立即失效
- **内置 Web 管理页**（Go embed，登录认证，无外部依赖）

## 快速开始

**无配置文件**，参数极简：

```bash
# 构建
go build -o build/smartstrm.exe ./cmd/server

# 运行（仅需指定端口）
./build/smartstrm.exe -port 8024
```

首次启动自动生成随机密码并打印到日志；访问 `http://localhost:8024` 以 `admin` 登录，登录后请在「系统设置 → 修改密码」中更换。

数据库固定 `data/data.db`（SQLite，纯 Go 无 CGO，自动创建 data 目录），可直接备份整个 `data/` 目录。

### 命令行参数

| 参数 | 说明 |
|---|---|
| `-port` | 监听端口，默认 `8024` |
| `-reset-password` | 重置 admin 密码：生成随机密码并打印，执行后退出（仅管理员单账号，不支持多用户） |

忘记密码：`./build/smartstrm.exe -reset-password`（打印新密码后登录修改）

## 配置管理（全部在 Web UI）

- **存储管理**：添加 OpenList/AList 存储（名称/地址/token）
- **任务管理**：创建任务（存储、扫描路径可用「浏览…」选择、Crontab、增量/同步、目录时间检查、任务级插件）
- **插件管理**：全局插件配置
- **系统设置**：STRM 生成设置（媒体后缀/大小阈值/复制后缀/生成根目录/URL 编码/生成类型/直链前缀）、**修改密码**（旧密码校验，改后全部登录失效）
- **系统设置 → Webhook**：
  - Webhook 地址含随机 token（Emby 通知不支持鉴权头），如泄露点击「重新生成地址」，旧地址立即失效
  - Emby 删除同步配置（strm_in_emby 前缀、路径映射、存储、白名单）

所有配置持久化于 SQLite，重启不丢失。

### 关于生成根目录

`save_dir` 默认 `./strm`（相对路径）。Go 程序无 Docker 挂载限制，目录可任意设置；Docker 部署时在「系统设置」中改为容器内可写路径（如挂载卷对应路径）。

## STRM 内容格式

路径兼容模式（默认）：

```
https://alist.example.com/d/115/FC2/A/ADN-468.mp4
```

文件名默认 `{name}.({ext}).strm`，可用 `custom_strm_name` 插件自定义（如 `{name}.strm`，注意自定义后丢失扩展名信息，Emby 同步删除功能失效）。

## Docker 部署要点

- `save_dir` 为容器内路径（默认 `/strm`），需将宿主机 STRM 目录挂载到容器并供 Emby 共享
- Emby 容器内 STRM 目录路径须与 `webhook.emby_delete_sync.strm_in_emby` 一致
- 配置文件挂载：`-v /host/config.yaml:/app/config.yaml`

## 项目结构

```
cmd/server/         入口 + 内置 Web 管理页(embed)
internal/config/    config.yaml 加载/保存
internal/driver/    OpenList/AList 驱动（分页列表、fs/get、remove、rename）
internal/plugins/   插件接口与 5 个内置插件
internal/generator/ STRM 生成核心（扫描→过滤→插件链→生成）
internal/task/      任务管理与 Cron 调度
internal/api/       管理 REST API（Bearer token 认证）
internal/webhook/   任务触发 + Emby 删除同步
mock/               mock OpenList 服务器 + 冒烟测试脚本
docs/               技术文档
```

## 构建

```bash
go build -o build/smartstrm.exe ./cmd/server
# 或使用 build.bat（多平台构建 + 版本注入）
```
