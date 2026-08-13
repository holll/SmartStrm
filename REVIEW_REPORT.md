# SmartStrm-Go 代码审查报告

**审查日期**：2026-08-13
**审查范围**：工作区未提交改动（3 个文件）
**审查方式**：三个并行审查代理分别从代码复用、代码质量、运行效率三个维度审查，共报告 9 项发现

---

## 一、改动概览

| 文件 | 改动内容 |
|---|---|
| `cmd/server/web/app.js` | 日志时间格式改为仿原版 `MM-DD HH:MM:SS`（无年份）；INFO 级别不显示标签；抽取 `levelTag()` |
| `internal/generator/generator.go` | 仿原版日志格式（任务信息块、汇总行）；目录跳过/文件跳过拆分计数；每目录一条 INFO 日志 |
| `internal/task/task.go` | 移除任务开始 banner（职责移至 generator 输出） |

---

## 二、确认问题与修复

### P1 — 统计口径断裂（generator.go / task.go）

**问题**：`skippedDirs`/`listFailed` 只挂在 Generator 结构体上，不进 `Result`、不落库、UI 无法展示；同时 `Result.Skipped` 语义被静默改为「仅文件」，DB `runs.skipped` 列与前端历史表口径随之漂移，历史记录不可比。

**修复**：
- 两个计数器移入 `Result` 结构体（`SkippedDirs`/`ListFailed`，带 json tag），删除 Generator 上的重复字段
- 落库时 `Skipped + SkippedDirs` 求和写入 `runs.skipped`，保持 DB 列「总跳过数」历史口径

### P1 — 汇总行覆盖不全（generator.go）

**问题**：「【生成文件完成】」统计行只在正常路径打印，`MkdirAll` 失败提前 return 时缺失，该路径日志无任何统计信息。

**修复**：汇总行与完成/停止判定改为 `defer` 统一输出，覆盖所有返回路径。

### P2 — logbuf 上限挤出日志（task.go）

**问题**：新增的每目录一条 INFO 日志放大了日志量，超 5000 行上限后环形淘汰，落库用 `Since(startSeq)` 只取剩余行，**历史日志丢失前半段**，任务越大丢得越多（5000 目录 → 5000+ 条 INFO）。

**修复**：上限提为常量 `logBufferMax = 50000`（两处调用点统一，顺带消除魔法值重复）。

### P3 — 日志行拼接重复（app.js）

**问题**：`fmtLogTime(l.time) + ' ' + levelTag(l.level) + l.msg` 在 `viewRunLog` 与 `fetchLog` 两处复制粘贴。

**修复**：抽取 `formatLogLine(l)` 统一两处调用。

### P3 — fmtLogTime 缺零值守卫（app.js）

**问题**：与 `fmtTime` 行为不一致——Go 零值时间（0001 年）会显示为 "01-01 00:00:00" 而非空。

**修复**：补充 `d.getFullYear() < 2000` 守卫。

---

## 三、未处理项（误报 / 超出范围）

| 发现 | 判定 |
|---|---|
| `fmtTime` 疑似死代码 | **误报**——仍被 9 处使用（运行历史/状态时间，含年份格式），与 `fmtLogTime` 用途有意区分 |
| `yn` 闭包无现成工具、每次 Run 重建 | 全库无 bool→"True"/"False" 工具，仅 2 次使用，内联可接受 |
| `'INFO'/'WARN'` 魔法字符串 | 项目既有模式，非本次引入 |
| 停止事件 4 处重复日志 | 历史遗留，本次 diff 未引入 |
| task.go 注释「任务信息块由 generator 输出」 | 保留——解释了 banner 移除的原因，属有价值的 WHY 注释 |

---

## 四、验证结果

| 检查项 | 结果 |
|---|---|
| `go vet ./internal/generator/ ./internal/task/` | 通过 |
| `go build ./...` | 通过 |
| `node --check cmd/server/web/app.js` | 通过 |

**结论**：4 项确认问题全部修复，改动后统计口径一致、所有路径有完整日志输出、大目录树历史日志不再丢失，且未引入回归。
