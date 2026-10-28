# 2026-10-28 升级探针噪音：8 条 `wget Connection refused` 的定性与收敛

**起因**：生产机 `boyunlab:/www/docker/SForumV3/sforum` 从 v3.0.21 在线更新到
v3.0.23 时，备用槽启动后连打 8 条
`wget: can't connect to remote host (127.0.0.1): Connection refused`，随后仍打印
「正在原子切换 Web/API 流量…」并停止 `web-green`，看起来像「健康检查没过也硬切」。

## 定性（不是故障）

- 8 条来自 `wait_inside_slot` 的 2 秒重试：`docker compose exec` 在槽容器内部跑
  BusyBox wget，`-q` 压不住 `bb_error_msg` 的连接失败诊断；失败分支是 `elif`
  短路，所以每轮最多一条 → 8 条 ≈ 8 轮 ≈ 16 秒冷启动窗口。
- `up -d` 只保证容器 started，不保证进程 listen。`cmd/api/main.go` 的
  `bootstrap.NewAPI`（连库 → `SyncBuiltins` → 插件运行时 genesis）跑完才
  `Listen()`，这段窗口 8080 必然 refused。v3.0.23 抬了 8 个内置插件版本，
  sourceDigest 全变，首次启动的内置同步更重。
- 切换发生在探针**通过之后**：`online_update` 只有 `wait_inside_slot` 返回 0 才
  进 `switch_router`，而 `switch_router` 在 `caddy reload` 后还要
  `wait_stable_ingress` 校验 `:3000`/`:18080` 的 `X-SForum-Active-Slot`，失败会
  回滚并 `die`。日志里没有回滚 → 切换已复核成功。

## Changed

- `upgrade.sh`：抽出 `probe_slot_endpoint`（stderr 收进 `PROBE_OUTPUT`，不直接
  上屏）、新增 `dump_slot_diagnostics`、重写 `wait_inside_slot`：
  首次即通过零输出；重试后通过给一行「已就绪（等待 Ns，共 M 次探测）」；等待中
  每 15 秒一行心跳（stderr，带失败探针名与槽容器名）；超时打印最后一次探针原始
  输出 + 该槽 `ps -a` 与 `logs --tail=60`。`SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS`
  写错单位时归一为 120，不再炸在算术展开上。
- `deploy/scripts/upgrade-health-probe_test.sh`（新）：5 个用例——快路径零输出、
  瞬时失败不泄漏 `wget:`、超时带诊断与日志 dump、容器未运行时透出 compose 报错、
  慢启动有 15 秒心跳。`fake_compose` 复用 `zero-downtime-state_test.sh` 的
  `COMPOSE=(fn)` 范式。
- `scripts/ci/release-tooling-gate.sh`：注册上述测试。
- `docs/{zh-CN,en-US}/deployment/one-click.md`：第 5 步补一句重试/超时语义。

## Decisions

- 不在循环里逐条打印失败：冷启动失败是预期态，逐条打印会把「还在启动」写成一片
  红色错误。诊断只在超时时一次性给出。
- 不做容器状态提前失败：`restart: unless-stopped` 下 `exited` 是瞬时态，轮询状态
  会误杀本来能成功的升级；容器没在跑时 compose 的报错本身会被 `PROBE_OUTPUT`
  捕获，超时路径照样能看见。
- 不改 120 秒默认值、不新增环境变量（该变量已在 `docs/*/deployment/environment.md`）。
- 测试里失败轮数必须落文件：`probe_slot_endpoint` 在命令替换的子 shell 中调用
  `fake_compose`，进程内变量递减不会回到父 shell（第一版就是这么挂到 120 秒超时的）。

## 验证

- `bash -n upgrade.sh`；`./deploy/scripts/upgrade-health-probe_test.sh`（28s，全绿）。
- `zero-downtime-state_test` / `deploy_test` / `upgrade-version-selection_test` /
  `zero-downtime-compose_test` / `compose-production_test` / `bootstrap_test` /
  `install-loader_test` / `postgres-safety_test` / `tests/validate-docs_test.sh` /
  `node tests/validate-docs.mjs` 全绿。
- 输出实样：瞬时失败 → `备用槽 blue 已就绪（等待 4s，共 3 次探测）。`；
  超时 → `健康检查超时（1 秒），最后未通过的检查：api ready（api-blue）` +
  `最后一次探针输出：wget: ... Connection refused` + 容器状态与日志。

## Next

- 修复随**下一个 Release** 的 `sforum-deploy.tar.gz` 到达服务器（`upgrade.sh` 是
  发布资产）；本次生产机不需要改动，只需按 runbook 复核：
  `cat .deployrc`（期望 `active_slot=blue` / `version=v3.0.23`）、
  `curl -sI http://127.0.0.1:3000/health | grep -i x-sforum-active-slot`（`blue`）、
  `docker inspect sforum-api-blue-1`（标签 `3.0.23`）。
- 若 `.deployrc` 未更新而槽头已是 blue：重跑 `./upgrade.sh v3.0.23 --yes` 自愈
  （会以旧 active 再确认新槽并写回状态）。本次无 schema 迁移，回退走
  `./upgrade.sh v3.0.21`，不需要恢复 `backups/sforum-20261028T152247Z.sql`。
