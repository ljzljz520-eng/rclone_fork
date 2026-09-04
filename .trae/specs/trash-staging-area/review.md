# 删除文件暂存区（回收站） - 独立审查

- [ ] CP-R1: 启用 `--trash` 后，文件删除被移动到 `<fs根>/.rclone-trash/<日期>/<时间-随机>/<原路径>`，原路径文件消失；同日同名重复删除产生独立条目且旧条目不丢失；暂存区内容不会被二次移入
  - **Type**: `rule`
  - **Covers**: AC-1, AC-7
  - **Evidence**: Pending

- [ ] CP-R2: `trash list` 输出原始路径/删除时间/大小；`trash restore` 一键恢复全部、patterns 过滤恢复、冲突跳过报告、`--overwrite` 覆盖
  - **Type**: `rule`
  - **Covers**: AC-2, AC-3
  - **Evidence**: Pending

- [ ] CP-R3: `trash cleanup` 按 `--trash-retention` 驱逐过期条目、按 `--trash-max-size` 从旧到新驱逐至限额以下；`trash empty` 清空全部
  - **Type**: `rule`
  - **Covers**: AC-4, AC-5
  - **Evidence**: Pending

- [ ] CP-R4: 拦截面正确：sync 删除、purge、vfs 删除进暂存区；`rclone move`/move 回退源删除/backup-dir 流不进暂存区；暂存区内对象删除为永久删除
  - **Type**: `rule`
  - **Covers**: AC-6
  - **Evidence**: Pending

- [ ] CP-R5: 四个 rc 端点（operations/trashlist、trashrestore、trashcleanup、trashempty）参数与行为和命令等价，缺 fs 参数报错
  - **Type**: `rule`
  - **Covers**: AC-8
  - **Evidence**: Pending

- [ ] CP-R6: 未启用 `--trash` 时既有行为零变化；`--dry-run`/`-i` 下无实际移动/删除
  - **Type**: `rule`
  - **Covers**: AC-9, AC-10
  - **Evidence**: Pending

- [ ] CP-U1: 实现质量
  - **Type**: `rubric`
  - **Covers**: AC-11
  - **Scale**: 1-5
  - **Anchors**: 1 = 核心包出现后端特例/大面积重构/缺 godoc；3 = 功能正确但风格偏离或有明显重复；5 = 改动集中、复用 Move/DeleteFiles/cache/filter 等既有机制、注释符合 AGENTS.md、向后兼容
  - **Pass Threshold**: >= 4
  - **Evidence**: Pending

## Review History

### Review R1

- **Reviewer**: 独立审查（只读，未修改任何源代码）
- **Date**: 2026-09-04
- **Result**: **fail**
- **总体结论**：功能主体（移入/布局/list/restore/cleanup/empty/rc/purge/sync/move 旁路/dry-run）实现正确、测试真实断言、改动集中且风格符合 AGENTS.md；但发现 **1 个向后兼容阻断问题**（未启用 `--trash` 时过滤器规则仍然生效，导致 delete/sync 静默跳过 `.rclone-trash` 目录，违反 AC-9/NFR-1）和 **1 个首用边界 bug**（`trash empty` 在暂存目录不存在时报错退出）。修复 Finding 1/2 后可再复审。

#### Checks Performed

| 检查 | 命令 | 结果 |
|---|---|---|
| 全量构建 | `go build ./...`；`go build -o /tmp/rclone-review .` | PASS |
| 新增测试 | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ -count=1 -run 'TestTrash\|TestRcTrash' -v` | PASS（18 个测试全过） |
| 回归测试 | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ ./fs/sync/ ./vfs/ -count=1` | PASS（ok 18.0s / 10.8s / 18.3s） |
| 相关包回归 | `go test ./fs/ ./fs/filter/ ./fs/walk/` | PASS |
| vet / fmt | `go vet ./fs/operations/ ./fs/sync/ ./vfs/ ./cmd/trash/`；`gofmt -l`（全部改动文件） | 干净 |
| CLI 冒烟 | `/tmp/rclone-review` 对 local 后端：delete --trash、同名二次删除、防循环、trash list/--json、restore（全量/冲突/overwrite/patterns）、cleanup（retention/max-size）、empty、purge --trash、sync --trash、move --trash（源永久删除/覆盖进目的暂存）、--backup-dir 优先、--dry-run、绝对路径、首用空列举 | 见 Evidence |
| rc 冒烟 | `rclone rcd --rc-no-auth` 下调用 operations/trashlist、trashrestore、trashcleanup、trashempty 及缺 fs 参数 | PASS |
| 代码阅读 | spec.md、tasks.md、trash.go 全文、operations.go（move/DeleteFileWithBackupDir/Purge/Delete/Rmdirs/MoveBackupDir/RemoveExisting）、sync.go（pairHandler/pairCopyOrMove/runSyncCopyMove/MoveDir）、vfs/file.go、fs/config.go、cmd/trash/trash.go、两个测试文件 | 完成 |

#### Evidence（关键输出摘录）

1. **阻断问题复现**（未启用 `--trash`）：
   ```
   $ mkdir -p data/.rclone-trash/2020-01-01/entry && echo keep > data/.rclone-trash/2020-01-01/entry/old.txt && echo hello > data/a.txt
   $ rclone delete data          # 注意：没有 --trash
   $ find data -type f
   data/.rclone-trash/2020-01-01/entry/old.txt      # 应被删除却残留
   $ rclone sync src dst         # 没有 --trash
   # src/.rclone-trash/s.txt 未被复制；dst/.rclone-trash/d.txt（源端不存在）未被删除
   ```
2. 功能正确性（均符合预期）：
   - `delete --trash` 后布局为 `.rclone-trash/2026-09-04/043934-<16位随机>/dir/file.txt`，内容一致；同名文件两次删除产生两个独立条目（first/second 内容均保留）；再次 delete 条目不倍增（防循环 PASS）。
   - `trash restore`：全量恢复 PASS；冲突时 "Restored 0 ... skipped 1" 且原文件不动；`--overwrite` 后内容被暂存版本替换。
   - `trash cleanup`：2001 旧条目被驱逐、1 天前条目保留；`--trash-retention 0 --trash-max-size 20B` 时 33B 总量驱逐最旧两条（17B）、保留 16B 条目（注意：裸数字 `20` 按 SizeSuffix 惯例为 20Ki，不是 bug）。
   - `purge --trash`：文件进暂存、根目录保留；`sync --trash`：目的端多余文件进暂存；`move --trash`：源文件永久删除（源端无 .rclone-trash）、目的端被覆盖文件进入目的端暂存；`--backup-dir` 与 `--trash` 同时设置时文件进 backup-dir 且不产生 trash 目录。
   - rc 四端点返回 `items`/`restored,skipped`/`removed,bytes`；缺 `fs` 返回 400 `Didn't find key "fs"`。
   - `--dry-run`：delete/restore/cleanup/empty 均无实际移动/删除（对象状态前后一致）。
3. **Finding 2 复现**：
   ```
   $ rclone trash empty fresh            # fresh/.rclone-trash 不存在
   ERROR : Local file system at fresh/.rclone-trash: Failed to list "": directory not found
   ERROR : Attempt 1/3 ... 2/3 ... 3/3 failed with 1 errors and: directory not found
   NOTICE: Failed to empty: directory not found     # exit=3
   ```
   暂存目录一旦存在（即使已空），重复 empty 正常退出 0。

#### 检查点结论

| 检查点 | 结论 | 说明 |
|---|---|---|
| CP-R1（移入/布局/独立条目/防循环） | **pass** | 测试 TestTrashDeleteAndList、TestTrashAntiLoop、TestTrashDeleteInsideTrashIsPermanent + CLI 冒烟均验证；ctx 旁路 + 过滤器排除 + trashExcluded 前缀三重防循环成立 |
| CP-R2（list/restore/patterns/冲突/overwrite） | **pass** | TestTrashRestore/TestTrashRestorePatterns/TestRcTrash + CLI 冒烟验证；JSON 输出字段完整 |
| CP-R3（retention/max-size/empty） | **pass（附 Finding 2）** | 驱逐规则正确（retention=0、max-size=-1 边界均验证）；但 empty 在暂存目录不存在时失败（Finding 2） |
| CP-R4（拦截面：sync/purge/vfs 进暂存；move/backup-dir 不进；暂存内永久删除） | **pass** | sync 删除、purge fallback、move 源删除旁路（sync.go L469/L518 与 operations.go L522）、MoveBackupDir 旁路（L2020）、Move dst 覆盖删除 L471 在 trashMove/restore/backup 路径均继承 bypass ctx（已逐行确认）、vfs File.Remove 改道 operations.DeleteFile 且缓存清理保留；vfs 全套测试通过 |
| CP-R5（4 个 rc 端点等价、缺参报错） | **pass** | TestRcTrash + rcd 实时冒烟验证 |
| CP-R6（未启用零变化；dry-run/-i 安全） | **fail** | dry-run 部分 pass；但未启用 `--trash` 时 `.rclone-trash` 目录被过滤器静默排除（Finding 1），违反 AC-9/NFR-1 |
| CP-U1（实现质量 rubric） | **4/5（达标线 4，勉强达标）** | 改动集中（核心新增 1 文件 ~750 行，核心包最小侵入）、复用 Move/DeleteFiles/walk/cache/filter/rc 既有机制、无后端特例、godoc 完整且为当前时描述、测试断言真实；扣分项：Finding 1 的兼容回归、TestTrashFilterRule/TestTrashDisabled 未覆盖该回归、Finding 2 边界缺失、少量文档/计数小瑕疵 |

#### Findings

1. **[actionable][high] 未启用 `--trash` 时 trash 目录排除规则仍然生效（向后兼容回归，CP-R6/AC-9 阻断项）**
   - 位置：[trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L109-L116) `trashFilterRule` 未检查 `ci.Trash`；调用点 [operations.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1204-L1205) `Delete` 与 [sync.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/sync/sync.go#L1357-L1358) `runSyncCopyMove` 无条件调用 `TrashFilterCtx`。
   - 根因：`TrashDir` 默认值为 `.rclone-trash`，`trashFilterRule` 仅判断 `dir != ""`，导致 `ci.Trash=false` 时规则 `"- .rclone-trash/**"` 仍被加入过滤器副本；delete/sync 的遍历因此跳过该目录。
   - 复现：见 Evidence 1（无 `--trash` 的 `rclone delete` 残留 `.rclone-trash` 内文件；`rclone sync` 不复制源端、不删除目的端该目录内文件）。用户数据中恰有同名目录、或在 conf 中设置了 `trash_dir` 但未启用 `trash` 时同样中招，且表现为"删除/同步静默不彻底"的数据安全问题。
   - 期望：`!ci.Trash` 时规则为空（no-op），与 spec Assumptions"启用暂存后该名称目录才被排除"及 NFR-1 一致。
   - 修复建议：在 `trashFilterRule` 开头加 `if !ci.Trash { return "" }`（`trashPurgeCtx`/`trashInternalCtx` 仅在 trash 生效路径调用，不受影响）；同步修正 [trash_internal_test.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash_internal_test.go#L67-L74) `TestTrashFilterRule`（该用例当前在 `Trash=false` 下断言规则非空，固化了错误行为），并在 `TestTrashDisabled` 中增加"未启用时删除/同步 `.rclone-trash` 内文件行为与主线一致"的断言。

2. **[actionable][medium] `trash empty`（含 `--dry-run`）在暂存目录不存在时报错并以 exit 3 退出**
   - 位置：[trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L435-L438) `TrashEmpty` 无条件对 trash fs 执行 `Rmdirs`；底层 walk 对不存在目录重试 3 次并经 [operations.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1533-L1538) `CountError` 累计错误，导致命令失败。
   - 复现：见 Evidence 3（首用 remote 上 `rclone trash empty remote:path` → 3 条 ERROR + "Failed to empty: directory not found"，exit=3；`--dry-run` 同样刷 ERROR）。`list/restore/cleanup` 因 `trashListEntries` 有 `ErrorDirNotFound` 探测而正常。
   - 期望：empty 应幂等，暂存目录不存在时视为空、退出 0 且无 ERROR 日志。
   - 修复建议：`TrashEmpty` 复用 `trashListEntries` 的根目录存在性探测（或令其返回"目录不存在"标记），不存在时跳过 `Rmdirs` 直接返回 0；或对 `Rmdirs` 返回的 `fs.ErrorDirNotFound` 静默忽略且不经 CountError。补充"空 trash/不存在 trash"的 empty 测试。

3. **[advisory][low] dry-run 下 restore/empty/cleanup 的汇总计数仍按已执行上报**
   - 位置：[trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L504-L509)（Move 在 dry-run 返回 nil 后 `restored++`）、trash.go L318-L343（removed/bytes 在删除前按计划值统计）。CLI 会输出 "Restored 1 files"、"Trash emptied: removed 1 files" 而实际无变化（实际对象状态正确，无副作用）。建议 dry-run 时计数为 0 或在文案中标注 dry-run；[trash_test.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash_test.go#L335-L345) `TestTrashDryRun` 当前断言 `restored==1/removed==1`，可一并调整。

4. **[advisory][low] 命令文档与行为不符：`rclone ls/lsl/lsf` 仍列出暂存目录内容**
   - 位置：[cmd/trash/trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/cmd/trash/trash.go#L45) 称 "The trash directory is excluded from rclone listings automatically"；实际只有 delete/sync 遍历排除（冒烟：`rclone --trash ls <dir>` 仍显示 `.rclone-trash/...`）。建议改为"excluded from delete/sync walks"。

5. **[advisory][low] 暂存目录内不符合布局的对象对 list/empty 不可见**
   - 位置：[trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L234-L253) `trashListEntries` 忽略无法解析的对象。用户手动放入 `.rclone-trash` 的文件不会出现在 `trash list`，也不会被 `trash empty` 删除（Rmdirs 仅删空目录）。建议在文档中说明或让 empty 兜底清空整个 trash fs。

6. **[advisory][low] `isTrashFs`/`trashExcluded` 的路径分隔符假设**
   - 位置：[trash.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L78-L105) 使用 `strings.Trim(root, "/")` 与 `"/"+dir` 后缀匹配；Windows 本地后端 Root 使用反斜杠，"fs 根即暂存目录"的判定可能失效（macOS/Linux 不受影响）。建议跨平台场景用 `filepath.ToSlash` 归一化后再比较。

7. **[advisory][low] `Purge` 在 trash 启用且 `dir != ""`（如 rc operations/purge 带 dir 参数）时 leaveRoot=true 会保留被 purge 的子目录本身**（空目录残留）；CLI purge 的 fs 根即目标目录，行为正确。位置：[operations.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1181-L1191)。

8. **[advisory][info] vfs `File.Remove` 改道 `operations.DeleteFile` 在未启用 trash 时也有可观测变化**：mount 内删除现在计入删除统计/日志并尊重 `--dry-run`（此前 `f.o.Remove` 无条件直删）。此为 FR-3 明确要求且方向正确，但属于既有用户可感知的行为变化，建议在 changelog/发布说明中提及。位置：[vfs/file.go](file:///Users/kkcarrot/swe-project/rclone_fork/vfs/file.go#L676-L680)。

9. **[advisory][nit] 其他**：`trashLastCleanup` map 在 rcd/mount 长进程中按 fs 配置串增长、无回收（每条目极小）；spec FR-2 写"8 位随机串"而实现/计划为 16 位（实现与 tasks 一致，建议回改 spec 文案）；容量驱逐时单文件超限条目会在下次清理（而非当次移入后）被驱逐，符合"懒清理"设计，仅作记录。

#### 修复建议（阻断项）

- Finding 1：`trashFilterRule` 增加 `ci.Trash` 开关；更新 `TestTrashFilterRule` 与 `TestTrashDisabled`（增加未启用时对 `.rclone-trash` 内文件 delete/sync 的回归断言）。修复后重跑 CP-R6 冒烟（Evidence 1 两条命令应表现为主线行为）。
- Finding 2：`TrashEmpty` 对 trash 目录不存在的情况短路返回；补充首用 empty 测试。
- 其余 advisory 可同 PR 或后续处理，不阻断。

### Review R2

- **Reviewer**: 独立审查（只读，未修改任何源代码）
- **Date**: 2026-09-04
- **Result**: **fail**
- **总体结论**：R1 的两个 actionable 发现（Finding 1 兼容回归、Finding 2 empty 首用报错）均已**正确且完整地修复**，无同类残留；三项附带 advisory 修复（文档措辞、`filepath.ToSlash` 归一化、Purge `leaveRoot = dir == ""`）实现正确、未引入回归。全部测试与构建通过。但 R2 新发现 **1 个 high 级数据安全缺口**：`purge --trash` 的 fallback 遍历使用 `includeAll=true`，过滤器被完全旁路，导致**已存在的暂存区内容在 purge 时被永久删除**（违反 FR-3「暂存目录本身受过滤器排除，不被列举/删除」），另有 1 个 medium 级命令失败问题（嵌套暂存目录导致父级 purge 以 exit 1 失败）。修复后可再复审。

#### Checks Performed

| 检查 | 命令 | 结果 |
|---|---|---|
| 全量构建 | `go build ./...`；`go build -o /tmp/rclone-r2 .` | PASS |
| 新增测试 | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ -count=1 -run 'TestTrash\|TestRcTrash' -v` | PASS（18 个测试全过，含修正后的 TestTrashFilterRule/TestTrashDisabled 与新增 TestTrashEmptyMissingAndForeign） |
| 回归测试 | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ ./fs/sync/ ./vfs/ -count=1` | PASS（ok 17.5s / 10.3s / 17.7s） |
| 相关包回归 | `RCLONE_CONFIG="/notfound" go test ./fs/filter/ ./fs/walk/ ./cmd/trash/` | PASS（cmd/trash 无测试文件，构建通过） |
| vet / fmt | `go vet ./fs/operations/ ./fs/sync/ ./vfs/ ./cmd/trash/`；`gofmt -l`（全部改动文件） | 干净 |
| Finding 1 复验（CLI） | 无 `--trash` 时 `rclone delete dir`（含 `.rclone-trash/` 子目录）；无 `--trash` 时 `rclone sync src dst`（双方各有 `.rclone-trash`） | PASS（见 Evidence 1/2） |
| Finding 2 复验（CLI） | `rclone trash empty/cleanup/list/restore <fresh-dir>`（无暂存目录）；`trash empty --dry-run` 同场景；含外来文件的 empty | PASS（全部 exit 0 无 ERROR；见 Evidence 3/4/5） |
| 新增边界冒烟 | `delete --trash` 作用于 fs 根恰为 `.rclone-trash`；作用于 `.rclone-trash/<日期>` 子目录；`purge --trash` 作用于**已有暂存区**的树；`purge --trash` 作用于含嵌套暂存区的树 | 见 Evidence 6/7/8（发现 R2-F1/F2/F3） |
| 代码阅读 | trash.go 全文、operations.go（DeleteFileWithBackupDir/Purge/Delete/listToChan/Rmdirs/MoveBackupDir/move 回退）、sync.go（pairHandler/pairCopyOrMove/startDeleters/runSyncCopyMove）、walk.go（ListR/listR/walk 的 includeAll 语义）、filter.go（Add/IncludeDirectory/NewFilter）、vfs/file.go、fs/config.go、cmd/trash/trash.go、两个测试文件 | 完成 |

#### Evidence（关键输出摘录）

1. **Finding 1 修复复验 — 未启用 trash 时 delete**：
   ```
   $ mkdir -p data/.rclone-trash/2020-01-01/entry && echo keep > data/.rclone-trash/2020-01-01/entry/old.txt && echo hello > data/a.txt
   $ rclone delete data          # 没有 --trash
   $ find data -type f
   （无输出：.rclone-trash 内文件已一并删除，0 残留）        # PASS
   ```
2. **Finding 1 修复复验 — 未启用 trash 时 sync**：
   ```
   $ rclone sync src dst         # 没有 --trash；src 有 .rclone-trash/.../s.txt，dst 有 .rclone-trash/.../d.txt
   $ find dst -type f
   dst/.rclone-trash/2020-01-01/x/s.txt   # 源端暂存同名目录内容正常复制
   dst/n.txt                              # dst 独有的 d.txt 已被正常删除        # PASS
   ```
3. **Finding 2 修复复验 — 首用/缺失目录**：
   ```
   $ rclone trash empty fresh          # fresh/.rclone-trash 不存在
   NOTICE: Trash emptied: removed 0 files (0)        # exit=0，无 ERROR、无重试
   $ rclone trash cleanup/list/restore fresh         # 同样 exit 0（cleanup removed 0 / restored 0 skipped 0）
   $ rclone trash empty fresh --dry-run              # exit=0，无 ERROR（R1 时 dry-run 同样刷 ERROR）
   ```
4. **empty 清空非布局对象**：`delete --trash` 产生 2 个暂存文件后手动放入 `.rclone-trash/foreign.txt`，`trash empty` 输出 `removed 3 files (16)`，全部对象消失；暂存根目录本身保留（与 leaveRoot 语义一致）；重复 empty 幂等 exit 0。
5. **R1 advisory 修复复验**：父命令 Long 现为 "The trash directory is excluded from delete and sync walks automatically"（与实际行为一致）；`isTrashFs`/`trashExcluded` 均先 `filepath.ToSlash` 归一化（trash.go L81-82/L97/L104）；`purge --trash subdir`（rc 语义 dir!=""）时 leaveRoot=false，空子目录可被移除（L1185）。
6. **R2-F1 复现（数据丢失）**：
   ```
   $ echo precious > p/old.txt && rclone delete --trash p
   $ rclone trash list p
   2026-09-04 13:21:33           9  old.txt            # 暂存区有 old.txt
   $ echo newfile > p/new.txt && rclone purge --trash p
   $ find p -type f
   p/.rclone-trash/2026-09-04/052133-zihejuq3cuweyik2/new.txt   # old.txt 被永久删除！
   $ rclone trash list p
   2026-09-04 13:21:33           8  new.txt            # 旧暂存内容不可恢复
   ```
   dry-run 对比证实遍历差异：`purge --dry-run --trash` 输出 `sub/.rclone-trash/2020-01-01/e/s.txt: Skipped move into trash`（暂存对象被投递到删除通道），而 `delete --dry-run --trash` 不投递任何暂存对象；Rmdirs 阶段日志可见 `DEBUG : sub/.rclone-trash: Excluded`（过滤器只在 includeAll=false 的 Rmdirs 遍历生效）。
7. **R2-F2 复现（命令失败）**：
   ```
   $ rclone delete --trash p2/sub          # 产生 p2/sub/.rclone-trash（按 fs 根隔离设计）
   $ rclone purge --trash p2
   ERROR : sub: Failed to rmdir: remove p2/sub: directory not empty
   ERROR : Attempt 1/3 ... 2/3 ... 3/3 failed with 2 errors and: failed to remove directories: ...
   NOTICE: Failed to purge with 2 errors: ...     # exit=1
   ```
   （文件实际已成功移入 p2 根暂存区，但命令报失败。）
8. **R2-F3 复现（边界，无数据丢失）**：`rclone delete --trash p/.rclone-trash/2026-09-04`（fs 根位于暂存目录*之下*）把文件移入嵌套的 `.rclone-trash/2026-09-04/.rclone-trash/...` 而非永久删除；嵌套条目对 `trash list`/`trash cleanup` 不可见，但 `trash empty` 可清空。对照：`rclone delete --trash p/.rclone-trash`（fs 根恰为暂存目录）为永久删除，符合文档示例。

#### 检查点结论

| 检查点 | 结论 | 说明 |
|---|---|---|
| CP-R1（移入/布局/独立条目/防循环） | **pass** | R1 验证仍然成立；delete 路径的 ctx 旁路 + 过滤器排除 + trashExcluded 前缀三重防循环未受修复影响；TestTrashDeleteAndList/AntiLoop/DeleteInsideTrashIsPermanent 通过 |
| CP-R2（list/restore/patterns/冲突/overwrite） | **pass** | 相关代码未改动，TestTrashRestore/TestTrashRestorePatterns/TestRcTrash 通过 |
| CP-R3（retention/max-size/empty 幂等） | **pass** | Finding 2 已修复：缺失目录短路返回 (0,0,nil)（trash.go L435-L440）、empty 删除全部对象含非布局文件（L443-L474）、Rmdirs best-effort（L476-L478）；TestTrashEmptyMissingAndForeign 与 CLI 冒烟（Evidence 3/4）验证 |
| CP-R4（拦截面：sync/purge/vfs 进暂存；move/backup-dir 不进；暂存内永久删除） | **fail** | sync 删除、vfs、move 源旁路、backup-dir 旁路均正确；但 **purge fallback 不保护已存在的暂存区**（R2-F1：暂存对象被 includeAll 遍历投递给删除通道，根级暂存对象被 trashExcluded 判定为永久删除、嵌套暂存对象被误移入根暂存区），FR-3 明确要求「暂存目录本身受过滤器排除，不被列举/删除」；嵌套暂存区还使父级 purge 硬失败（R2-F2）。现有 TestTrashPurge 仅覆盖空树首 purge，未覆盖「purge 时暂存区已存在」场景 |
| CP-R5（4 个 rc 端点等价、缺参报错） | **pass** | TestRcTrash 通过；trashempty 端点同步受益于 Finding 2 修复 |
| CP-R6（未启用零变化；dry-run/-i 安全） | **pass** | Finding 1 已修复：`trashFilterRule` 在 `!ci.Trash` 时返回 ""（L113-L115），`TrashFilterCtx` 返回原 ctx（L129-L131，不复制过滤器、零行为变化）；全部调用点排查无残留——TrashFilterCtx 仅 Delete（operations.go L1205）与 runSyncCopyMove（sync.go L1358）调用，trashPurgeCtx 仅在 useTrash（trashActive 要求 ci.Trash）分支调用；TestTrashFilterRule/TestTrashDisabled 已修正并断言未启用时删除 `.rclone-trash` 内文件；CLI 冒烟 Evidence 1/2 验证。dry-run 对象状态安全（TestTrashDryRun + Evidence 4） |
| CP-U1（实现质量 rubric） | **3/5（未达达标线 4）** | 修复质量本身良好（两处修复最小、对症、测试真实断言；advisory 修复得当），整体仍保持改动集中、复用既有机制、godoc 完整、无后端特例；但 purge 拦截面的关键安全场景（已有暂存区不被 purge 破坏）存在逻辑缺口且无测试覆盖，`trashPurgeCtx` 精心构造的排除规则在 includeAll 遍历下完全失效却无任何断言拦截，反映集成验证未覆盖 purge↔trash 交互的核心路径 |

#### Findings

1. **[actionable][high] `purge --trash` 永久删除已存在的暂存区内容（数据丢失，违反 FR-3）**
   - 位置：[operations.go](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1176-L1192) Purge fallback 的 `listToChan(purgeCtx, f, dir)` → [operations.go L1279-L1297](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1279-L1297) 以 `includeAll=true` 调 `walk.ListR`；而 [walk.go L149-L163](file:///Users/kkcarrot/swe-project/rclone_fork/fs/walk/walk.go#L149-L163) 与 [walk.go L299-L328](file:///Users/kkcarrot/swe-project/rclone_fork/fs/walk/walk.go#L299-L328) 中 includeAll 时**完全跳过过滤器应用**（Walk 路径的 list.DirSorted 同样如此），导致 [trash.go trashPurgeCtx L142-L151](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L142-L151) 添加的 `- <trash-dir>/**` 规则对 DeleteFiles 遍历完全无效；随后 [operations.go L567-L583](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L567-L583) 中根级暂存对象被 `trashExcluded`（remote 前缀分支，trash.go L104-L105）判定为「暂存内删除」走 `dst.Remove` **永久删除**；嵌套暂存对象（如 `sub/.rclone-trash/...`）前缀匹配失败则被 trashMove 误移入根暂存区（origPath 为 `sub/.rclone-trash/...`）。
   - 复现：见 Evidence 6（先 `delete --trash` 建立暂存区，再 `purge --trash` 同一根，旧暂存文件被永久删除）；dry-run 下可见暂存对象被投递到删除通道。
   - 影响：暂存区是用户的防误删安全网；对已在用 trash 的 remote 执行一次 `purge --trash`（或 rc operations/purge）会静默清空既有暂存内容，恰是本特性要防止的数据丢失。delete/sync 路径因 includeAll=false 不受影响。
   - 期望：purge fallback 的列举不得投递暂存区对象（根级与任意深度嵌套均不可），trashPurgeCtx 的排除意图必须实际生效；建议在 Purge fallback 对 listToChan 通道做对象/目录级 trash 排除（复用 trashExcluded/isTrashFs 判定），或让 includeAll 遍历额外强制执行 trash 排除规则；并补充「purge 时暂存区已存在/嵌套暂存区」的回归测试。
2. **[actionable][medium] 树内含嵌套暂存目录时 `purge --trash` 以 exit 1 失败（rmdir directory not empty ×3 重试）**
   - 位置：[operations.go L1191](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1191) `Rmdirs(purgeCtx, f, dir, leaveRoot)`；Rmdirs 的遍历 includeAll=false、暂存目录被过滤器剪枝（日志 `DEBUG : sub/.rclone-trash: Excluded`），因此父目录被误判为空并尝试 Rmdir，后端因目录内仍有被隐藏的暂存内容返回 non-empty → CountError 重试 3 次 → exit 1。
   - 复现：见 Evidence 7（先对 `remote:path/sub` 执行 trash 操作产生 `sub/.rclone-trash`（按 fs 根隔离的设计行为），再 `purge --trash remote:path`）。删除动作本身已成功，失败仅来自目录清理。
   - 期望：trash 模式下 purge 不应因「目录含有受保护的暂存区」而硬失败；建议 Rmdirs 将被排除目录视为非空（不尝试 rmdir 其父链），或在 trash 模式下容忍该类 rmdir 失败（DeleteFiles 成功后残留的内容只可能是暂存区）。
3. **[advisory][low] 删除 fs 根位于暂存目录*之下*的路径时，文件被移入嵌套暂存区而非永久删除**
   - 位置：[trash.go isTrashFs L79-L87](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L79-L87) 仅匹配 `root == dir` 或后缀 `/<dir>`；`rclone delete --trash remote:.rclone-trash/2026-09-04` 这类 fs 根（`.rclone-trash` 的子目录）判定失败，trashExcluded 的 remote 前缀分支同样不匹配，文件被 trashMove 到 `<该子目录>/.rclone-trash/...`。
   - 复现：见 Evidence 8。无数据丢失（嵌套条目对 list/cleanup 不可见但 `trash empty` 可清空）；文档仅承诺「trash 目录本身」内删除为永久删除（cmd/trash/trash.go L62-63，示例为 `remote:.rclone-trash`），严格意义上不违文档，但行为令人意外。
   - 期望（可选）：isTrashFs 增加「root 包含 `/<dir>/` 路径段」判定（与暂存名保留假设及过滤器规则任意深度匹配的语义一致）。
4. **[advisory][nit]** `trashFilterRule`（trash.go L116）对 `ci.TrashDir` 未做 `filepath.ToSlash` 归一化（isTrashFs/trashExcluded 已做）；默认值无分隔符不受影响，仅 Windows 用户显式配置含反斜杠的多级 trash-dir 时规则可能不匹配，过滤器规则惯例使用正斜杠。R1 advisory 3（dry-run 计数按计划值上报）、8（vfs Remove 改道的可观测变化需发布说明）、9（trashLastCleanup map 无回收）维持原状，按 R1 结论不阻断。

#### R1 修复项复验结论

- **R1 Finding 1（high，兼容回归）**：已正确完整修复。`trashFilterRule` 开头 `if !ci.Trash { return "" }`；`TrashFilterCtx` 规则为空时返回原 ctx（不 AddConfig、零开销零行为变化）；全部调用点（Delete、runSyncCopyMove）在未启用时为 no-op；trashPurgeCtx/trashInternalCtx 仅在 trash 生效路径使用。测试 TestTrashFilterRule（Trash=false → ""）、TestTrashDisabled（`.rclone-trash/...` 对象未启用时被永久删除）断言到位；CLI 冒烟 delete/sync 双场景确认主线行为恢复。
- **R1 Finding 2（medium，empty 首用报错）**：已正确完整修复。根 List 探测 ErrorDirNotFound 短路（trash.go L435-L440），与 trashListEntries 同一判定模式；empty 改为清空暂存 fs 全部对象（含非布局外来文件，回应 R1 advisory 5）；TestTrashEmptyMissingAndForeign 覆盖缺失 noop/外来文件/幂等三种情形；CLI 冒烟 fresh 目录 empty/cleanup/list/restore 及 dry-run 均 exit 0 无 ERROR。
- **附带 advisory 修复**：文档措辞（trash.go cmd L45-46）、ToSlash 归一化、Purge `leaveRoot = dir == ""`（operations.go L1185）三处均实现正确，未引入新问题。

#### 修复建议（阻断项）

- R2-F1：让 Purge fallback 的列举/删除通道实际排除暂存区（根级与任意深度），确保 purge 永不列举、删除、移动暂存区内对象；补充 TestTrashPurge 场景：暂存区已存在时 purge，旧暂存内容必须完好（建议断言 purge 前后 TrashList 中原有条目不变）。
- R2-F2：trash 模式下 Rmdirs 对「因含受保护暂存目录而非空」的父目录不报错（视为应保留），或排除该类 rmdir 错误；补充嵌套暂存区下 purge 根目录的用例。
- R2-F3 及 nits 可同 PR 或后续处理，不阻断。

### Review R3

- **Reviewer**: 独立审查（只读，未修改任何源代码）
- **Date**: 2026-09-04
- **Result**: **pass**
- **总体结论**：R2 的两个 actionable 发现（R2-F1 purge 永久删除已有暂存区内容、R2-F2 嵌套暂存目录导致 purge exit 1）均已**正确完整修复**并有真实集成测试覆盖；R1 两个 actionable 修复复验仍然成立。R2 修复代码本身（listToChan 新增 includeAll 参数、trashPurgeCtx 列举 + trashInternalCtx 执行 Rmdirs 的双 ctx 设计、双排除规则、leaveRoot）经逐行审查与 CLI/rc 冒烟未发现新的数据安全或回归问题。仅余 R2-F3 advisory 的部分残留（本地绝对路径/bucket 型 fs 根位于暂存目录*之下*时嵌套暂存仍会发生，无数据丢失、不违文档）及若干 nit。全部检查点通过，无 actionable 发现。

#### Checks Performed

| 检查 | 命令 | 结果 |
|---|---|---|
| 全量构建 | `go build ./...`；`go build -o /tmp/rclone-r3 .` | PASS |
| 新增测试 | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ -count=1 -run 'TestTrash\|TestRcTrash' -v` | PASS（18 个测试全过，含 TestTrashPurgeKeepsExistingTrash、TestTrashPurgeWithNestedTrash） |
| 回归测试（指定） | `RCLONE_CONFIG="/notfound" go test ./fs/operations/ ./fs/sync/ ./vfs/ -count=1` | PASS（ok 18.4s / 9.9s / 17.7s） |
| 相关包回归 | `RCLONE_CONFIG="/notfound" go test ./fs/filter/ ./fs/walk/ ./fs/cache/ ./fs/rc/ ./cmd/trash/` | PASS（cmd/trash 无测试文件，构建通过） |
| vet / fmt | `go vet ./fs/operations/ ./fs/sync/ ./vfs/ ./cmd/trash/`；`gofmt -l`（全部改动文件） | 干净 |
| R2-F1 复验（CLI） | `delete --trash` 建立暂存区 → 放入新文件 → `purge --trash` → `trash list` | PASS（新旧条目均在，exit 0；见 Evidence 1） |
| R2-F2 复验（CLI） | 对子目录 `delete --trash` 产生嵌套暂存区 → 根 `purge --trash` | PASS（exit 0，嵌套暂存原样保留、新文件进根暂存；见 Evidence 2） |
| R1-F1 复验（CLI） | 无 `--trash` 时 `delete`/`sync` 含 `.rclone-trash` 的树 | PASS（.rclone-trash 内文件被正常删除/复制；见 Evidence 3） |
| 首用幂等（CLI） | fresh 目录 `trash empty/list/cleanup/restore` 及 `empty --dry-run` | PASS（全部 exit 0 无 ERROR；见 Evidence 4） |
| R2 修复专项（CLI/rc） | 子目录 `purge --trash`（CLI fs 根即目标）；rc `operations/purge` 带 remote + `_config={"trash":true}`（leaveRoot=false 路径）；`purge --trash --dry-run`；`purge --trash <fs根==暂存目录>`；trash 子命令带用户 `--exclude` | PASS（见 Evidence 5-8） |
| R2-F3 加固复验（CLI） | 本地绝对路径 fs 根位于暂存目录*之下*的 `delete --trash`/`purge --trash` | 见 Finding 1（advisory，部分残留） |
| 代码阅读 | trash.go 全文、operations.go（DeleteFileWithBackupDir/Purge/Delete/listToChan/Rmdirs/move/MoveBackupDir）、sync.go（pairHandler/pairCopyOrMove/runSyncCopyMove）、walk.go（ListR/Walk/includeAll）、filter.go（Add/NewFilter/IncludeDirectory/ReplaceConfig）、glob.go（globToRegexp/globToDirGlobs）、vfs/file.go、fs/config.go、cmd/trash/trash.go、cmd/purge/purge.go、fs/rc/context.go、两个测试文件 | 完成 |

#### Evidence（关键输出摘录）

1. **R2-F1 复验 — purge 不删除已有暂存区内容**：
   ```
   $ echo precious > p/old.txt && rclone delete --trash p
   $ rclone trash list p
   2026-09-04 13:54:07           9  old.txt
   $ echo newfile > p/new.txt && rclone purge --trash p        # exit=0
   $ find p -type f
   p/.rclone-trash/2026-09-04/055407-qepilis2rufehik7/new.txt
   p/.rclone-trash/2026-09-04/055407-tugabam4joqapaj1/old.txt  # 旧暂存内容完好
   $ rclone trash list p                                        # exit=0
   2026-09-04 13:54:07           8  new.txt
   2026-09-04 13:54:07           9  old.txt                    # 新旧条目全部可见
   ```
2. **R2-F2 复验 — 嵌套暂存目录下 purge exit 0**：
   ```
   $ rclone delete --trash p2/sub          # 产生 p2/sub/.rclone-trash/.../x.txt
   $ echo y-content > p2/y.txt && rclone purge --trash p2     # exit=0（R2 时为 exit 1）
   $ find p2 -type f
   p2/.rclone-trash/2026-09-04/055419-rayedup2vudiquq0/y.txt  # 新文件进根暂存
   p2/sub/.rclone-trash/2026-09-04/055419-bagoxex1vepuxey8/x.txt  # 嵌套暂存原样保留
   ```
3. **R1-F1 复验 — 未启用 trash 时主线行为**：`rclone delete d`（无 --trash）后 `.rclone-trash/2020-01-01/entry/old.txt` 与 `a.txt` 均被删除（0 残留）；`rclone sync src dst`（无 --trash）后 src 的 `.rclone-trash/.../s.txt` 正常复制、dst 独有的 `d.txt`/`n.txt` 正常删除。
4. **首用幂等**：fresh 目录上 `trash empty/list/cleanup/restore` 及 `empty --dry-run` 全部 exit 0，输出 `removed 0 files (0)` / `Restored 0 ... skipped 0`，无 ERROR/重试。
5. **leaveRoot 两条路径**：CLI `rclone purge --trash k/sub`（fs 根即目标、dir=""）→ sub 保留并含自身 `.rclone-trash`（a.txt、deep/b.txt 在其中），exit 0；rc `operations/purge fs=<root> remote=sub _config={"trash":true}`（dir!=""）→ **sub 目录被移除**，两文件进入根暂存且 trashlist 返回 origPath `sub/a.txt`、`sub/deep/b.txt`。
6. **dry-run 安全**：`purge --trash --dry-run` 仅输出 `Skipped move into trash`，原文件全部保留原位，无目录删除。
7. **暂存目录本身永久删除**：`purge --trash <root>/.rclone-trash`（fs 根恰为暂存目录）→ 暂存内容永久删除、暂存目录本身移除，exit 0。
8. **trash 子命令对用户过滤器免疫**：`trash list/empty --exclude '*.txt'` 仍能列举/清空全部暂存对象（内部 walk 为 includeAll=true）；对照 `purge --trash --exclude '*.log'` 与主线 purge 一样被 cmd/purge 的 `fs.Fatalf("filters are not supported with purge ...")` 守卫拦截（两者 exit 1 一致），过滤器不会在 trash 路径产生分歧行为。

#### R2 修复代码审查结论

- **listToChan 签名变更**：全仓仅 2 个调用点，均在 [Purge](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1187-L1193)；非 trash 路径维持 `includeAll=true`（与旧行为逐字节一致），trash 路径 `includeAll=false` 使 [trashPurgeCtx](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L159-L170) 的排除规则实际生效。最小改动、godoc 已更新。
- **双 ctx 设计正确**：列举/删除用 `purgeListCtx`（include-all 过滤器 + 双暂存排除规则；DeleteFiles 内 DeleteFileWithBackupDir 仍走 trashMove，dry-run 由 SkipDestructive 保证且 ci 不被 ReplaceConfig 影响——冒烟 6 验证）；Rmdirs 用 [trashInternalCtx](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L175-L181)（include-all + bypass），暂存对象被 walk 列举并将其父目录链标记为非空（[Rmdirs](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/operations.go#L1558-L1572) 仅对象可令目录非空），含暂存目录的父目录不再被 Rmdir——R2-F2 的 directory-not-empty 重试链从机制上消除（冒烟 2 exit 0 证实）。
- **双排除规则无误伤**：`- <dir>/**` 的正则为 `(^|/)\.rclone\-trash/.*$`（glob 不以 `/` 开头时锚定 `(^|/)`），本身即匹配任意深度的暂存目录；`- **/<dir>/**` 是其子集，属显式冗余、无额外匹配面。`my.rclone-trash/`、`.rclone-trash-backup/` 等近似名均不匹配（逐段正则核对）。
- **leaveRoot 逻辑**：`leaveRoot = useTrash && dir == ""`——CLI purge（fs 根即目标）保留含暂存区的根；rc purge 带 remote 子目录时子目录可被移除（冒烟 5 两路径均验证）；purge 暂存目录本身时 isTrashFs=true → useTrash=false → 原生 Purge/永久 fallback（冒烟 7）。
- **防循环未受影响**：purge 列举被过滤器排除的暂存对象不进通道；DeleteFileWithBackupDir 的 trashExcluded 前缀判定（根级）与 ctx 旁路（trashMove/restore/cleanup/empty 均经 trashBypassCtx/trashInternalCtx）三重防护保持完好，TestTrashAntiLoop/TestTrashDeleteInsideTrashIsPermanent 通过。

#### 检查点结论

| 检查点 | 结论 | 说明 |
|---|---|---|
| CP-R1（移入/布局/独立条目/防循环） | **pass** | TestTrashDeleteAndList/AntiLoop/DeleteInsideTrashIsPermanent + R1/R3 CLI 冒烟；purge 移入路径同样满足布局与防循环 |
| CP-R2（list/restore/patterns/冲突/overwrite） | **pass** | TestTrashRestore/TestTrashRestorePatterns/TestRcTrash 通过；R3 未改动该面，rc trashlist 冒烟字段完整 |
| CP-R3（retention/max-size/empty 幂等） | **pass** | TestTrashCleanupRetention/MaxSize/Empty/EmptyMissingAndForeign 通过；fresh 目录四命令 exit 0（Evidence 4） |
| CP-R4（拦截面：sync/purge/vfs 进暂存；move/backup-dir 不进；暂存内永久删除） | **pass** | R2-F1/F2 已修复并验证（Evidence 1/2）；sync 删除、vfs（全套测试通过）、move 源删除旁路（operations.go L522、sync.go L467/L516）、MoveBackupDir 旁路（L2027-L2030）均未变；purge 暂存目录本身为永久删除（Evidence 7） |
| CP-R5（4 个 rc 端点等价、缺参报错） | **pass** | TestRcTrash 通过；rc 四端点缺 fs 均 400 `Didn't find key "fs"`；rc operations/purge + `_config trash` 行为正确（Evidence 5） |
| CP-R6（未启用零变化；dry-run/-i 安全） | **pass** | TestTrashFilterRule/TestTrashDisabled + CLI delete/sync 双场景（Evidence 3）；purge/delete/restore/cleanup/empty dry-run 对象状态不变（Evidence 6 + TestTrashDryRun） |
| CP-U1（实现质量 rubric） | **4/5（达标线 4，达标）** | R2 修复最小对症（listToChan 仅 Purge 两调用点、双 ctx 职责清晰、复用 walk/filter/DeleteFiles/Rmdirs 既有机制）、两个新集成测试断言真实行为（含 purge 前后条目不变、嵌套暂存 purge 无错）、godoc 与"解释 why"注释符合 AGENTS.md、无后端特例；扣分：R2-F3 加固不完整（Finding 1）、trashPurgeCtx/trashInternalCtx 的 NewFilter(nil) 语义与"include-all"注释有理论偏差（Finding 2）、trashFilterRule 成为仅测试引用的近重复代码（Finding 3） |

#### Findings

1. **[advisory][low] R2-F3 加固对"fs 根位于暂存目录*之下*"仅覆盖相对根，本地绝对路径/bucket 型根仍会嵌套暂存**
   - 位置：[isTrashFs](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L79-L88) 判定为 `root == dir || HasSuffix(root, "/"+dir) || HasPrefix(root, dir+"/")`。本地后端 Root() 为绝对路径（ToSlash/Trim "/" 后形如 `abs/p/.rclone-trash/2026-09-04`），暂存名段位于串**中间**，三条均不匹配；bucket 型根 `bucket/.rclone-trash/<date>` 同理。`HasPrefix` 子句只覆盖 drive 式相对根（`.rclone-trash/...`）。
   - 复现（R3 CLI）：`rclone delete --trash /abs/n/.rclone-trash/2026-09-04` → 文件被移入嵌套的 `.../2026-09-04/.rclone-trash/2026-09-04/<entry>/d.txt` 而非永久删除；`purge --trash` 同路径同样嵌套。对照：fs 根恰为暂存目录（`/abs/n/.rclone-trash`）时 HasSuffix 命中、永久删除正确。
   - 影响：无数据丢失（嵌套内容 `trash empty` 可清空），文档仅承诺「暂存目录*本身*内删除为永久删除」（cmd/trash/trash.go L62-63），不违文档；行为令人意外。
   - 建议：补一条中段判定即可闭合，如 `strings.Contains(root, "/"+dir+"/")`（与过滤器规则任意深度匹配的语义一致），并增加对应单测/集成用例。
2. **[advisory][nit] trashPurgeCtx/trashInternalCtx 注释称 "include-all filter"，但 `filter.NewFilter(nil)` 会重新解析全局 filter.Opt（含 CLI/options 过滤规则）**
   - 位置：[trash.go L160/L177](file:///Users/kkcarrot/swe-project/rclone_fork/fs/operations/trash.go#L156-L181)。实际影响已被现有机制抵消：CLI purge 在 [cmd/purge/purge.go L42-L44](file:///Users/kkcarrot/swe-project/rclone_fork/cmd/purge/purge.go#L42-L44) 对激活过滤器 Fatalf 拦截（trash/非 trash 行为一致，R3 实测两者 exit 1 一致）；trash 内部列举（list/empty/cleanup/restore）walk 均为 includeAll=true，过滤器被旁路（R3 实测 `trash list/empty --exclude` 免疫）；rc `_filter` 只替换作业 ctx 过滤器、不写全局 Opt。
   - 建议（可选硬化）：用惰性 Options 构造（如 `filter.NewFilter(&filter.Options{MinSize: -1, MaxSize: -1})`）保证语义上确实 include-all，使代码不依赖上述守卫。
3. **[advisory][nit]** `trashFilterRule`（trash.go L112-L122）在生产代码中已无调用方（仅 trash_internal_test.go 引用），与 `trashExcludeRules` 近重复且未做 ToSlash 归一化（R2 nit 4 维持）。建议将单测合并到 trashExcludeRules 后删除该函数，或显式标注其测试用途。
4. **[advisory][info] 既有记录维持**：R1 advisory 3（dry-run 汇总计数按计划值上报）、R1 advisory 8（vfs File.Remove 改道 operations.DeleteFile 带来的可观测变化建议进发布说明）、R1 advisory 9（trashLastCleanup map 无回收）均仍成立、不阻断。

#### R2 修复项复验结论

- **R2-F1（high，purge 永久删除已有暂存区）**：已正确完整修复。Purge trash 路径列举改为 includeAll=false + trashPurgeCtx（双排除规则），DeleteFiles 与列举共用同一 ctx；TestTrashPurgeKeepsExistingTrash 断言 purge 后新旧条目并存且旧条目 DelTime 不变；CLI 冒烟 Evidence 1 复现 R2 场景并通过。
- **R2-F2（medium，嵌套暂存致 purge exit 1）**：已正确完整修复。Rmdirs 改用 trashInternalCtx（include-all），含暂存对象的父目录经 walk 实测标记为非空、不再触发 Rmdir；TestTrashPurgeWithNestedTrash 与 CLI 冒烟 Evidence 2（exit 0、嵌套暂存完好）验证。
- **R2-F3（advisory，根位于暂存目录之下）**：部分修复，见 Finding 1（无数据丢失、不阻断）。
- **R1 Finding 1/2 及附带 advisory 修复**：R3 复验仍然成立（Evidence 3/4；代码未回退）。
