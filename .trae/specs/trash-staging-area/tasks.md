# 删除文件暂存区（回收站） - 实施计划

## Task 1: 全局配置项与基础设施
- **Status**: `completed`
- **Priority**: high
- **Depends On**: None
- **Description**:
  - 在 [fs/config.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/config.go) 的 `Options` 中新增 5 个条目并在 `ConfigInfo` 增加字段：
    - `trash`（bool，默认 false，Groups: Important）
    - `trash_dir`（string，默认 `.rclone-trash`，Groups: Important）
    - `trash_retention`（Duration，默认 7d，Groups: Important）
    - `trash_max_size`（SizeSuffix，默认 -1，Groups: Important）
    - `trash_cleanup_interval`（Duration，默认 1h，Groups: Important）
  - ctx 旁路标记 `trashBypassCtx`/导出 `TrashBypassCtx`、`isTrashBypassed`（fs/operations/trash.go）。
  - 过滤器排除规则：`TrashFilterCtx(ctx)`（ctx 内过滤器副本加 `- <trashdir>/**`，不污染全局）、`trashPurgeCtx(ctx)`（include-all + 暂存排除，供 Purge 使用）。
- **Acceptance Criteria Addressed**: AC-9
- **Test Requirements**:
  - `rule` TR-1.1: `go build ./...` 通过；`rclone help flags` 出现 5 个新 flag 且默认值正确
  - `rule` TR-1.2: 未启用 `--trash` 时既有测试全部通过
- **Completion Evidence**:
  - `rclone help flags` 输出 5 个 flag：`--trash`、`--trash-cleanup-interval Duration (default 1h0m0s)`、`--trash-dir string (default ".rclone-trash")`、`--trash-max-size SizeSuffix (default off)`、`--trash-retention Duration (default 168h0m0s)`。
  - 全量 `go test ./...` 除环境缺失 flock 的 TestS3Minio 外全部通过（TR-8.2 证据）。

## Task 2: 暂存核心 —— fs 解析、布局编解码、移入
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 1
- **Description**:
  - `trashFsFor(ctx, f)`：`fs.ConfigString(f)` + `fspath.JoinRootPath` + `fs/cache.Get` 派生暂存 fs（保持 crypt 等包装链）。
  - `trashEntryRemote(now, orig)` → `<UTC日期>/<HHMMSS>-<16位随机>/<原始路径>`；`parseTrashEntry` 解码。
  - `trashMove`：解析 trashFs → 节流懒清理 → `Move(trashBypassCtx, trashFs, nil, entry, dst)`（服务端优先、copy+delete 兜底；dry-run 由 Move 的 SkipDestructive 保证）。
- **Acceptance Criteria Addressed**: AC-1, AC-7, AC-10
- **Test Requirements**:
  - `rule` TR-2.1: 编解码往返一致
  - `rule` TR-2.2: trashMove 后原路径消失、暂存路径内容一致
  - `rule` TR-2.3: 同名两次删除产生两个条目
  - `rule` TR-2.4: dry-run 不移动
- **Completion Evidence**:
  - `TestTrashEntryLayout`/`TestTrashEntryLayoutRejects` 通过（TR-2.1）。
  - `TestTrashDeleteAndList`、CLI 冒烟（delete 后文件位于 `.rclone-trash/<日期>/<条目>/...` 且内容一致）通过（TR-2.2/2.3/2.4）；`TestTrashDryRun` 通过。

## Task 3: 删除链路接线
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 2
- **Description**:
  - `DeleteFileWithBackupDir`：backupDir 优先；否则 `trashActive && !trashExcluded(...)` 时 `trashMove`，action 日志 "move into trash"/"Moved into trash"。
  - `Purge`：trash 生效时跳过后端原生 Purge，用 trashPurgeCtx（include-all + 排除暂存）走 fallback：DeleteFiles→移入暂存，Rmdirs leaveRoot=true 保留含暂存区的根目录。
  - sync move 源删除（sync.go 两处）与 operations.move 回退源删除：旁路 ctx，保持永久删除语义；`MoveBackupDir` 整体旁路（backup-dir 优先时不重复进暂存）。
  - vfs `File.Remove`：改调 `operations.DeleteFile(f.ctx, f.o)`，保留 cache 清理逻辑。
  - 防循环：`trashExcluded`（fs rooted at trash 或对象 remote 以 trashdir 为前缀→硬删除）+ ctx 旁路 + 过滤器排除。
- **Acceptance Criteria Addressed**: AC-6, AC-7, AC-9, AC-10
- **Test Requirements**:
  - `rule` TR-3.1~TR-3.5: delete/purge/move 旁路/vfs/暂存内硬删除
- **Completion Evidence**:
  - `TestTrashPurge`、`TestTrashSyncDeletions`、`TestTrashMoveSemantics`（覆盖目的端覆盖文件进暂存、源文件永久删除）、`TestTrashDeleteInsideTrashIsPermanent`、`TestTrashAntiLoop`、`TestTrashDisabled` 通过。
  - CLI 冒烟：`purge --trash`、`sync --trash`、`move --trash` 行为符合预期；修复了 Purge fallback 初始化回归（doFallbackPurge 默认 true）。
  - vfs 全套测试通过（`go test ./vfs/` ok 16.6s）。

## Task 4: 清理引擎 —— 过期驱逐、容量驱逐、懒清理
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 2
- **Description**:
  - `trashListEntries`：ListR 列举暂存 fs（先根 List 探测，不存在视为空，避免 walk 计数错误），按条目聚合 delTime/objs/size。
  - `TrashCleanup`/`trashCleanupFs`：retention>0 过期驱逐；max-size>=0 时按 delTime 升序驱逐至 ≤ 上限；bypass 永久删除；best-effort rmdir 空条目/日期目录。
  - `TrashEmpty`：清空暂存 + Rmdirs。
  - 懒清理：`trashAutoCleanup` 按 Fs 配置串节流（`--trash-cleanup-interval`，0 关闭），失败仅 Debugf 不阻断。
- **Acceptance Criteria Addressed**: AC-4, AC-5, AC-10
- **Test Requirements**:
  - `rule` TR-4.1~TR-4.5
- **Completion Evidence**:
  - `TestTrashCleanupRetention`（2001 旧条目被驱逐、1 天前保留、retention=0 不驱逐）、`TestTrashCleanupMaxSize`（5+10+15=30、上限 20 时保留 15）、`TestTrashEmpty`、`TestTrashDryRun`（dry-run 计数但不删）通过；CLI 冒烟（手工构造 2020 旧条目驱逐、30B 上限驱逐 2 个）通过。

## Task 5: list 与 restore 核心
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 2
- **Description**:
  - `TrashItem{OrigPath, DelTime, Size}` + `TrashList`（按删除时间排序）。
  - `TrashRestore(ctx, f, patterns, all, overwrite)`：patterns 用 `filter.GlobPathToRegexp` 匹配原始路径；冲突默认 skipped 并保留暂存；overwrite 时 bypass 永久删除目标再 Move 回；恢复成功条目 best-effort 清目录；dry-run 由 Move/DeleteFile 的 SkipDestructive 保证。
- **Acceptance Criteria Addressed**: AC-2, AC-3, AC-10
- **Test Requirements**:
  - `rule` TR-5.1~TR-5.5
- **Completion Evidence**:
  - `TestTrashDeleteAndList`（list 字段）、`TestTrashRestore`（全量恢复/冲突 skipped/--overwrite 覆盖）、`TestTrashRestorePatterns`（"keep/**" 只恢复匹配项）通过；CLI 冒烟冲突/覆盖恢复符合预期。

## Task 6: rc 端点
- **Status**: `completed`
- **Priority**: medium
- **Depends On**: Task 4, Task 5
- **Description**:
  - `operations/trashlist`（返回 items）、`operations/trashrestore`（fs/all/patterns/overwrite→restored/skipped）、`operations/trashcleanup`（removed/bytes）、`operations/trashempty`（removed/bytes），注册于 fs/operations/trash.go init。
- **Acceptance Criteria Addressed**: AC-8
- **Test Requirements**:
  - `rule` TR-6.1~TR-6.2
- **Completion Evidence**:
  - `TestRcTrash` 通过（list/restore patterns/cleanup/empty 全链路 + 缺 fs 参数报错）；实时 rcd 冒烟四个端点返回正确 JSON（delTime/origPath/size、restored/skipped、removed/bytes）。

## Task 7: `rclone trash` 命令组
- **Status**: `completed`
- **Priority**: medium
- **Depends On**: Task 4, Task 5
- **Description**:
  - `cmd/trash/trash.go`：父命令 `trash`（含机制、与 backup-dir 关系、bucket 根限制说明）；子命令 `list [--json]`、`restore [--all] [--overwrite] [pattern...]`、`cleanup`、`empty`；`cmd/all/all.go` 空导入注册。
- **Acceptance Criteria Addressed**: AC-2, AC-3, AC-4, AC-5
- **Test Requirements**:
  - `rule` TR-7.1~TR-7.3
- **Completion Evidence**:
  - `rclone trash --help` 及四个子命令 help 正常；CLI 端到端冒烟（delete→list→restore、cleanup、empty、dry-run）全部通过。

## Issue I-1: 未启用 --trash 时暂存目录排除规则仍然生效（R1 actionable/high）
- **Status**: `completed`
- **Completion Evidence**:
  - `trashFilterRule` 开头增加 `if !ci.Trash { return "" }`；TrashFilterCtx 在 Trash=false 时为 no-op。
  - `TestTrashFilterRule` 修正（Trash=false 时规则为空）；`TestTrashDisabled` 增加 `.rclone-trash/...` 对象在未启用时被永久删除的断言，全部通过。
  - CLI 复验：无 `--trash` 时 `rclone delete data` 删除 `.rclone-trash` 内文件（0 残留）；`rclone sync` 正常复制 `.rclone-trash/t.txt`。
- **Priority**: high
- **Depends On**: Task 8
- **Discovered By**: Review R1
- **Description**:
  - `trashFilterRule` 仅判断 `TrashDir != ""`（默认值即 `.rclone-trash`），`TrashFilterCtx` 在 operations.Delete 与 sync.runSyncCopyMove 被无条件调用，导致未启用 `--trash` 时 delete/sync 遍历仍排除 `.rclone-trash/**`：`rclone delete dir` 残留该目录内文件、`rclone sync` 不复制/不删除该目录。违反 AC-9/NFR-1。
  - 修复：`trashFilterRule` 开头增加 `if !ci.Trash { return "" }`（trashPurgeCtx 仅在 trashActive 路径调用、trashInternalCtx 不使用该规则，显式 trash 命令不受影响）；修正 `TestTrashFilterRule`（Trash=false 时规则为空）；`TestTrashDisabled` 增加"未启用时 delete 会删除 `.rclone-trash` 内文件"的回归断言。
- **Acceptance Criteria Addressed**: AC-9
- **Test Requirements**:
  - `rule` TR-I-1.1: Trash=false 时 TrashFilterCtx 前后过滤器行为一致；含 `.rclone-trash/x.txt` 的 fs 走 operations.Delete 后该文件被永久删除
  - `rule` TR-I-1.2: 未启用 trash 时 sync 正常复制/删除 `.rclone-trash` 内文件（sync 测试或 CLI 验证）

## Issue I-2: `trash empty` 在暂存目录不存在时报错退出（R1 actionable/medium）
- **Status**: `completed`
- **Completion Evidence**:
  - TrashEmpty 先做根 List 探测，目录不存在时直接返回 (0,0,nil)；删除逻辑改为清空暂存区全部对象（含不符合布局的外来文件），再 best-effort Rmdirs。
  - 新增 `TestTrashEmptyMissingAndForeign`（缺失 noop + 外来文件一并清空 + 幂等）通过；CLI 复验 fresh 目录 empty/cleanup/restore/list 全部 exit 0 无 ERROR。
- **Priority**: medium
- **Depends On**: Task 8
- **Discovered By**: Review R1
- **Description**:
  - TrashEmpty 无条件对 trash fs 执行 Rmdirs；暂存目录首次使用不存在时 walk 重试 3 次并 CountError，命令 exit 3。期望幂等：不存在视为空，退出 0 无 ERROR。
  - 修复：TrashEmpty 在列举前做根 List 探测（复用 trashListEntries 相同的 ErrorDirNotFound 判定），不存在时直接返回 0；补"首用 empty"测试。
  - 顺带处理 advisory：TrashEmpty 删除暂存区内所有对象（含不符合布局的外来文件），使 empty 语义完整。
- **Acceptance Criteria Addressed**: AC-4（empty 幂等）
- **Test Requirements**:
  - `rule` TR-I-2.1: 暂存目录不存在时 TrashEmpty 返回 (0,0,nil) 且无错误日志
  - `rule` TR-I-2.2: 暂存区含非布局对象时 TrashEmpty 一并清空

## Review 附带修复项（advisory，随本次一并处理）
- 命令父文档 "excluded from rclone listings" 改为准确描述（delete/sync 遍历排除）。
- `isTrashFs`/`trashExcluded` 路径比较先 `filepath.ToSlash` 归一化（Windows 反斜杠）。
- Purge 仅在 `dir == ""`（暂存目录位于被 purge 根内）时 leaveRoot。
- spec FR-2 随机串位数文案与实现对齐（16 位）。

## Task 8: 测试补全与全量验证
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 3, Task 6, Task 7
- **Description**:
  - `fs/operations/trash_internal_test.go`：布局编解码、trashExcluded、trashFilterRule 单测。
  - `fs/operations/trash_test.go`：13 个集成测试覆盖 delete/list/restore（冲突/覆盖/patterns）/retention/max-size/empty/purge/sync 删除/move 语义/暂存内硬删除/防循环/未启用回归/dry-run + rc 端到端。
  - 全量验证：`go test ./...`、`go vet`、`gofmt`。
- **Acceptance Criteria Addressed**: AC-1 ~ AC-11
- **Test Requirements**:
  - `rule` TR-8.1: 新增测试全部通过且断言实际行为
  - `rule` TR-8.2: go build + fs/vfs/cmd 既有测试无回归
  - `rubric` TR-8.3: 实现质量；scale 1-5；threshold >= 4
- **Completion Evidence**:
  - 18 个 trash 相关测试全部通过（`go test ./fs/operations/ -run 'TestTrash|TestRcTrash'`）。
  - `go test ./...`：唯一失败 `cmd/serve/s3 TestS3Minio`，原因为环境缺 `flock`（master 同样失败，与本次改动无关）；fs/operations、fs/sync、vfs、fs 全部子包、cmd 全部其余包均 ok。
  - `go vet` 通过；gofmt 无差异；未引入新依赖；核心改动集中在 trash.go（新增）+ operations.go/sync.go/config.go/vfs 各最小改动。
