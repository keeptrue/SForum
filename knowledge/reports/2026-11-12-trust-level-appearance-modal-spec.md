# 信任等级「外观设置」Modal — 视觉规格 / 深色复核 / 文案定稿

- 日期：2026-11-12
- 作者：小设（designer）
- 范围：只读复核 + 规格产出。未改动 `apps/web` 下任何文件。
- 取代：档位表行内展开的「外观」面板（`admin.forum.trustLevels.tiers.appearance`）。

## 0. 不变量（这次改动不能破的四条）

1. **候选色只有一个来源**：推荐色板仍来自服务端 `colorChoices`（9 项）。前端不新增第二份色表；自定义 hex 是「用户输入值」，不是候选表。
2. **选中态双通道**：环线（`--sf-accent` 边框 + `--sf-accent-soft` 底）+ 14px 对勾，沿用 `SFAdminTrustLevelColorPicker.vue` 现在的写法，不只靠颜色区分。
3. **色块 32×32 不缩小**：WCAG 2.5.8 触达最小尺寸。断点只调间距，不调块大小。
4. **深色不另配一套色值**：深色下的识别色由 `color-mix(in srgb, <hex> 74%, white)` 现算，不落第二个 token 文件（`sforum-trust-colors.css` 仍是唯一取值来源）。

## 1. Modal 骨架

| 项 | 桌面（≥640px） | 390 窄屏 |
|---|---|---|
| 宽度 | `min(560px, 92vw)` | 全宽 − 24px（左右各 12px） |
| 圆角 | 14px | 顶部 16px / 底部 0（贴底 sheet 观感） |
| 最大高度 | `min(86dvh, 720px)` | `calc(100dvh - 48px)` |
| 内边距 | header/footer 16×18，body 16×18 | 14×14 |
| 分段 | header（不滚）/ body（唯一滚动区）/ footer（吸底） | 同 |

- `role="dialog"` + `aria-modal="true"` + `aria-labelledby` 指标题。遮罩 `--sf-overlay`，不做 blur。
- body 与 footer 之间用 1px `--sf-border` 顶线分隔；body 底部补 8px padding，避免最后一块内容贴在分隔线上。

## 2. 推荐色板几何

沿用现有组件语言：`display:flex; flex-wrap:wrap;` 每项 32×32、圆角 8、`border:1px solid var(--sf-border)`，圆点 16px（`inset 0 0 0 1px rgb(0 0 0 / .12)` 内描边保留，浅色 dot 在纸色主题上才不会糊）。

| 断点 | 每行 | gap | 单行总宽 | 可用内容宽 | 结论 |
|---|---|---|---|---|---|
| ≥640px | 9 | 6px | 336px | ≈ 524px | 单行，左对齐 |
| <640px（390） | 9 | 4px | 320px | ≈ 334px | 单行，左对齐 |

9 色必须在两个断点都**保持一行**：折成 8+1 的最后一块会被读成「不同类别」。390 那条线只有 14px 余量，`gap` 必须走 4px，不能吃 6px（6px 时 336px 溢出 2px，出现横向滚动条）。

选中态（与图标选择器逐字同构）：

```css
.sf-tl-appearance__swatch[data-selected="true"] {
  border-color: var(--sf-accent);
  background: var(--sf-accent-soft);
}
.sf-tl-appearance__check {          /* 第二通道 */
  position: absolute; top: -4px; right: -4px;
  width: 14px; height: 14px; border-radius: 999px;
  border: 1px solid var(--sf-card);
  background: var(--sf-fg); color: var(--sf-card);
}
```

### 2.1 裁决：「自动」不再占色块位

旧面板里「按位次自动」是第 10 个 32px 方块。10 块在 390 上必然折行（10×32+9×4=356 > 334），折出来的孤块正好是「自动」，语义上很容易被误读成「第 10 种颜色」。

改成**独立一行 chip**，放在推荐色板**下方**、仍是同一个 radio group 的成员：

```
[ ● 16px 色点 ]  按位次自动（当前为 紫罗兰）        ← 24px 高、圆角 8、data-selected 时环线+底色
```

- 色点直接画该档的默认色（`tierColorVar(defaultColor)`），运营一眼看到「自动」长什么样。
- 文案复用 `admin.forum.trustLevels.colors.auto` + `colors.autoWith` 的思路，不再需要 8px 的「自动」角标（`colors.autoBadge` 在本 modal 内可不再使用）。
- 好处：9 色网格在两个断点都是干净的单行，且「自动」从抽象开关变成一句看得懂的句子。

## 3. 自定义行（`input[type=color]` + hex 文本 + 应用）

单行三段，两个断点同构（最窄 390 也放得下，不需要堆叠）：

```
[色块 36×32]  [ #7C3AED        ]  [ 应用 ]
   8px gap        96px 宽            56px
```

| 元素 | 规格 |
|---|---|
| 取色块 | `input[type="color"]`，36×32、圆角 8、`border:1px solid var(--sf-border)`；必须清掉 UA 内衬：`padding:0; &::-webkit-color-swatch-wrapper{padding:0} &::-webkit-color-swatch{border:0;border-radius:7px}` |
| hex 文本 | 96px 宽、32px 高、等宽字体（`ui-monospace`）、**显示统一大写**、placeholder `#RRGGBB`、`maxlength=7`、`spellcheck=false`、`autocapitalize=off` |
| 应用按钮 | 次要样式（非主色），32px 高、横向 padding 12；文案「应用」；`disabled` 条件 = 输入非法（不隐藏按钮，避免布局跳动） |
| 行标签 | 行首上方 12px `--sf-fg-secondary`「自定义颜色」，与推荐色板标题同级 |

交互口径：

- 取色块 `input` 事件只更新**本地预览**（不提交），点「应用」才写入表单；文字输入同理。
- 非法态：输入框 `border-color: var(--sf-danger)` + 下方 11px 一行提示（文案见 §6 `invalidHex`），宽度变化不得推动整行重排（提示区常驻一行高度）。
- 应用成功后不回 toast（modal 内就地反馈），仅把该色块置为选中态并把 hex 规整成大写。

## 4. 明/暗双底预览卡

目的：让运营在**保存前**看到这个色在两套页面上的真实样子。窗口一关就看不到的预览没有价值，所以两张卡必须并排。

| 项 | 规格 |
|---|---|
| 排布 | 2 列 grid，`gap 12px`（390 时 10px），**永不堆叠**（堆起来就失去对比意义）|
| 卡片高 | 88px（390 时 84px），`border-radius 10px`，`padding 12px`（390 时 10px）|
| 明卡底 | 固定 `#ffffff`，`border:1px solid #e5e7eb` |
| 暗卡底 | 固定 `#1b201f`（与公开页实测深色卡面同一值），`border:1px solid #2a3230` |
| 卡内顶部 | 11px 标签：`previewLight` = 浅色页面 / `previewDark` = 深色页面；明卡上 `#6b7280`、暗卡上 `#9ca3af` |
| 卡内主体 | `sf-tl-mark`（28px，`data-step="2"` 即「34% 环 + 淡底」）+ 右侧档位名（14px/600）+ 其下 11px 一句 `#RRGGBB` |
| 文字色 | 明卡 `#111827` / `#4b5563`；暗卡 `#f3f4f6` / `#9ca3af` |

**两张卡用固定色，不用主题变量。** 后台本身切了深色时，若预览卡跟着 `var(--sf-card)` 走，两张卡会同时变暗 —— 「明/暗预览」当场失去意义。这里是全篇唯一允许硬编码颜色的地方，理由写在注释里。

## 5. footer 与内部滚动边界

- footer `position: sticky; bottom: 0`，`background: var(--sf-card)`，`border-top: 1px solid var(--sf-border)`，`padding: 12px 18px`，`z-index: 1`。
- 滚动**只发生在 body**：`overflow-y: auto; overscroll-behavior: contain;` header / footer 不参与滚动。body `max-height` = modal 高 − header − footer，不写死数值。
- 按钮布局：`justify-content: space-between`；左「恢复默认外观」（ghost/text 按钮，13px），右「完成」（primary，主色）。390 下仍单行：左 96px + 右 64px 远小于 334px，不缩字、只缩 padding（按钮高恒 40px）。
- 390 键盘弹起后（`dvh` 收缩）footer 必须仍在可视区：modal 高度用 `dvh` 而不是 `vh`，body 自己缩。
- 图标段是最高的一段（全量 `SFIconPicker`），它自己承担卡片内滚动 / 分页（`ICON_PICKER_PAGE_SIZE`）；**不要**在 modal body 里再嵌一个固定高的滚动区，两层滚动条是这版最容易踩的坑。

## 6. 深色适配复核

公式：`color-mix(in srgb, <hex> 74%, white)`（按 srgb 通道线性混合，等价于 `0.74·c + 0.26·255`）。基准卡面 `#1b201f`（相对亮度 L = 0.0136）。

| 输入 hex | 混合结果 | 对 `#1b201f` 卡面 | 原色直出对比 | 判定 |
|---|---|---|---|---|
| `#111827` | `#4f545f` | **2.17** | 1.08 | 不合格（<3:1，提亮也救不回）|
| `#fde047` | `#fee877` | 13.37 | 12.51 | 合格（但见下方「过亮」提示）|
| `#ff0000` | `#ff4242` | 4.80 | 4.13 | 合格 |
| `#7c3aed` | `#9e6df2` | 4.65 | 2.89 | 合格（**直出会挂，靠混合救回**）|
| `#0f766e` | `#4d9a94` | 5.00 | 3.01 | 合格（直出贴线）|
| `#166534` | `#538d69` | 4.23 | 2.31 | 合格（直出挂）|
| `#94a3b8` | `#b0bbca` | 8.49 | 6.43 | 合格 |
| `#f472b6` | `#f797c9` | 8.03 | 6.23 | 合格 |
| `#2dd4bf` | `#64dfd0` | 10.22 | 8.86 | 合格 |
| `#eab308` | `#efc748` | 10.16 | 8.60 | 合格 |

阈值反解：要 3:1，混合结果亮度需 ≥ 0.1409。中性灰落在 `#27303f`（2.85，未过）与 `#334155`（3.39，已过）之间，**边界约 `#2b3542`**。

### 6.1 结论（给运营的一句话规则）

- **不该被推荐的：近黑色。** 所有亮度低于 `#334155` 这一级的输入（`#111827`、`#0b1220`、`#1e293b`、`#27303f`）在深色页上即便提亮也只有 1.9–2.9:1，识别色会糊进 `#1b201f`。这类色必须靠**形状/描边**才看得见，不适合当档位识别色。
- **提亮公式真正救的是饱和中间色**：`#7c3aed`（2.89→4.65）、`#166534`（2.31→4.23）、`#0f766e`（3.01→5.00）。这三个直出会挂，混合后合格 —— 所以公式不是装饰，必须落地。
- **反方向也要管**：`#fde047` 在深色卡上 13.37 很亮，但它对**白色**卡面只有 1.32:1，`#94a3b8` 2.56、`#f472b6` 2.65 同理。极亮色在明卡上必须靠 `sf-tl-mark` 的环/淡底（`data-step`）撑出边界，不能只靠色相平铺。
- 前端落法：自定义 hex 提交前算一次「深色混合后对 `#1b201f` 的对比度」，`< 3` 时给一行温和提示（见 §7 `lowContrastHint`，可选键）。**只提示不阻断** —— 运营可能有自己的深色页面底色。

## 7. 文案定稿（可直接粘贴）

键名前缀 `admin.forum.trustLevels.appearanceDialog.`。新手友好：不出现「token / 变量 / 白名单 / 十六进制」这类词；不卖萌；无 emoji。

```json
{
  "zh-CN": {
    "admin": {
      "forum": {
        "trustLevels": {
          "appearanceDialog": {
            "title": "外观设置",
            "subtitle": "这里改的是这一档在页面上长什么样，不会改变谁能升到这一档。",
            "previewLight": "浅色页面",
            "previewDark": "深色页面",
            "iconTitle": "图标",
            "iconHint": "挑一个最能代表这一档的图标。不选就用默认的。",
            "iconReset": "用回默认图标",
            "colorTitle": "颜色",
            "colorRecommended": "推荐颜色",
            "colorCustom": "自定义颜色",
            "colorCustomHint": "想用别的颜色？点左边的色块挑一个，或者直接填 #RRGGBB。",
            "colorInvalidHex": "颜色格式不对，请填成 #7C3AED 这样的六位色值。",
            "colorApply": "应用",
            "colorLowContrast": "这个颜色在深色页面上会有点看不清，建议换亮一点的。",
            "footerReset": "恢复默认外观",
            "footerDone": "完成"
          }
        }
      }
    }
  },
  "en-US": {
    "admin": {
      "forum": {
        "trustLevels": {
          "appearanceDialog": {
            "title": "Appearance",
            "subtitle": "This only changes how the tier looks on the page. It does not change who can reach it.",
            "previewLight": "Light page",
            "previewDark": "Dark page",
            "iconTitle": "Icon",
            "iconHint": "Pick the icon that best represents this tier. Leave it empty to use the default.",
            "iconReset": "Use default icon",
            "colorTitle": "Color",
            "colorRecommended": "Recommended colors",
            "colorCustom": "Custom color",
            "colorCustomHint": "Want a different color? Click the swatch on the left, or type a value like #7C3AED.",
            "colorInvalidHex": "That is not a valid color. Use a six-digit value like #7C3AED.",
            "colorApply": "Apply",
            "colorLowContrast": "This color will be hard to see on the dark page. Try a lighter one.",
            "footerReset": "Reset appearance",
            "footerDone": "Done"
          }
        }
      }
    }
  }
}
```

必选键 = 除 `colorLowContrast` 外的全部（`colorLowContrast` 与 §6.1 的对比度提示配套，可选实现）。
「自动」一行复用既有 `admin.forum.trustLevels.colors.auto` / `colors.autoWith`，不新增键。

## 8. 给前端的实现提醒

1. `input[type=color]` 只能给 `#rrggbb`，没有 alpha；hex 文本要接受大小写并统一大写回填，`#RGB` 三位写法建议**规整成六位**而不是报错（新手最常输错的形式）。
2. 预览卡的 `sf-tl-mark` 需要祖先上的 `--tl-accent`：直接把 `tierAccentStyle(customHex || token)` 挂在卡上，不要给 `.sf-tl-mark` 本身声明 `--tl-accent`（会遮蔽祖先内联值，见 `sforum-trust-colors.css` 第 38–53 行的坑）。
3. 深色预览卡里的 `--tl-accent` 要传**混合后**的值（`color-mix(in srgb, <hex> 74%, white)`），否则暗卡上看到的是直出色，与实际页面不符。
4. 9 色单行在 390 只有 14px 余量：任何给色块加的 margin 都会导致折行，间距一律走 `gap`。
5. `data-sf-*` 观测点沿用现有命名（`data-sf-trust-tier-color-picker`、`data-sf-color-choice`、`data-sf-color-picker-auto`），QA 脚本不用改选择器。
