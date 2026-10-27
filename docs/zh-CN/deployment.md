# 生产部署

[← 中文文档首页](./README.md)

SForum 生产环境用 Docker Compose 运行 `web`、内嵌 River Worker 的 `api`、
PostgreSQL 和 Redis。只支持 Linux `amd64`/`arm64`，不支持 Windows。对外只
发布 loopback 端口，TLS 由宿主机反向代理终止。

## 先选路线

| 你的情况 | 用这个 | 深入阅读 |
| --- | --- | --- |
| 全新服务器，想最快上线 | `./sforum-bootstrap.sh install` | [一键安装与更新](./deployment/one-click.md) |
| 已有安装，更新到新版本 | `./sforum-bootstrap.sh upgrade` | [一键安装与更新](./deployment/one-click.md) |
| 已有安装，每次都要重新校验官方 bootstrap | loader 的 `upgrade` 子命令 | [一键安装与更新](./deployment/one-click.md) |
| 已有安装，连整包工具链一起重新同步 | `./sforum-latest.sh` | [一键安装与更新](./deployment/one-click.md) |
| 目标版本包含必须停机的迁移 | `./upgrade.sh --version <TAG> --confirm-maintenance` | [一键安装与更新](./deployment/one-click.md) |
| 不想用脚本，自己写 Compose | 手动下载、迁移、启动 | [手动 Compose 部署](./deployment/manual.md) |
| 只做备份、恢复、日志、重启 | `./deploy.sh --action ...` | [故障排查](./deployment/troubleshooting.md) |
| 配变量、密钥、反代信任 | `.env.production` | [环境变量与密钥](./deployment/environment.md) |

## 前置条件

- Docker Engine，以及 Docker Compose `2.24.4` 或更高版本。
- `curl`、`tar` 和标准 `install` 命令。
- 能访问 GitHub 和 `ghcr.io`；离线或镜像环境见
  [环境变量与密钥](./deployment/environment.md)。
- loopback 的 `3000`、`18080` 端口可用，或在安装向导里改成其他端口。
- 一个已经指向服务器的域名，以及宿主机上的 Caddy/Nginx。

不需要安装 Git，也不需要克隆仓库。

## 快速安装（最新稳定版）

下面的命令下载并校验**最新稳定版**的 `sforum-bootstrap.sh`；bootstrap 随后
会刷新同一目标版本的完整部署工具包：

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash
```

它会先询问安装目录（默认 `./sforum`，提示里显示绝对路径），回车即用默认值。
`SFORUM_INSTALL_DIR` 或 `-d DIR` 直接指定目录并跳过询问，
`SFORUM_NONINTERACTIVE=1` 全链路不询问，`--version <TAG>` 固定版本，
`--yes` 跳过确认。这个地址始终指向最新稳定 Release，不需要维护版本号。
loader 先解析不可变标签、校验该标签的 `sforum-bootstrap.sh`，再把控制权交给
它；bootstrap 随后刷新同一目标版本的完整部署工具包。安装向导会逐项解释
配置，并自动生成 PostgreSQL、Redis、会话、验证码、身份 HMAC、敏感选项
加密和 Marketplace 验签密钥。全部直接回车即可得到一套能启动的本机配置；
生成的 `.env.production` 权限为 `0600`，秘密不会打印到终端。

### 校验规则（必需）

- **只有 loader 允许从管道执行**：它不含安装逻辑，只做解析、校验和移交。
  `sforum-bootstrap.sh` 本身始终从磁盘执行，从不接收管道内容。
- loader 只校验 `SHA256SUMS` 中 `sforum-bootstrap.sh` 的精确条目，并要求结果
  恰好只有一条；bootstrap 内部会用同样规则再校验一次自身和完整部署包。
- 任何下载失败、条目缺失/重复、checksum 或来源证明失败都会在任何远程脚本
  执行前终止。首层 loader 的字节本身没有校验；需要连它一起固定时，用
  `SFORUM_INSTALL_SHA256` 指定 `sforum-bootstrap.sh` 的期望哈希。
- `gh attestation verify` 是可选的来源核对步骤（需要 GitHub CLI 且已认证）。
  loader 与 bootstrap 在检测到 `gh` 时都会核对；可用
  `SFORUM_SKIP_ATTESTATION=true` 显式跳过，但只建议在无法安装 `gh` 的环境
  这样做。
- 完整可审计的等价命令（逐条下载、校验、执行）见
  [一键安装与更新](./deployment/one-click.md)。

### 通道与固定版本

默认通道是 `stable`：只解析最新**稳定** Release。交互运行时会先询问渠道；
回车即稳定版。预发布版永远不会被隐式选中，必须显式选择：

```sh
./sforum-bootstrap.sh install --channel prerelease
./sforum-bootstrap.sh install --version <TAG>
./sforum-bootstrap.sh install --version <TAG> --lang zh --yes
```

无论哪种选择，脚本都会在拉取镜像前解析为具体的 `vX.Y.Z` 标签。生产 Compose
不会直接运行浮动的 `latest` 镜像。

## 更新已有安装

已有安装推荐用 bootstrap 更新。它每次都会先把自身刷新到目标 Release，再
校验并刷新该 Release 的完整部署工具包，最后交给 `upgrade.sh`：

```sh
./sforum-bootstrap.sh upgrade
./sforum-bootstrap.sh upgrade --version <TAG>
./sforum-bootstrap.sh upgrade --channel prerelease
./sforum-bootstrap.sh upgrade --yes
```

### 内置插件自动更新

升级后首次启动 API 时，Host 会自动把受保护的内置插件提升到该发行版自带的
版本。升级脚本本身不需要额外参数；行为与关闭方式见
[扩展与主题（运营侧）](./usage/extensions.md#内置插件自动更新)。需要先审查的
部署可先用 `SFORUM_SAFE_MODE=true` 启动，在后台关闭「自动更新内置插件」后再
正常启动。

### 每次重新获取官方工具链（可选）

`./sforum-bootstrap.sh upgrade` 的自刷新由本地旧脚本发起。若要求本次使用的
bootstrap 一定来自 GitHub，用 loader 的 `upgrade` 子命令：

```sh
curl -fsSL https://github.com/keeptrue/SForum/releases/latest/download/sforum-install.sh | bash -s -- upgrade
```

需要连整包工具链（`deploy.sh`、compose 文件、`deploy/scripts`、Caddy 示例、
`VERSION`）一起重新同步时，用仓库侧的 `sforum-latest.sh` 包装器：每次运行都会
解析最新 Release、下载并校验其完整部署包 `sforum-deploy.tar.gz`，把受管文件
全部同步到该版本，旧文件备份到 `.sforum/tooling-backups/`，然后才交给
install/upgrade。`--check` 只列出缺失或过期的文件，不写入任何内容；在 Git
检出目录里实际同步需要显式加 `--allow-git`，避免误覆盖源码。完整用法与失败
语义见[一键安装与更新](./deployment/one-click.md)。

第一次接入 bootstrap 时，在现有安装目录从**最新稳定 Release 资产**下载并
校验（不要使用 `main` 分支的浮动内容）：

```sh
(
  set -eu
  cd /path/to/sforum
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
    if command -v gh >/dev/null 2>&1; then
      gh attestation verify sforum-bootstrap.sh --repo keeptrue/SForum
    fi
  )
  install -m 0755 "$bootstrap_dir/sforum-bootstrap.sh" ./sforum-bootstrap.sh
  rm -rf "$bootstrap_dir"
  trap - EXIT HUP INT TERM
  ./sforum-bootstrap.sh upgrade
)
```

刷新工具包不会修改 `.env.production`、`.deployrc`、`deploy/runtime/` 或数据
卷；刷新前的受管工具文件会备份到 `.sforum/tooling-backups/`。

首次从旧版直连端口拓扑更新时，脚本会要求确认一次性 blue/green 入口转换
（输入 `BOOTSTRAP`，或传 `--bootstrap`）。这一步需要短暂停止旧服务并启动
稳定 Caddy 入口。转换完成后，数据库不变或只含已声明向后兼容 Core 迁移的
版本可以保持 HTTP 在线切换；未声明 Core 迁移、任何 River 迁移，或旧版
迁移镜像无法通过在线安全检查时，需要维护窗口。完整语义见
[一键安装与更新](./deployment/one-click.md)。

## 目标形态

| 服务 | 职责 |
| --- | --- |
| `web` | Nuxt 生产输出；同域代理普通 HTTP API |
| `api` | Fiber API；扩展运行时；内嵌 River Worker；可选 WebSocket 入口 |
| `postgres` | 业务数据、River 队列、迁移版本 |
| `redis` | 服务端会话、缓存、限流 |

直连拓扑只发布两个 loopback 端口：

| 用途 | 默认值 |
| --- | --- |
| Web | `127.0.0.1:${WEB_PORT:-3000}` |
| API / WebSocket Upgrade | `127.0.0.1:${API_PORT:-18080}` |

普通 `/api/v1/*` 由 Nuxt 同域转发到 `api:8080`。宿主机 Caddy/Nginx 示例在
部署包的 `deploy/caddy/Caddyfile`；WebSocket Upgrade 可直打 API 端口。
真实客户端 IP 默认已按这个拓扑配好（信任私网对端、不信任 loopback），宿主机
反代不需要再往 `TRUSTED_PROXIES` 里写自己的地址。前面有 CDN 时，CDN 出口段写进
Caddy 的 `servers.trusted_proxies`，由 Caddy 剥掉 CDN 头并重算真实客户端；应用侧
仍然留空，永远不要信任所有网段。

## 维护者发布

维护者从已同步到 `origin/main` 的干净 `main` 工作区创建版本：

```sh
./scripts/release.sh
```

脚本推送标签后默认立即返回；只有 `--wait` 才占用终端等待结果。交互发布可
填写一行发布重点，或用 `--notes-file` 提供多行 Markdown。Release 复用相同
提交已触发的 `main` CI，只有精确 SHA 的 push CI 成功后才构建、扫描和提升
镜像。GitHub Release 同时发布：

- `linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64` 的 `sforum`
  管理 CLI；Windows 不在支持范围内。
- `linux/amd64`、`linux/arm64` 后端运行包，包含 API、迁移、CLI 和从同一
  已扫描候选镜像提取的精确内置扩展。
- 固定名称 `sforum-bootstrap.sh`、`sforum-deploy.tar.gz` 和兼容用
  `upgrade.sh`。
- 覆盖全部压缩包的 `SHA256SUMS` 和 GitHub 构建来源证明。

后端运行包不包含 Nuxt Web、PostgreSQL 或 Redis，不是完整站点安装包；生产
仍使用三个版本一致的 Docker 镜像。下载后可用
`gh attestation verify <文件> --repo keeptrue/SForum` 和 `SHA256SUMS`
核对。

## 深入阅读

- [一键安装与更新](./deployment/one-click.md)：bootstrap、deploy.sh、
  upgrade.sh 的完整执行链、参数、状态文件与失败语义。
- [手动 Compose 部署](./deployment/manual.md)：不用脚本，从下载部署包到
  首个管理员、反向代理、更新与回滚。
- [环境变量与密钥](./deployment/environment.md)：`.env.production` 的加载
  方式、必填项、生成与轮换、Compose 透传边界。
- [故障排查](./deployment/troubleshooting.md)：症状、判定、处置，以及
  `recovery_required` 的恢复路径。

## 上线后清单

1. 注册第一个账号；它会成为受保护的 `super_admin`。
2. 在站点设置里确认站点名称、HTTPS URL、语言与外观。
3. 配置邮件（内置 SMTP 插件），让密码重置和通知可用。
4. 选择附件存储提供方。
5. 搜索默认用站点 PostgreSQL FTS；需要再启用 Meili 插件。
6. 演练一次扩展信任与 Safe Mode。
7. 检查 `/health`、`/api/v1/health`、`/api/v1/ready`。
8. 设置备份保留周期，并把备份放到宿主机磁盘或异地存储。

## 相关

- 开发环境：[快速开始](./getting-started.md)
- 站长：[使用说明](./usage/README.md)
- 历史长文归档：`docs/archive/legacy-root/development-and-deployment.md`
