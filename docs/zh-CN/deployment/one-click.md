# 一键安装与更新

[← 生产部署](../deployment.md)

这页写透三个 Release 脚本：`sforum-bootstrap.sh`、`deploy.sh`、`upgrade.sh`，
以及仓库侧可选的预取包装器 `sforum-latest.sh`。先看职责，再看执行链，最后看
状态文件与失败语义。

## 交付物与职责

| 文件 | 职责 |
| --- | --- |
| `sforum-bootstrap.sh` | 在线入口：解析不可变标签，校验并刷新自身和完整工具包，然后交给 `deploy.sh` 或 `upgrade.sh` |
| `sforum-deploy.tar.gz` | 同一 Release 的完整部署工具包 |
| `deploy.sh` | 安装/更新控制台，以及备份、恢复、状态、日志、重启、停止 |
| `upgrade.sh` | 已有安装的 blue/green 更新器 |
| `deploy/scripts/configure-production.sh` | `.env.production` 配置向导与密钥生成 |
| `deploy/scripts/backup-postgres.sh` | PostgreSQL 备份，先写临时文件再发布为 `0600` |
| `deploy/scripts/restore-postgres.sh` | 独立临时库 + 单事务恢复，校验后原子切换库名 |
| `deploy/scripts/wait-for-health.sh` | 健康检查等待 |
| `compose.yaml` + `compose.prod.yaml` + `compose.release.yaml` | 直连生产拓扑与发布镜像 |
| `compose.zero-downtime.yaml` | 蓝绿入口与双槽拓扑 |
| `deploy/caddy/Caddyfile` | 宿主机 Caddy 反代示例 |
| `deploy/router/Caddyfile.example` | 蓝绿入口 Caddy 示例 |
| `VERSION` | 部署包所属的精确版本 |

`sforum-latest.sh` 不属于 Release 交付物，也不由 bootstrap 刷新。它是仓库提供的
可选包装器：每次先解析最新 Release 并校验该 Release 的 bootstrap，再交给它执行
install/upgrade。

调用关系：

```text
sforum-bootstrap.sh install ──> deploy.sh --action deploy
sforum-bootstrap.sh upgrade ──> upgrade.sh
sforum-latest.sh            ──> sforum-bootstrap.sh upgrade
./deploy.sh                  ──> 交互菜单 / --action ...
```

## 获取方式

### 方式 A：单文件 bootstrap（推荐）

滚动安装：

```sh
(
  set -eu
  mkdir -p sforum
  cd sforum
  bootstrap_dir="$(mktemp -d .sforum-bootstrap.XXXXXX)"
  trap 'rm -rf "$bootstrap_dir"' EXIT HUP INT TERM
  curl -fsSLo "$bootstrap_dir/sforum-bootstrap.sh" \
    https://github.com/keeptrue/SForum/releases/latest/download/sforum-bootstrap.sh
  curl -fsSLo "$bootstrap_dir/SHA256SUMS" \
    https://github.com/keeptrue/SForum/releases/latest/download/SHA256SUMS
  (
    cd "$bootstrap_dir"
    awk '$2 == "sforum-bootstrap.sh" { print }' SHA256SUMS > sforum-bootstrap.sha256
    test "$(wc -l < sforum-bootstrap.sha256 | tr -d '[:space:]')" = 1
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum -c sforum-bootstrap.sha256
    else
      shasum -a 256 -c sforum-bootstrap.sha256
    fi
  )
  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh
  rm -rf "$bootstrap_dir"
  trap - EXIT HUP INT TERM
  ./sforum-bootstrap.sh install
)
```

固定版本时，把两个滚动地址换成同一标签：

```sh
https://github.com/keeptrue/SForum/releases/download/<TAG>/sforum-bootstrap.sh
https://github.com/keeptrue/SForum/releases/download/<TAG>/SHA256SUMS
```

### 方式 B：完整部署包

适合要离线审计、要保留整包、或不想让 bootstrap 先联网刷新自身的场景：

```sh
(
  set -eu
  mkdir -p sforum
  cd sforum
  curl -fsSLo sforum-deploy.tar.gz \
    https://github.com/keeptrue/SForum/releases/latest/download/sforum-deploy.tar.gz
  curl -fsSLo SHA256SUMS \
    https://github.com/keeptrue/SForum/releases/latest/download/SHA256SUMS
  awk '$2 == "sforum-deploy.tar.gz" { print }' SHA256SUMS > sforum-deploy.sha256
  test "$(wc -l < sforum-deploy.sha256 | tr -d '[:space:]')" = 1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c sforum-deploy.sha256
  else
    shasum -a 256 -c sforum-deploy.sha256
  fi
  if command -v gh >/dev/null 2>&1; then
    gh attestation verify sforum-deploy.tar.gz --repo keeptrue/SForum
  fi
  tar -xzf sforum-deploy.tar.gz --strip-components=1
  ./sforum-bootstrap.sh install
)
```

部署包根目录带 `README.md` 和 `VERSION`。解压后的目录就是安装目录；之后的
更新、备份、恢复都在这个目录里执行。

## 安装执行链

`sforum-bootstrap.sh install` 不是“下载后直接跑”，而是分阶段校验和移交。

1. 检查 `curl`、`tar`，以及当前目录是否已有 `.env.production` 或
   `.deployrc`。任一存在都会拒绝 install，并提示改用 upgrade。
2. 解析目标标签。默认 `stable` 只查 GitHub `/releases/latest`；显式
   `--channel prerelease` 或 `--version <TAG>` 才允许预发布。结果必须是
   `vX.Y.Z` 形式的不可变标签。
3. 下载目标标签的 `sforum-bootstrap.sh` 和 `SHA256SUMS`，只取该文件名的
   精确 checksum 条目，校验后做 `bash -n` 语法检查。
4. 用新 bootstrap 覆盖自身，然后重新执行；这一步保证后续所有逻辑都来自
   目标 Release，而不是本地旧脚本。
5. 下载并校验 `sforum-deploy.tar.gz`：检查压缩包只包含 `sforum-deploy/`
   路径、无路径穿越、`VERSION` 与目标标签一致、受管文件齐全、三个入口
   脚本语法有效。
6. 备份当前受管工具文件到 `.sforum/tooling-backups/`，再逐个原子替换。
   任一文件替换失败会恢复上一套工具并退出。
7. 移交 `./deploy.sh --action deploy --version <TAG>`。
8. `deploy.sh` 在缺少 `.env.production` 时启动配置向导；已有文件则只做
   `chmod 600`，不覆盖秘密。
9. 预检：Docker、daemon、Compose 版本、`curl`、环境变量契约、Compose
   展开、loopback 端口占用。
10. 拉取 `migrate`、`api`、`web` 三个镜像；对 `api` 和 `migrate` 分别用
    `docker run --rm <image> sforum-api --version` /
    `sforum-migrate --version` 核对构建身份。
11. 启动 `postgres` 和 `redis`。已有数据库先备份；全新库跳过备份。
12. 停止旧应用。蓝绿拓扑会同时停止 `edge` 与两个槽；直连拓扑只停
    `api`、`web`。随后清理遗留的独立 `worker` 容器。
13. 用目标镜像执行迁移，迁移和启动都带 `--pull never`，避免旧应用停止后
    再次依赖 Registry。
14. 以 `--no-build --pull never` 启动 `postgres redis api web`。
15. 检查 `http://127.0.0.1:${API_PORT}/api/v1/ready` 和
    `http://127.0.0.1:${WEB_PORT}/health`，确认四个受管服务都在运行且稳定。
16. 写入 `.deployrc`，打印反代目标、站点地址和管理后台地址。

任何一步失败都不会把实例标记为成功。迁移、启动或健康检查失败时，
`.deployrc` 会写入 `status=recovery_required`。

## `deploy.sh` 控制台

不带参数运行会进入交互菜单：

| 菜单 | 动作 |
| --- | --- |
| 1 | 安装或更新（推荐） |
| 2 | 创建 PostgreSQL 备份 |
| 3 | 恢复 PostgreSQL 备份 |
| 4 | 查看服务状态 |
| 5 | 查看日志 |
| 6 | 重启服务 |
| 7 | 停止服务 |
| 0 | 退出 |

脚本化时用 `--action`：

| 动作 | 行为 |
| --- | --- |
| `deploy` / `install` / `update` | 解析目标版本，备份已有库，迁移，启动，健康检查，记录 `.deployrc` |
| `backup` | 启动 `postgres` 后创建备份，输出备份文件路径 |
| `restore` | 提示输入 `RESTORE`，然后调用恢复脚本 |
| `status` | 用当前记录的版本执行 `docker compose ps` |
| `logs` | 跟踪全部服务日志 |
| `restart` | 重启服务并重新做健康检查 |
| `stop` | 停止受管服务 |
| `rollback` | 只打印回滚指引，不改变版本 |

版本解析有两条路径：

- **目标版本**：`deploy`/`install`/`update` 使用。显式 `--version` 优先；
  否则按 `--channel` 解析 `latest`。不会把 `.deployrc` 里的旧版本当作目标。
- **当前版本**：`status`/`logs`/`restart`/`stop`/`backup`/`restore` 使用。
  默认读 `.deployrc`，再回退到 `.env.production` 的 `SFORUM_VERSION`；
  维护动作不会为了显示状态去访问 GitHub。

`--lang zh|en` 设置界面语言；`--yes`/`--defaults` 接受推荐配置。部署锁是
`.deploy.lock`，同一目录不能并发执行两个部署动作。

## 更新执行链

### 入口：`sforum-bootstrap.sh upgrade`

升级要求 `.env.production` 和 `.deployrc` 同时存在。它先刷新自身和完整
工具包，再移交本地 `upgrade.sh`。刷新不会修改 `.env.production`、
`.deployrc`、`deploy/runtime/` 或数据卷。

### 预取包装器：`sforum-latest.sh`（可选）

`bootstrap upgrade` 的自刷新由本地旧脚本发起。预取包装器把联网解析和校验
提前到任何本地脚本执行之前：

1. 解析目标 Release。交互运行先询问渠道（正式版 / 最新发布版 / 指定标签）；
   stable 从 `releases/latest` 的 302 `Location` 读取标签，不消耗 GitHub API
   配额；预发布走 `/releases` API。
2. 下载该标签的 `sforum-deploy.tar.gz` 与 `SHA256SUMS`，校验 tar.gz 的精确
   条目、包内路径与 `VERSION`；本机 `gh` 支持 `attestation` 时再做来源校验。
3. 从部署包内的 bootstrap 提取权威受管清单，把安装目录里缺失或过期的
   `deploy.sh`、`upgrade.sh`、compose 文件、`deploy/scripts`、Caddy 示例与
   `VERSION` 原子替换；旧文件备份到 `.sforum/tooling-backups/`。
4. 用固定标签执行 `sforum-bootstrap.sh install|upgrade --version <TAG>`；如果
   `.env.production` 存在但 `.deployrc` 没有有效版本记录，改用
   `deploy.sh --action deploy --version <TAG>` 重建状态。

在安装目录执行：

```sh
# 重复运行会原子替换包装器，不需要先删除旧文件
tmp="$(mktemp ./.sforum-latest.XXXXXX)" && \
curl -fsSLo "$tmp" "https://raw.githubusercontent.com/keeptrue/SForum/main/sforum-latest.sh?$(date +%s)" && \
curl -fsSLo "$tmp.sha256" "https://raw.githubusercontent.com/keeptrue/SForum/main/sforum-latest.sh.sha256?$(date +%s)" && \
mv -f "$tmp.sha256" sforum-latest.sh.sha256 && \
mv -f "$tmp" sforum-latest.sh && \
{ sha256sum -c sforum-latest.sh.sha256 2>/dev/null || shasum -a 256 -c sforum-latest.sh.sha256; } && \
chmod 0755 sforum-latest.sh && \
./sforum-latest.sh --check
```

把最后一行换成 `./sforum-latest.sh` 即执行安装或更新。

`--check` 只执行前两步并对比清单，打印目标版本、部署包 SHA-256、哪些文件缺失或
过期与计划动作，适合在维护窗口前预检；目标低于 `.deployrc` 记录时会先给出警告。
脚本位于仓库根目录，不随 Release 分发；`--lang` 只在 install 生效，
`--bootstrap` 只在 upgrade 生效；Git 检出目录里实际同步需要 `--allow-git`。

### 更新器：`upgrade.sh`

1. 读 `.deployrc` 的当前版本。缺失或非法时，从运行中的 `api-blue` /
   `api-green` 镜像标签反查；仍无法确定则要求先跑 `./deploy.sh`。
2. 交互确认渠道和目标版本；`--yes` 跳过确认。目标同样解析为不可变标签。
3. 获取 `.deploy.lock`。锁内记录 PID；持有进程已退出或锁龄超过一小时会
   自动清理。
4. 如果 `.deployrc` 没有 `active_slot`，执行一次性入口转换：拉取目标镜像
   和 Caddy，启动 PostgreSQL/Redis，检查迁移兼容性，备份，迁移，启动
   `api-blue`/`web-blue`，槽内健康检查，写入 `deploy/runtime/Caddyfile`，
   停止旧 `api`/`web`，启动 `edge`，再做入口健康检查。交互模式要求输入
   `BOOTSTRAP`；无人值守需显式 `--bootstrap`。
5. 如果已有 `active_slot`，执行在线更新：拉取并核对三个目标镜像；检查
   目标迁移是否全部可在线执行；备份数据库；执行已声明兼容的 Core 迁移；
   启动备用槽；槽内检查 `api/v1/ready`、`/health` 和经 Web 转发的
   `api/v1/ready`；原子切换 Caddy 到新槽；停止旧槽；记录新状态。
6. 迁移不能在线执行时进入维护窗口：交互模式询问是否继续；`--yes` 模式
   不会自动停机，会拒绝并给出
   `./upgrade.sh --version <TAG> --confirm-maintenance`。该命令要在安装
   目录直接执行；bootstrap 不转发这个参数。
7. 失败处理：维护窗口迁移失败会重启旧槽；新槽健康检查失败会停掉新槽并
   重启旧槽；入口切换失败会把 Caddy 配置回滚到旧槽。在线更新的备用槽
   失败不会影响当前流量。

WebSocket 长连接在切换时可能需要自动重连。备用槽和旧槽的 API 都内嵌
Worker，切换期间可能短暂同时消费队列；任务领取和重试由 River 的数据库锁
保证，切换完成后旧槽停止，不会留下独立 Worker。

## 历史安装兼容

`v3.0.0-alpha.12` 及更早版本附带的 `upgrade.sh` 无法正确启动数据库兼容预检，
且旧的 `latest` 语义包含预发布。历史安装目录先固定下载 `v3.0.0-alpha.13`
的修复脚本，校验 SHA-256 后再更新；之后的更新改用 bootstrap。

```sh
(
  set -eu
  curl -fsSLo upgrade.sh \
    https://raw.githubusercontent.com/keeptrue/SForum/v3.0.0-alpha.13/upgrade.sh
  printf '%s  %s\n' ae186e13ca9551014e21ce7f77a7335413791268d97fb0b72f6be9820dedfe13 upgrade.sh > upgrade.sh.sha256
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c upgrade.sh.sha256
  else
    shasum -a 256 -c upgrade.sh.sha256
  fi
  chmod 0755 upgrade.sh
  ./upgrade.sh v3.0.0-alpha.13
)
```

该不可变标签是历史事实，不会改成 `main` 或 `latest`。

## 版本与通道

- `stable`：只解析最新稳定 Release。
- `prerelease`：允许解析预发布，但必须显式 `--channel prerelease` 或传
  具体标签。
- 所有选择最终都变成 `vX.Y.Z` 标签；生产 Compose 不运行浮动的 `latest`
  镜像。
- `.deployrc` 保存的是解析后的具体版本，不保存 `latest`。
- 需要可重复部署时显式固定版本：

```sh
./sforum-bootstrap.sh install --version <TAG>
./sforum-bootstrap.sh upgrade --version <TAG>
```

## 状态文件与锁

| 路径 | 作用 |
| --- | --- |
| `.env.production` | 生产配置，`0600`；向导不会覆盖已有文件 |
| `.deployrc` | 成功态记录 `lang`、`version`、`mode` |
| `.deployrc`（蓝绿） | 追加 `topology=blue-green`、`active_slot`、`blue_version`、`green_version` |
| `.deployrc`（失败） | `status=recovery_required`、`attempted_version`、`previous_version`、`reason`、`backup` |
| `.deploy.lock` | `deploy.sh`/`upgrade.sh` 的部署锁 |
| `.sforum-bootstrap.lock` | bootstrap 刷新工具包时的锁 |
| `.sforum-latest.lock` | `sforum-latest.sh` 预取并替换本地 bootstrap 时的锁 |
| `.sforum/tooling-backups/` | 被替换的受管工具文件备份 |
| `backups/` | PostgreSQL 备份，默认 `0600`；可用 `SFORUM_BACKUP_DIR` 改路径 |
| `deploy/runtime/` | 蓝绿 Caddy 配置；不要手工编辑 |

## 失败语义

`.deployrc` 的 `status=recovery_required` 表示旧应用已经停止、但新版本没有
被声明为健康。`reason` 有三种：

| `reason` | 含义 | 先做什么 |
| --- | --- | --- |
| `migration_failed` | 迁移失败 | 看 `./deploy.sh --action logs` 与迁移输出；修复后重试，或恢复 `backup` |
| `startup_failed` | 迁移成功但容器没有起来 | 看 `docker compose ps` 和 API 日志；修复配置/镜像问题后重试 |
| `health_failed` | 容器起来了但健康检查未通过 | 看 `/api/v1/ready`、`/health` 和依赖服务；修复后重试 |

`backup` 字段是本次部署前自动创建的备份路径。需要恢复时：

```sh
SFORUM_CONFIRM_RESTORE=RESTORE ./deploy.sh --action restore
```

常见边界：

- **install 被拒绝**：当前目录已有 `.env.production` 或 `.deployrc`。这是
  防止覆盖已有安装；改用 `upgrade`。
- **upgrade 被拒绝**：缺少 `.env.production` 或 `.deployrc`。半失败安装可
  先跑一次 `./deploy.sh --action deploy --version <TAG>` 补齐状态，再回到
  bootstrap upgrade。
- **维护窗口被拒绝**：无人值守不会自动停机。按提示在本地执行
  `./upgrade.sh --version <TAG> --confirm-maintenance`。
- **版本回退**：确认数据库迁移向后兼容后，用较早的不可变标签重新部署。
  迁移不兼容时，恢复部署前备份，不要只回退镜像。

## 验证

```sh
./deploy.sh --action status
./deploy.sh --action logs
docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml ps
curl -fsS http://127.0.0.1:${API_PORT:-18080}/api/v1/ready
curl -fsS http://127.0.0.1:${WEB_PORT:-3000}/health
```

更细的故障处置见[故障排查](./troubleshooting.md)。
