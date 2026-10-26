# 决策：内嵌 tzdata，而不是在运行镜像安装 tzdata

日期：2026-10-18。

## 背景

容器化实例的「站点设置 → 基础设置 → 时区与时间格式」保存非 UTC 时区时返回
HTTP 422 `options.invalid`。根因是运行镜像 `apps/api/Dockerfile` 最终阶段
`FROM alpine:3.22 AS api` 只装 `ca-certificates`，没有 `/usr/share/zoneinfo`；
API/worker 以 `CGO_ENABLED=0` 构建，且没有任何包 import `time/tzdata`。
`time.LoadLocation("Asia/Shanghai")` 在镜像内直接返回 `unknown time zone`
（发布镜像 `ghcr.io/keeptrue/sforum-api:v3.0.9` 实测），而 `UTC` 被 Go
特判所以能保存。本机 dev（宿主有 zoneinfo）无法复现。

同一根因还影响插件定时任务：manifest 的 `schedule.Timezone` 校验与 River
periodic schedule 解析同样调用 `time.LoadLocation`。

## 决定

1. 新增叶子包 `apps/api/app/Support/Timezone`：blank import `time/tzdata`，
   对外只暴露 `Load(name) (*time.Location, error)`。
2. 宿主侧所有时区解析（选项校验、manifest 校验、插件调度）统一走
   `timezone.Load`。
3. 不在 Dockerfile 里 `apk add tzdata`。

## 理由

- 自包含：内嵌数据对所有部署形态生效（alpine、scratch、distroless、单二进制
  分发），不会因为换镜像或裁镜像复发。
- 体积：约 450KB，低于安装 tzdata 包的 2–3MB，且只进链接该包的二进制。
- 覆盖完整：`options`、`Jobs`、`ExtensionManifest` 三个调用点都经过同一个包，
  新调用点也自然继承。
- 系统时区库仍优先，`time.LoadLocation` 的解析顺序与语义不变。

## 代价与边界

- 每个链接该包的二进制增大约 450KB。
- 内嵌时区规则跟随 Go 工具链版本（当前 1.26.9），只有升级 Go 才会更新。
- 插件独立后端进程不会自动继承；若插件自身调用 `time.LoadLocation`，需要在
  自己的构建里内嵌（当前 `extensions/` 下无此调用）。
- 空时区仍解析为 UTC（与标准库一致）；需要显式时区的调用方自己拒绝空值
  （`normalizeSiteTimezone` 保留原有空值检查）。

## 守卫

- `tests/validate-runtime-tzdata.mjs`（接入 `scripts/test.sh`）：最终镜像阶段
  必须安装 `tzdata`，或 `Timezone` 包必须内嵌 `time/tzdata`；同时禁止
  `app/Support/Timezone` 之外出现裸 `time.LoadLocation(`。
- 容器验收：`CGO_ENABLED=0` 构建的二进制在 `alpine:3.22` 内解析
  `Asia/Shanghai` / `America/New_York` / `Europe/London` 全部成功。
