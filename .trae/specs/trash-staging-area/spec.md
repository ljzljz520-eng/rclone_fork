# 删除文件暂存区（回收站） - 产品需求文档

## Overview
- **Summary**: 为 rclone 增加"删除文件暂存区"（回收站）能力。启用后，被删除的文件不再被永久删除，而是移动到同一 remote 下的隐藏暂存目录；暂存区支持配置保留时间与容量上限，并提供一键恢复、查看、清理、清空命令及对应 rc 接口。
- **Purpose**: 防止误删数据。rclone 的 delete/purge/sync/mount 删除目前不可恢复（仅 sync 场景有 `--backup-dir` 可保留被覆盖文件，但无保留期限、容量管理和恢复手段）。
- **Target Users**: 所有使用 rclone 删除/同步/挂载云存储的用户，尤其是无原生回收站的后端（如 S3、B2、SFTP、本地磁盘）用户。

## Goals
- 删除操作（delete/deletefile/ncdu/purge/sync 删除/mount 内删除）可将文件移入暂存区而非永久删除。
- 暂存区位于**同一 remote、同一后端链路**下的隐藏目录（默认 `.rclone-trash`），删除优先走服务端 Move，不支持时回退 copy+delete。
- 可配置保留时间（默认 7 天）与容量上限（默认不限）；过期/超量条目自动或手动清理。
- 一键恢复：`rclone trash restore` 将暂存文件移回原路径；原路径冲突时默认跳过并报告，`--overwrite` 可覆盖。
- 提供 list / restore / cleanup / empty 四个子命令与对应 rc 端点。
- 完全向后兼容：未启用时所有行为不变；尊重 `--dry-run` 与 `-i/--interactive`。

## Non-Goals
- 不做跨 remote 的统一暂存区（不实现 `--backup-dir` 式的独立暂存 remote）。
- 不做本地磁盘暂存。
- 不封装/统一各后端原生回收站（如 Google Drive trash）；`rclone cleanup` 语义不变。
- 不引入常驻守护进程/定时任务；过期清理采用"懒清理 + 显式命令"。
- 不改动自动生成的文档（`make commanddocs` 产物）；网站文档不在本次范围。
- bucket 型后端（如 S3）在 fs 根为空（未指定 bucket）时暂存不可用，需在文档/报错中说明，不为个别后端写特例 hack。

## Background & Context
- 删除链路核心：[fs/operations/operations.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go) 中 `DeleteFileWithBackupDir`（L557）、`DeleteFilesWithBackupDir`（L600）、`Delete`（L1184）、`Purge`（L1154）。`delete`/`deletefile`/`ncdu` 命令、sync 删除通道（`fs/sync/sync.go` L608/L665）、bisync 均汇聚于此。
- `Purge`（L1154）：后端支持原生 Purge 时走服务端批量删除；否则 fallback 为 `DeleteFiles` + `Rmdirs`。
- VFS（mount）删除直接调用 `f.o.Remove(f.ctx)`（[vfs/file.go](file:///Users/kkcarrot/swe-project/rclone_fork/vfs/file.go) L677）；vfs 已导入 `fs/operations`，无循环依赖。
- `sync.go` L467/L515 的 `DeleteFile` 是 `move` 命令复制成功后对**源文件**的删除，不应被暂存拦截（否则 move 失去意义）。
- 已有 `--backup-dir`/`--suffix` 机制（sync/copy 场景把被覆盖/删除文件移走）；本特性与其共存：显式设置了 backupDir 的 sync 删除仍走 backupDir。
- 全局 flags 由 `fs/config.go` 的 `Options` 条目自动注册（如 `backup_dir`、`max_delete_size`），可写入 rclone.conf。
- 暂存区 fs 可通过 `fs.ConfigString(f)` 重建路径串后用 `fs/cache.Get` 获取（与 backupDir 同法），天然保持 crypt 等包装链。
- 过滤器支持运行时追加规则：`filter.GetConfig(ctx).AddRule("- 目录名/**")`（[fs/filter/filter.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/filter/filter.go) L377）。
- 用户已确认：暂存位置=同一 remote 隐藏目录；拦截范围=文件删除命令 + sync 删除 + mount 删除 + purge；恢复冲突=跳过并报告；默认保留 7 天、容量不限。

## Functional Requirements

- **FR-1 配置项**：新增全局配置/flags：
  - `--trash`（bool，默认 false）：启用暂存区。
  - `--trash-dir`（string，默认 `.rclone-trash`）：暂存目录名，位于被操作 fs 根目录下。
  - `--trash-retention`（duration，默认 `7d`）：条目保留时长；`0` 表示永不过期。
  - `--trash-max-size`（SizeSuffix，默认 `-1` 不限）：暂存区总容量上限；超出时按删除时间从旧到新驱逐条目，直至低于上限。
  - `--trash-cleanup-interval`（duration，默认 `1h`）：懒清理节流间隔；`0` 关闭自动清理。
- **FR-2 暂存布局**：暂存条目路径为 `<trash-root>/<YYYY-MM-DD>/<HHMMSS>-<16位随机串>/<原始相对路径>`。其中：
  - `<trash-root>` = 被操作 fs 根 + `/` + `--trash-dir`；
  - 日期目录为删除当天 UTC 日期；条目目录编码精确删除时间（用于容量驱逐排序）；
  - 原始相对路径完整保留（含子目录），同名文件同日多次删除互不覆盖。
- **FR-3 删除拦截**：
  - `DeleteFileWithBackupDir`：`backupDir != nil` 时维持现状；否则若 `--trash` 启用、当前 ctx 未标记"暂存旁路"、且对象不属于暂存区 fs，则将文件 Move 进暂存区（`operations.Move`，服务端优先、copy+delete 兜底），否则按原逻辑永久删除。
  - `Purge`：`--trash` 启用且对当前 fs 生效时，强制走 fallback 路径（列举全部对象→移入暂存区→Rmdirs），不调用后端原生 Purge；暂存目录本身受过滤器排除，不被列举/删除。
  - VFS `File.Remove`/`RemoveAll`：改调 operations 的暂存感知删除路径（保留现有 cache 清理逻辑）。
  - sync 中 move 源文件删除（sync.go L467/L515）：以"暂存旁路"ctx 调用，保持永久删除语义。
- **FR-4 防循环保护**：
  - 启用 `--trash` 时，向当前运行的全局过滤器追加排除规则（`- <trash-dir>/` 与 `- <trash-dir>/**`），使 delete/sync/march 等遍历不进入暂存目录。
  - 暂存区内部操作（移入、恢复覆盖）通过 ctx 标记"暂存旁路"，其间发生的删除为永久删除，不会二次进入暂存区。
- **FR-5 懒清理**：进程内首次执行移入暂存时，若距上次清理超过 `--trash-cleanup-interval`，先执行一次清理（过期驱逐 + 容量驱逐）；清理失败只记日志、不阻断删除。CLI 进程内至多一次；mount/rcd 长进程按间隔节流。
- **FR-6 清理规则**：
  - 过期驱逐：条目删除时间早于 `now - --trash-retention` 时永久删除（retention=0 跳过）。
  - 容量驱逐：统计暂存区全部条目字节数，若超过 `--trash-max-size`，按删除时间从旧到新永久删除条目，直到总量 ≤ 上限（max-size<0 跳过）。
- **FR-7 命令组 `rclone trash`**：
  - `rclone trash list remote:path [--json]`：列出该 fs 根对应的暂存区条目（原始路径、删除时间、大小），支持 `--json` 机器可读输出。
  - `rclone trash restore remote:path [--all] [pattern...] [--overwrite]`：一键恢复。默认/`--all` 恢复全部条目；可给路径模式过滤；原路径已存在同名文件时跳过并在汇总中报告，`--overwrite` 时永久删除目标后移回；恢复成功的条目从暂存区移除。
  - `rclone trash cleanup remote:path`：立即执行过期+容量驱逐，输出驱逐条目数与释放字节数。
  - `rclone trash empty remote:path`：永久清空整个暂存区（尊重 `--dry-run`/`-i`）。
  - 所有子命令尊重 `--dry-run` 与 `-i/--interactive`，并通过 `cmd.Run` 获得标准统计与错误处理。
- **FR-8 rc 端点**（注册于 fs/operations，参数与命令等价）：
  - `operations/trashlist`（fs, remote, opt.json）
  - `operations/trashrestore`（fs, remote, all, patterns, overwrite）
  - `operations/trashcleanup`（fs, remote）
  - `operations/trashempty`（fs, remote）
- **FR-9 可观测性**：移入暂存区记日志 `Moved into trash`（对象级 Infof）；统计仍计入删除计数（保守、不改变 max-delete 安全语义）；恢复/清理/清空输出操作条目数与字节数。
- **FR-10 注册**：新命令包 `cmd/trash` 通过 `cmd/all/all.go` 空导入注册。

## Non-Functional Requirements

- **NFR-1 兼容性**：未设置 `--trash` 时，所有命令/rc/vfs 路径的可观测行为与现状完全一致；不改变任何现有 flag/rc 的语义。
- **NFR-2 后端通用性**：核心逻辑只依赖 `fs.Fs`/`fs.Object` 通用接口与 `operations.Move`（已有服务端移动/copy+delete 回退），不在 fs/vfs 核心写任何后端特例。
- **NFR-3 性能**：懒清理每进程至多按间隔触发一次全量暂存区列举；逐文件删除路径不引入额外列举；清理驱逐复用 `DeleteFiles` 并发通道。
- **NFR-4 测试**：新增逻辑配套单元/集成测试（local 后端，沿用 fstest 脚手架），覆盖移入、布局解析、list、restore（含冲突/覆盖）、过期驱逐、容量驱逐、防循环、dry-run、未启用回归。
- **NFR-5 代码风格**：遵循 AGENTS.md：godoc 注释、最小改动、复用 `lib`/现有例程、命令文档写在源码 Long 字段。

## Constraints
- **Technical**：Go；全局 flag 走 `fs/config.go` Options；命令走 cobra；rc 走 `rc.Add`；过滤器规则经 `filter.GetConfig(ctx).AddRule`；fs 实例经 `fs/cache.Get` 获取；不引入新依赖。
- **Business**：向后兼容优先；不破坏现有后端集成测试语义。
- **Dependencies**：仅 rclone 现有内部包（fs、fs/operations、fs/cache、fs/filter、fs/accounting、cmd、vfs、lib/atexit 等）。

## Assumptions
- 暂存区按"被操作 fs 根"隔离（类似 freedesktop 每卷 `.Trash` 约定）：对 `remote:bucket/path` 操作产生的暂存区位于 `remote:bucket/path/.rclone-trash`；`rclone trash restore remote:bucket/path` 操作同一暂存区。这是唯一对 local/S3/目录型后端都统一的规则。
- 暂存目录名 `.rclone-trash` 为保留名：启用暂存后该名称目录在所有遍历中被排除（即使用户数据中恰有同名目录）。
- 时间戳用 UTC；条目排序以条目目录名编码的删除时间为准。
- 恢复目标 fs 为暂存 fs 的父级 fs（由暂存 fs 配置串去掉尾部 `/<trash-dir>` 重建），同链路服务端 Move 回原位。
- `--trash` 可写入 rclone.conf 长期启用；`--dry-run` 下所有破坏性动作仅打印不执行。

## Acceptance Criteria

### AC-1: 启用暂存后删除文件进入暂存区
- **Type**: `rule`
- **Given**: 已配置 `--trash` 的 rclone，remote 上存在文件 `dir/file.txt`
- **When**: 执行 `rclone delete remote:path`（或 deletefile/ncdu 删除）
- **Then**: 原路径文件消失；`remote:path/.rclone-trash/<日期>/<条目>/dir/file.txt` 存在且内容与原文件一致
- **Pass Condition**: 测试断言原路径 NewObject 返回 ErrorObjectNotFound，暂存路径对象存在且大小/内容一致
- **Evidence**: `fs/operations/trash_test.go` 测试输出

### AC-2: 暂存条目可列出且原始路径可解析
- **Type**: `rule`
- **Given**: 暂存区中存在若干条目
- **When**: 执行 `rclone trash list remote:path` 与 `operations/trashlist`
- **Then**: 每个条目输出原始相对路径、删除时间、大小；`--json`/rc 返回等价结构化结果
- **Pass Condition**: 列表条目数 == 已删除文件数，原始路径字段等于删除前路径
- **Evidence**: trash_test.go 断言 + 命令手工验证输出

### AC-3: 一键恢复与冲突处理
- **Type**: `rule`
- **Given**: 暂存区有条目，且部分原路径已被同名文件占用
- **When**: 执行 `rclone trash restore remote:path`（默认恢复全部）
- **Then**: 未冲突条目移回原路径且内容一致并从暂存区移除；冲突条目保留在暂存区并在汇总中报告为 skipped；加 `--overwrite` 后冲突条目也恢复（原文件被永久覆盖）
- **Pass Condition**: 恢复后非冲突文件校验一致；skipped 计数正确；`--overwrite` 场景原文件被暂存版本替换
- **Evidence**: trash_test.go 恢复/冲突/覆盖三组断言

### AC-4: 保留时间驱逐
- **Type**: `rule`
- **Given**: 暂存区中存在删除时间分别为 10 天前和 1 天前的条目，`--trash-retention=7d`
- **When**: 执行 `rclone trash cleanup`（或触发懒清理）
- **Then**: 10 天前条目被永久删除，1 天前条目保留；`retention=0` 时均不按时间删除
- **Pass Condition**: cleanup 后 list 仅含未过期条目；dry-run 下不删除
- **Evidence**: trash_test.go 过期驱逐断言

### AC-5: 容量上限驱逐
- **Type**: `rule`
- **Given**: 暂存区总大小超过 `--trash-max-size`，条目删除时间可排序
- **When**: 执行清理（显式或懒触发）
- **Then**: 最旧条目被优先永久删除，直到总量 ≤ 上限；`--trash-max-size=-1` 时不按容量驱逐
- **Pass Condition**: 清理后暂存区总字节 ≤ 上限（或剩余条目不足）；被删条目为最旧批次
- **Evidence**: trash_test.go 容量驱逐断言

### AC-6: 拦截覆盖面
- **Type**: `rule`
- **Given**: `--trash` 启用
- **When**: 分别执行 (a) `rclone sync` 导致目标端文件被删、(b) mount 内删除文件（vfs `File.Remove`）、(c) `rclone purge remote:path/sub`、(d) `rclone move remote:A remote:B`
- **Then**: (a)(b)(c) 的文件均出现在对应暂存区且原路径不可见；(d) 源 remote 上文件被永久删除（不进源端暂存区），目标端正常
- **Pass Condition**: 四个场景的路径存在性断言全部符合
- **Evidence**: trash_test.go / vfs 测试 / sync 测试断言

### AC-7: 防循环与防覆盖
- **Type**: `rule`
- **Given**: `--trash` 启用且暂存区已有内容
- **When**: 再次执行 `rclone delete remote:path`（遍历会经过暂存目录）；并同日两次删除同名同路径文件
- **Then**: 暂存目录内容不被二次移入；两次删除产生两个独立条目，旧条目内容不丢失
- **Pass Condition**: 暂存区条目数增加 1（非倍增）；两个条目内容分别对应两次删除的文件内容
- **Evidence**: trash_test.go 循环删除断言

### AC-8: rc 端点与命令等价
- **Type**: `rule`
- **Given**: rc/rcd 可用
- **When**: 调用 `operations/trashlist`、`operations/trashrestore`、`operations/trashcleanup`、`operations/trashempty`
- **Then**: 返回结构包含 items/统计字段，行为与对应命令一致，参数缺失时返回参数错误
- **Pass Condition**: rc 调用返回成功且远端状态变化与命令路径一致
- **Evidence**: rc_test.go 断言

### AC-9: 未启用时完全向后兼容
- **Type**: `rule`
- **Given**: 未设置 `--trash`
- **When**: 运行现有 delete/purge/sync/move 相关测试
- **Then**: 行为与改动前完全一致
- **Pass Condition**: `go build ./...` 通过；`fs/operations`、`fs/sync`、`vfs`、`cmd` 相关既有测试全部通过
- **Evidence**: 测试运行输出

### AC-10: dry-run / interactive 安全
- **Type**: `rule`
- **Given**: `--trash` 启用
- **When**: 带 `--dry-run` 执行 delete、trash restore、trash cleanup、trash empty
- **Then**: 仅打印计划动作，不发生任何移动/删除
- **Pass Condition**: dry-run 后远端对象状态与执行前完全一致
- **Evidence**: trash_test.go dry-run 断言

### AC-11: 实现质量与 rclone 风格契合度
- **Type**: `rubric`
- **Dimension**: 代码质量（最小侵入、godoc 完整、复用现有例程、命名/日志风格一致、无后端特例）
- **Scale**: 1-5
- **Anchors**: 1 = 在核心包写后端特例或大面积重构；3 = 功能正确但有重复代码/注释缺失；5 = 改动集中、复用 Move/DeleteFiles/cache/filter 等现有机制、注释符合 AGENTS.md 规范
- **Pass Threshold**: >= 4
- **Evidence**: 独立审查代码阅读 + golangci-lint 输出
