# 2026-10-27 Session Handoff — 交互式安装目录

## Changed

- `sforum-install.sh`：新增唯一的交互步骤——安装目录询问。默认 `$PWD/sforum`；
  当前目录本身已是安装目录时默认当前目录（不再嵌套第二层 `./sforum`）。提示与
  回显都显示绝对路径；`/`、已存在普通文件、不可写位置被拒绝并重问；非空且非安装
  目录追加一次 `[y/N]`，答否回到目录询问。路径解析用纯词法 + `pwd -P`（不依赖
  `realpath`），创建目录延后到目标版本解析成功之后。
- 新增 `SFORUM_NONINTERACTIVE=1`：跳过目录询问并给 bootstrap 传 `--yes`。
  `--yes` / `--defaults` / 无终端同样跳过询问，但语义仍是"用推荐默认值"。
- `sforum-bootstrap.sh` 交接前打印 `Install directory:`。
- `deploy.sh` / `upgrade.sh`：成功与 `status` 打印安装目录、配置文件、备份目录；
  `.deployrc` 记录 `install_dir=`（`upgrade.sh` 的 `persist_state` 整体重写，
  两处都必须带）。记录的目录与当前目录不一致时打印提示而不是静默忽略。
- `sforum-latest.sh` 启动打印安装目录；`sforum-latest.sh.sha256` 已重新生成。
- 测试：`install-loader_test.sh` 增加 pty 交互矩阵（默认/相对/绝对/拒绝根目录/
  非空确认与拒绝/原地安装/`--yes`/`SFORUM_NONINTERACTIVE`），非交互用例统一钉
  `SFORUM_NONINTERACTIVE=1`；`deploy_test.sh`、`bootstrap_test.sh`、
  `zero-downtime-state_test.sh` 增加路径输出与 `install_dir=` 断言。
- 文档：中英 `one-click.md` 新增「安装目录」小节并改写 loader 职责描述；README 与
  两份 `deployment.md` 快速安装段同步；`validate-docs.mjs` 把
  `SFORUM_NONINTERACTIVE` 加入 one-click 必需标记。

## Decisions

- 只有一行 loader 询问目录；本地入口保持"装在脚本所在目录"，只打印绝对路径。
  见 `knowledge/decisions/2026-10-27-interactive-install-directory.md`（修订
  `2026-10-27-one-line-install-loader.md`）。
- loader 仍然不写任何安装状态，只读取 `.env.production`/`.deployrc` 的存在与版本。

## Next

- 若之后要支持"在本地入口换目录"，正确做法是先复制整包工具链再执行，不要在
  bootstrap 里就地猜测目标目录。
- `sforum-latest.sh` 目前是中文单语，若要做双语需同时更新其 sha256 与文档。

## Open Questions

- `SFORUM_NONINTERACTIVE` 是否也要抑制 `configure-production.sh` 的逐项确认？
  当前它只传 `--yes`，向导在 `--defaults` 下已经全默认，行为一致。
