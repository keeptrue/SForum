# 部署故障排查

[← 生产部署](../deployment.md)

先收集三样东西，再动手改：

```sh
./deploy.sh --action status
./deploy.sh --action logs
cat .deployrc
```

`.deployrc` 的 `status=recovery_required` 是判断“旧应用已停、新版本未健康”
的关键。没有它时，先看 `docker compose ps` 和 API 日志。

如果你用 `sforum-latest.sh`，先跑 `./sforum-latest.sh --check`：它会解析目标
Release、校验部署包、列出需要更新的受管工具文件，并显示计划动作（install /
upgrade / deploy 重建状态），全程不写入。

本页命令默认在安装目录执行，并复用这个数组：

```sh
COMPOSE=(docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml)
```

## 主机与 Docker

| 症状 | 判定 | 处置 |
| --- | --- | --- |
| `docker: command not found` | 没有 Docker | 安装 Docker Engine；不要用 Podman 代替 |
| `Cannot connect to the Docker daemon` | daemon 未运行 | 启动 Docker 服务后重试 |
| `需要 Docker Compose 2.24.4 或更高版本。` | Compose 太旧 | 升级 Compose 插件；`docker compose version --short` 确认 |
| `permission denied ... docker.sock` | 当前用户无 Docker 权限 | 用有权限的用户执行，或把用户加入 `docker` 组后重新登录 |
| `最终健康检查需要 curl。` | 宿主机缺 `curl` | 安装 `curl`；脚本用它检查健康端点 |

## 下载与校验

| 症状 | 判定 | 处置 |
| --- | --- | --- |
| `could not query the latest stable Release` | GitHub API 不可达，或还没有稳定 Release | 检查网络/DNS；需要预发布时用 `--channel prerelease` 或显式 `--version <TAG>` |
| `SHA256SUMS must contain exactly one ... entry` | 校验文件缺失、重复或格式不对 | 重新下载同一标签的 `SHA256SUMS`，不要手工编辑 |
| `checksum verification failed for ...` | 下载损坏或资产不匹配 | 删除文件后重新下载；不要跳过校验 |
| `provenance verification failed for ...` | `gh attestation verify` 失败 | 确认标签与仓库正确；无法安装 `gh` 时可显式 `SFORUM_SKIP_ATTESTATION=true`，但要先核对 checksum |
| `deployment bundle VERSION does not match ...` | 包内容与目标标签不一致 | 重新下载该标签的 `sforum-deploy.tar.gz` |
| `deployment bundle is missing ...` | 包不完整 | 重新下载；不要从旧目录拼文件 |
| `deployment bundle contains path traversal` | 包内路径异常 | 停止使用该包，重新从 Release 下载 |
| `another bootstrap operation is already running` | bootstrap 锁未释放 | 等另一个操作结束；确认无进程后删除 `.sforum-bootstrap.lock` |
| `another deployment or update operation is running` | 部署锁未释放 | 等另一个操作结束；`upgrade.sh` 会在持有进程退出或锁龄超过一小时后自动清理 |
| `sforum-install: could not query the latest stable Release` | loader 解析标签失败 | 检查网络/DNS；用 `--version <TAG>` 跳过解析，或 `--channel prerelease` 选预发布 |
| `sforum-install: sforum-bootstrap.sh checksum verification failed` | `SHA256SUMS` 缺该条目、条目重复，或下载内容不匹配 | 重新下载同一标签的 `SHA256SUMS`；不要手工编辑，也不要跳过校验 |
| `sforum-install: does not match SFORUM_INSTALL_SHA256` | 设置的 pin 与下载的 bootstrap 不一致 | 确认 pin 来自同一标签的 `SHA256SUMS`；不一致就停下排查，不要清空 pin 继续 |
| `sforum-install: install path exists and is not a directory` | `-d`/`SFORUM_INSTALL_DIR` 指向了文件 | 换一个目录，或先移走该文件 |
| `notice: no interactive terminal available` | 管道执行且没有可用的 `/dev/tty` | 预期行为：loader 用推荐默认值继续（等同 `--yes`）。需要交互时在终端里跑 `bash <(curl -fsSL .../sforum-install.sh)` |

## 安装与更新状态

| 症状 | 判定 | 处置 |
| --- | --- | --- |
| `an existing installation was detected; use the upgrade command` | 目录里已有 `.env.production` 或 `.deployrc` | 改用 `./sforum-bootstrap.sh upgrade`；不要为了重装删数据卷 |
| `.deployrc is missing; run install first` | 只完成了一半安装 | 先跑 `./deploy.sh --action deploy --version <TAG>` 补齐状态，再回到 bootstrap upgrade |
| `The currently deployed version is unknown; re-run ./deploy.sh to restore state.` | `.deployrc` 没有有效 `version=vX.Y.Z`，也没有可反查的蓝绿槽容器 | 跑 `./deploy.sh --action deploy --version <TAG>` 重建状态；`./sforum-latest.sh` 会自动识别并走这一步 |
| `core upgrade is incompatible ... target=... is older than migrated Core=...` | 数据库已迁移到更高的 Core 版本，目标版本比它旧 | 用 `./sforum-latest.sh --version <migrated Core 版本>` 或 `--channel prerelease`；需要真正回退时先恢复部署前备份，不要只回退镜像 |
| 同一条报错出现在**本机 dev 构建**（`target=1.0.0`） | `git describe --tags --match 'v[0-9]*' --long --dirty --always` 退化成裸 sha（常见原因：shallow clone 藏了 `v*` tag），dev 构建派生不出发行线，落回哨兵 `1.0.0`，于是「不知道我是谁」被当成了「我要降级」 | 先 `git rev-parse --is-shallow-repository`；输出 `true` 时执行 `git fetch --unshallow`，再确认上面的 `git describe` 输出带 `v` 前缀。当前版本起未声明的 target 不再参与降级判定，日志会给出 `Core version is unknown for this build` 告警 |
| `.env.production is missing; run install first` | 配置被移走或安装未完成 | 恢复配置或重新跑安装向导；不要凭空生成数据库密码 |
| `生产配置不完整或不安全。... <KEY>` | 环境变量契约不满足 | 按提示修对应键；规则见[环境变量与密钥](./environment.md) |
| `以下本机端口已被其他程序占用：` | `WEB_PORT`/`API_PORT` 被占 | 停掉占用进程，或在 `.env.production` 改端口；蓝绿拓扑还要检查 `edge` |
| `Could not pull ghcr.io/... after 3 attempts.` | Registry 不可达、包私有或限流 | 确认三个包公开可读；必要时 `docker login ghcr.io`；稍后重试 |
| `image version is '...', expected '...'` | 镜像标签与构建身份不一致 | 不要继续；确认 `SFORUM_VERSION` 与 Registry 中的标签一致 |
| `Standby slot failed internal health checks; live traffic was not changed.` | 备用槽没通过槽内检查 | 当前流量未受影响；看新槽日志，修复后重试 |
| `Ingress switch failed and was rolled back to the active slot.` | Caddy 切换失败并已回滚 | 检查 `deploy/runtime/Caddyfile` 与 `edge` 日志；当前流量仍在旧槽 |
| `Migration failed. The previous slot was restarted...` | 维护窗口迁移失败 | 旧槽已重启；修复迁移问题，或恢复 `.deployrc` 里的备份 |
| `Target slot failed its health checks after migration...` | 迁移成功但新槽不健康 | 旧槽已重启；检查新版本配置与依赖，再重试 |
| `The target release has migrations that are not safe online...` | 目标迁移必须停机 | 在安装目录执行 `./upgrade.sh --version <TAG> --confirm-maintenance`；bootstrap 不转发该参数 |
| 首次转换提示输入 `BOOTSTRAP` | 旧版直连拓扑要转蓝绿 | 交互输入 `BOOTSTRAP`，或加 `--bootstrap`；这一步有短暂维护窗口 |

## `recovery_required` 决策树

1. 读 `.deployrc` 的 `reason` 和 `backup`。
2. `migration_failed`：先看迁移日志；修复 SQL/权限问题后重试部署。无法修复时恢复 `backup`。
3. `startup_failed`：看 `docker compose ps` 与 API 日志；常见原因是环境变量、卷权限、镜像标签。
4. `health_failed`：分别检查 `http://127.0.0.1:${API_PORT}/api/v1/ready` 和
   `http://127.0.0.1:${WEB_PORT}/health`；再确认 PostgreSQL/Redis 健康。
5. 重试仍失败且数据库已被迁移时，恢复部署前备份，不要只回退镜像。

恢复命令：

```sh
SFORUM_CONFIRM_RESTORE=RESTORE ./deploy.sh --action restore
```

恢复脚本会在独立临时库中单事务导入，校验应用表后才原子切换库名；任一
SQL 错误都不会替换原库。

## 运行时问题

| 症状 | 判定 | 处置 |
| --- | --- | --- |
| 反代 502/504 | 反代目标错，或 `web`/`api` 没运行 | 检查 `WEB_PORT`/`API_PORT`、`docker compose ps`、容器日志 |
| 浏览器 API 403（CSRF） | `Origin` 与 `CSRF_TRUSTED_ORIGINS` 不匹配 | 把公开 origin 写进 `CSRF_TRUSTED_ORIGINS`；多站点逗号分隔 |
| 管理后台 404 | 访问路径与 `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` 不一致 | 用 `APP_URL + 前缀` 访问；改前缀后重启 `web` |
| WebSocket 连不上 | 反代没有转发 Upgrade，或 API 端口不对 | 按 `deploy/caddy/Caddyfile` 把 Upgrade 指到 `API_PORT` |
| 上传附件丢失 | 卷没有挂载或被重建 | 检查 `attachment_uploads` 卷；恢复备份前先确认卷 |
| 扩展导致启动失败 | 插件在启动阶段抛错 | 用下面的 Safe Mode 流程带外禁用 |
| 备份文件为空/不可读 | `pg_dump` 失败或磁盘满 | 看备份脚本输出；修复后重跑，不要发布半截备份 |

## Safe Mode 带外恢复

扩展导致 API 无法启动时，先用 Safe Mode 让宿主起来，再禁用问题扩展。

1. 在 `.env.production` 增加：

```sh
SFORUM_SAFE_MODE=true
```

2. 只重启 `api`：

```sh
"${COMPOSE[@]}" up -d --no-build --pull never api
```

3. 用发布页的 `sforum-cli_<VERSION>_linux_<arch>.tar.gz` 解压出 `sforum`
   二进制，放到安装目录。Compose 没有把 `5432` 发布到宿主机，所以让 CLI
   在 `postgres` 的网络命名空间里执行：

```sh
POSTGRES_USER="$(sed -n 's/^POSTGRES_USER=//p' .env.production)"
POSTGRES_DB="$(sed -n 's/^POSTGRES_DB=//p' .env.production)"
POSTGRES_PASSWORD="$(sed -n 's/^POSTGRES_PASSWORD=//p' .env.production)"
POSTGRES_USER="${POSTGRES_USER:-sforum}"
POSTGRES_DB="${POSTGRES_DB:-sforum}"

sforum_cli() {
  docker run --rm -i \
    --network "container:$("${COMPOSE[@]}" ps -q postgres)" \
    -v "$PWD/sforum:/usr/local/bin/sforum:ro" \
    -e "DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@127.0.0.1:5432/${POSTGRES_DB}?sslmode=disable" \
    alpine:3.22 /usr/local/bin/sforum "$@"
}
```

4. 按结果用 `sforum extension disable`、`sforum extension disable-all` 或
   `sforum extension quarantine` 禁用/隔离：

```sh
# 查看扩展状态
sforum_cli extension list

# 禁用单个第三方扩展
sforum_cli extension disable <extension-id>

# 禁用全部第三方扩展
sforum_cli extension disable-all

# 隔离精确匹配的内置/系统制品
sforum_cli extension quarantine <extension-id> \
  --expect-version <精确版本> \
  --expect-digest <64位hex摘要>
```

5. 把 `SFORUM_SAFE_MODE` 改回 `false`，再重启 `api`。

`quarantine` 只隔离精确匹配的制品，版本或摘要不一致会拒绝，避免误隔离。

## 运行时资源

管理台 `/control-panel` 的资源卡通过
`GET /api/v1/admin/overview/resources` 读取 API 与直属插件进程的 CPU、RSS
和可用 PSS。Linux 镜像直接读 `/proc`，不需要宿主机 PID namespace 或 Docker
socket。采样最多每 5 秒共享一次；RSS 与完整 PSS 展示最近 60 秒中位数；不
支持 PSS 的系统不会伪造“有效占用”。Linux 还报告 `AnonHugePages`，便于确认
插件是否仍有透明大页放大。插件明细按 RSS 从高到低，只归因当前 API/Worker
直接拥有的进程。API 行标记“含 Worker”；Worker 行只显示内嵌并发槽位和运行
任务数，不虚构独立 Worker 的 MiB。

## pprof 与 GOMEMLIMIT

这两个变量不在默认 Compose 的 `environment:` 列表里，直接写进
`.env.production` 不会生效。新增 `compose.diagnostics.yaml`：

```yaml
services:
  api:
    environment:
      PPROF_ENABLED: "true"
      PPROF_ADDR: "127.0.0.1:6060"
      GOMEMLIMIT: "512MiB"
```

应用 override：

```sh
DIAG=(docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml \
  -f compose.diagnostics.yaml)

"${DIAG[@]}" up -d --no-build --pull never api
```

pprof 只监听容器 loopback，不能通过 `ports:` 暴露。用共享网络命名空间
的一次性容器访问：

```sh
docker run --rm --network "container:$("${DIAG[@]}" ps -q api)" \
  alpine:3.22 wget -qO- http://127.0.0.1:6060/debug/pprof/
```

采集完成后删除 override 并重启 `api`。不要把 pprof 端口发布到公网或交给
反向代理。

## 收集证据

提交问题前至少保留：

```sh
./deploy.sh --action status > status.txt
./deploy.sh --action logs > logs.txt 2>&1
cat .deployrc > deployrc.txt
"${COMPOSE[@]}" ps > ps.txt
```

日志和 `.deployrc` 可能包含内部地址；分享前先脱敏，不要贴出
`.env.production`、数据库密码或密钥。
