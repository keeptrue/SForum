# SForum Documentation

Official project documentation. Choose a language:

| Language | Path | Audience |
| --- | --- | --- |
| **简体中文** | [`zh-CN/`](./zh-CN/README.md) | 默认语言；站长使用、本地开发、部署 |
| **English** | [`en-US/`](./en-US/README.md) | Operators, contributors, developers |

## Quick links

| Topic | 中文 | English |
| --- | --- | --- |
| Getting started | [快速开始](./zh-CN/getting-started.md) | [Getting started](./en-US/getting-started.md) |
| Operator usage | [使用说明](./zh-CN/usage/README.md) | [Usage](./en-US/usage/README.md) |
| Account & security | [账户与安全](./zh-CN/usage/account-security.md) | [Account & security](./en-US/usage/account-security.md) |
| Development | [开发指南](./zh-CN/development/README.md) | [Development](./en-US/development/README.md) |
| API usage | [API 使用](./zh-CN/development/api.md) | [API usage](./en-US/development/api.md) |
| Deployment | [生产部署](./zh-CN/deployment.md) | [Deployment](./en-US/deployment.md) |
| One-click install/update | [一键安装与更新](./zh-CN/deployment/one-click.md) | [One-click install/update](./en-US/deployment/one-click.md) |
| Manual Compose | [手动部署](./zh-CN/deployment/manual.md) | [Manual Compose](./en-US/deployment/manual.md) |
| Environment & secrets | [环境变量与密钥](./zh-CN/deployment/environment.md) | [Environment & secrets](./en-US/deployment/environment.md) |
| Deployment troubleshooting | [部署故障排查](./zh-CN/deployment/troubleshooting.md) | [Deployment troubleshooting](./en-US/deployment/troubleshooting.md) |
| Architecture | [架构](./zh-CN/architecture.md) | [Architecture](./en-US/architecture.md) |
| Product | [产品说明](./zh-CN/product.md) | [Product](./en-US/product.md) |
| Extensions (tech) | [扩展参考](./extensions/) | [Extension reference](./extensions/) |
| Plugin badge facts | [插件徽章事实](./zh-CN/extensions/badge-plugin-facts.md) | [Plugin badge facts](./en-US/extensions/badge-plugin-facts.md) |

## Layout

```text
docs/
├── README.md                 # this hub
├── zh-CN/                    # Chinese handbook (maintained in parallel)
│   └── deployment/           # install/update, manual, environment, troubleshooting
├── en-US/                    # English handbook (maintained in parallel)
│   └── deployment/           # same structure as zh-CN/deployment
├── extensions/               # technical extension contracts (path stable for CI)
│   ├── authoring-guide.md
│   ├── badge-plugin-facts.md   # plugin badgeConditions + badges.fact.record
│   ├── migrating-from-sforum-v2.md   # SForum-old -> V3 mapping + quick starts
│   ├── catalogs/             # generated host catalogs
│   └── v3/                   # V3 governance, matrices, evidence
└── archive/                  # historical plans, old root drafts, security notes
```

## Conventions

1. **Bilingual handbooks** under `zh-CN/` and `en-US/` stay structurally parallel
   (same filenames and section order). When you change one locale, update the other
   in the same change when practical.
2. **`docs/extensions/`** is the machine-checked technical surface (catalogs, Host
   API, V3 matrices). Do not move these paths without updating generators and tests.
3. **`knowledge/`** is internal session memory (decisions, module status, handoffs).
   Prefer `docs/` for durable operator/developer guides.
4. **`contracts/`** holds OpenAPI/Protobuf; link from docs, do not duplicate schemas.

## Related paths outside `docs/`

- Repository rules for agents: `AGENTS.md`
- Living project memory: `knowledge/index.md`
- API contracts: `contracts/openapi.yaml`
- Extension packages: `extensions/`
