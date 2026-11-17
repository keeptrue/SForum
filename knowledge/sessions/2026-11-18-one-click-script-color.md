# 2026-11-18 一键脚本终端配色层

## Changed

- 新增 `deploy/scripts/term-ui.sh`：一键安装/更新链路唯一的配色实现（`ui_init_color` +
  `ui_banner` / `ui_step` / `ui_ok` / `ui_warn` / `ui_err` / `ui_key` / `ui_prompt`）。
  开关优先级 `SFORUM_COLOR=never` / 非空 `NO_COLOR` / `TERM=dumb` → 无色，否则
  `SFORUM_COLOR=always` 或 stdout 是终端 → 上色，其余（重定向、CI 日志）纯文本。
- `sforum-install.sh`、`sforum-bootstrap.sh`、`sforum-latest.sh` 内联同一份
  `# >>> term-ui` … `# <<< term-ui` 块（它们运行在 `deploy/scripts/` 还不存在的空目录里）；
  `deploy.sh`、`upgrade.sh` 改为 `source "$ROOT_DIR/deploy/scripts/term-ui.sh"`，
  配置向导 / 健康探针 / 恢复脚本改为 `source "$SCRIPT_DIR/term-ui.sh"`。
- 语义改造：`die` → 错误色（三个入口保留机器前缀在行首字节）、菜单/长动作 → 标题行、
  阶段推进 → `==>`、成功终局 → `[ok]`、可继续警告 → `[!]`、键值/下一步命令 → 强调行、
  交互提问 → 提示色。`upgrade.sh` 把 `say()` 拆成纯文本出口 `_say_text()` + `step/ok/warn/err`
  四个组合函数；`lock_text()`、`t()`、`msg()` 保持纯文本。
- 资产闭包：`scripts/ci/build-deploy-asset.sh` 的 `SOURCE_FILES` 与
  `sforum-bootstrap.sh` 的 `managed_files()` 同时登记 `deploy/scripts/term-ui.sh`。
- 新门 `tests/validate-term-ui.sh`（六条断言：奇偶 / 开关矩阵 / TTY 自动判定 / 行首锚定 /
  帮助文本无色 / `.deployrc` 洁净），接进 `scripts/ci/release-tooling-gate.sh` 与
  `release-tooling.yml` 两个触发块的 paths。
- 既有夹具补 `term-ui.sh`（`deploy_test.sh`、`backup-key_test.sh`、
  `compose-production_test.sh`、`upgrade-version-selection_test.sh`、
  `postgres-safety_test.sh`、`bootstrap_test.sh` 的 mock bundle），未改任何断言。
- 文档：`docs/zh-CN|en-US/deployment/one-click.md` 新增「输出颜色 / Terminal colors」。

## Decisions

- **唯一实现 + 三份内联**，而不是四份独立实现：三个入口必须在空目录里可执行，
  但配色语义只能有一处定义；奇偶门逐字节比对是这条约束的唯一强制手段。
- **整行包裹，不拆色**：拆色（标签一色、值一色）会让 `Install directory: <path>` 这类
  既有 PTY 断言与操作员复制粘贴同时失效；整行包裹保证消息正文一个字节不改。
- **机器前缀留在行首字节**：`tests/validate-sforum-latest.sh` 用 `^sforum-latest: ` 判错，
  颜色从冒号之后开始，`die` 的输出格式因此是 `前缀 + [x] 正文`。
- **颜色只在打印点**：`t()` / `say()` / `lock_text()` 的返回值被 `$(...)` 捕获或被
  `sed`/`awk` 读回（`.deployrc`、`DEPLOY_LOCK_LOG`），一个 ESC 字节就会让状态文件不可解析。
  配置向导的 `prompt_default` 把提示打到 stderr，正是同一条不变量的直接后果。
- **显式开关优先于环境默认关**：`SFORUM_COLOR=always|never` > `NO_COLOR` > `TERM=dumb`
  > TTY 自动判定。`SFORUM_COLOR=always` 压过 `NO_COLOR` 与 `TERM=dumb`（用户 2026-11-18
  裁定，与 no-color.org「程序显式开关优先」的惯例一致）；`SFORUM_COLOR=never` 无条件不上色。
  首轮按任务书逐字实现把 `NO_COLOR` 放进第一分支，已按方案在 task-3 纠正（四处副本同改，
  块 sha256 仍一致）。

## Next

- 配色层已收口：新增入口脚本必须内联（空目录可执行）或 `source` 同一份实现，
  `tests/validate-term-ui.sh` 的奇偶与开关矩阵会拦住漂移。
- `deploy/scripts/backup-postgres.sh` 仍刻意无色（stdout 被 `tail -n 1` 解析），不要顺手统一。
- 蓝绿入口 Caddyfile、`docker compose` 透传输出同样不在配色层范围内。

## Open Questions

- **已决（2026-11-18）**：`SFORUM_COLOR=always` 压过 `NO_COLOR` 与 `TERM=dumb`，即
  「显式开关 > 环境默认关」；见 Decisions 与两份部署文档的优先级段落。
- 是否要把 `deploy/scripts/*.sh` 的 usage 文本也纳入强调行（当前刻意保持纯文本，
  帮助输出必须无色是 `tests/validate-term-ui.sh` 的断言之一）。
