# SForum Documentation (English)

[简体中文](../zh-CN/README.md) · [Docs hub](../README.md)

SForum is a maintainable, plugin-first open-source forum framework: core is the host and contracts; verticals and vendor behavior live in extensions.

## Read by role

| You are… | Start here |
| --- | --- |
| Running it for the first time | [Getting started](./getting-started.md) |
| Site operator | [Usage](./usage/README.md) |
| Contributor / integrator | [Development](./development/README.md) |
| Shipping to production | [Deployment](./deployment.md) |
| Plugin / theme author | [Extensions (ops)](./usage/extensions.md) → [Authoring guide](../extensions/authoring-guide.md) |
| Product / architecture | [Product](./product.md) · [Architecture](./architecture.md) · [Roadmap](./roadmap.md) |

## Contents

### Usage

- [Usage overview](./usage/README.md)
- [First registration & super admin](./usage/first-login.md)
- [Account & security](./usage/account-security.md)
- [Admin control panel](./usage/admin.md)
- [Forum day-to-day](./usage/forum.md)
- [Notifications](./usage/notifications.md)
- [Search](./usage/search.md)
- [Extensions & themes (operators)](./usage/extensions.md)

### Development

- [Development overview](./development/README.md)
- [Environment setup](./development/setup.md)
- [Daily workflow](./development/workflow.md)
- [Developer CLI](./development/cli.md)
- [API usage](./development/api.md)
- [Testing & gates](./development/testing.md)
- [Repository map](./development/repository.md)

### Other

- [Deployment](./deployment.md)
- [One-click install and update](./deployment/one-click.md)
- [Manual Compose deployment](./deployment/manual.md)
- [Environment and secrets](./deployment/environment.md)
- [Deployment troubleshooting](./deployment/troubleshooting.md)
- [Architecture](./architecture.md)
- [Product](./product.md)
- [Roadmap](./roadmap.md)

## Technical reference (path-stable)

These paths are referenced by CI and generators—do not move casually:

- [Plugin authoring guide](../extensions/authoring-guide.md)
- [Host API v2](../extensions/host-api-v2.md)
- [Plugin badge facts](../extensions/badge-plugin-facts.md)
- [Runtime themes](../extensions/runtime-themes.md)
- [Generated catalogs](../extensions/catalogs/)
- [V3 platform docs](../extensions/v3/)

### Extension authoring (translations)

- [Plugin routes (中文)](../zh-CN/extensions/routes.md) · [English (canonical)](../extensions/routes.md)
- [Build, digest, and load (中文)](../zh-CN/extensions/build-and-load.md) · [English (canonical)](../extensions/build-and-load.md)
- [Plugin badge facts (中文)](../zh-CN/extensions/badge-plugin-facts.md) · [English (canonical)](../extensions/badge-plugin-facts.md)

## Docs vs knowledge

| Location | Purpose |
| --- | --- |
| `docs/zh-CN` / `docs/en-US` | Human handbooks for ops and development |
| `docs/extensions` | Extension contracts and generated catalogs |
| `knowledge/` | Decisions, module status, session handoffs |
