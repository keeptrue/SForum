# 环境搭建

[← 开发指南](./README.md)

## 1. 克隆与工具

```sh
git clone <your-fork-or-upstream> SForum
cd SForum
```

安装：

- Docker Desktop（或兼容的 Docker Engine + Compose）
- Go 1.26.10+（工具链锚定于 `apps/api/go.mod`，文档要求以它为准）
- Air：`go install github.com/air-verse/air@latest`
- Bun（前端）
- 可选：Ruby（校验 OpenAPI refs：`ruby scripts/validate-openapi-refs.rb`）

### 网络与代理

在中国大陆环境执行 `go get` / `bun install` / `bun add` 等联网命令前，按 `AGENTS.md` 设置本地代理，例如：

```sh
export https_proxy=http://127.0.0.1:7897
export http_proxy=http://127.0.0.1:7897
export all_proxy=socks5://127.0.0.1:7897
```

## 2. 环境变量

```sh
./scripts/dev.sh   # 首次会从 .env.example 生成 .env
```

也可手动：

```sh
cp .env.example .env
```

说明：

- `config.Load` **不会**自动读 `.env`；开发脚本负责 source/export  
- 站点运行时选项多数可在后台改；`.env` 多为启动回退值与基础设施连接  
- 生产使用 `.env.production` + `deploy.sh`，见 [生产部署](../deployment.md)

## 3. 启动开发栈

见 [快速开始](../getting-started.md) 三步流程。推荐：

| 终端 | 命令 |
| --- | --- |
| 1 | `./scripts/dev.sh` |
| 2 | `./scripts/api-dev.sh` |
| 3 | `cd apps/web && bun run dev` |

### 前端依赖

```sh
cd apps/web
bun install
bun run dev
```

API 始终内嵌并拥有 Worker。`scripts/worker-dev.sh` 仅保留为旧命令的明确报错，
避免误启动第二套消费者与插件进程。

## 4. 编辑器建议

- 打开仓库根为 workspace  
- Go 模块路径：`github.com/keeptrue/sforum/apps/api`  
- 前端在 `apps/web`，包名 `@sforum/web`  

## 5. 常见问题

| 现象 | 处理 |
| --- | --- |
| API 端口被占用 | `api-dev.sh` 只回收本仓库 `sforum-api`；若是 Docker/其他进程占用会拒绝启动 |
| 3000 被占用 | 默认视为用户自己的 Nuxt，勿 kill |
| 内置插件 digest | `api-dev.sh` 使用 staging 内置树，避免改写 git 跟踪的 manifest |
| go/bun 下载失败 | 检查代理与 DNS |

## 下一步

- [日常工作流](./workflow.md)  
- [测试与质量门禁](./testing.md)  
