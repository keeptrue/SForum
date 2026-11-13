package notifications

import (
	"context"
	"encoding/json"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 庆祝展示层（presentation.celebration）。
//
// 它是**通知平台的一个展示层**，不是新的通知类型、不是新的授予路径、也不是新的
// Host 命令：徽章授予照旧写通知行（类型 `badge_granted`），这一层只回答
// 「哪些已发生的通知值得在前端弹一张庆祝卡」。
//
// 为什么做成描述符上的标记而不是硬编码类型列表：插件也要能被庆祝
// （`notificationTypes[].celebration`）。Core 只声明自己那一条，插件声明自己的，
// 读面用 `presentation->>'celebration' = 'true'` 一次性覆盖两者。
//
// 三条产品约束（改之前先读 knowledge/decisions/2026-11-13-badge-notification-and-celebration.md）：
//   - **跟随 in_app 偏好**：庆祝是站内展示，所以它读的仍是 `notifications` 行；
//     用户关掉站内渠道时根本没有行可读，庆祝自然消失（不另做一套偏好）。
//   - **首访不回放历史**：没有游标行时返回 initialized=false + 空 items，
//     并把游标种子指向该用户当前最大通知 id。老用户升级后不该被几个月前的徽章刷屏。
//   - **目标不可见就整条丢弃**：复用 `ResolveSafeTargets`，不返回「点了没反应」的空卡。

const (
	// celebrationWindow 是庆祝候选的时间窗：7 天。更早的徽章进资料页看，
	// 不该在登录时突然弹出来。
	celebrationWindow = 7 * 24 * time.Hour
	// DefaultCelebrationLimit 是一次返回的最大条数（前端一次只展示一张卡，
	// 其余靠 `total` 聚合成「获得 N 枚新徽章」）。
	DefaultCelebrationLimit = 3
	// celebrationTotalCap 是 `total` 的上限：它是聚合文案里的数字，
	// 不是分页总数，封顶避免「已获得 4823 枚」这种无信息量的读数。
	celebrationTotalCap = 99
)

// CelebrationText 是 wire 上的双语言文本（键固定 zh/en）。
//
// 与 `extensionmanifest.LocalizedText` 分开：后者是**输入**形状（可写纯字符串或
// locale map），wire 需要的是两个稳定键，让前端不必再猜语言解析规则。
type CelebrationText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// CelebrationItem 是庆祝卡的数据。
//
// `titleKey`/`bodyKey` 是 Core 类型的 i18n 键，插件类型为 null（前端回落
// `label`/`body`）。三者同时存在不是冗余：Core 的文案由前端 i18n 拥有，
// 插件文案由插件清单拥有，服务端只负责选路，不生成句子。
type CelebrationItem struct {
	ID         int64             `json:"id"`
	Type       string            `json:"type"`
	Icon       string            `json:"icon"`
	Label      CelebrationText   `json:"label"`
	Body       CelebrationText   `json:"body"`
	TitleKey   *string           `json:"titleKey"`
	BodyKey    *string           `json:"bodyKey"`
	Params     map[string]string `json:"params"`
	TargetPath *string           `json:"targetPath"`
	CreatedAt  time.Time         `json:"createdAt"`
}

// CelebrationFeed 是 GET /notifications/celebrations 的响应。
//
// `CursorID` 恒有值：initialized=false 时它是「种子游标」（当前最大通知 id），
// 前端据此发第一次 ack；initialized=true 时它是当前离线游标。
//
// **`items` 可能少于 `total`**：`total` 是 SQL 候选数（封顶 99），`items` 是过了
// 目标可见性之后的子集（徽章被收回/停用会让条目整条消失）。前端约定：
// **`items` 为空时不渲染任何庆祝 DOM**——不要用 `total` 去决定是否弹卡。
type CelebrationFeed struct {
	Initialized bool              `json:"initialized"`
	CursorID    int64             `json:"cursorId"`
	Total       int64             `json:"total"`
	Items       []CelebrationItem `json:"items"`
}

// CelebrationStore 是庆祝读面的持久化能力（PostgresStore 实现；测试可替换）。
type CelebrationStore interface {
	// CelebrationCursor 返回 (游标, 是否存在游标行)。
	// 存在游标行 = 用户已经「初始化」过庆祝层（首访或个人设置里 ack 过）。
	CelebrationCursor(ctx context.Context, userID int64) (int64, bool, error)
	// LatestNotificationID 是该用户当前最大通知 id（无通知为 0）。
	LatestNotificationID(ctx context.Context, userID int64) (int64, error)
	// ListCelebrationRows 返回 id > afterID 的庆祝候选（按 id 升序，最多 limit 条）
	// 与同条件的总数（封顶 celebrationTotalCap）。
	ListCelebrationRows(ctx context.Context, userID int64, afterID int64, limit int) ([]CelebrationRow, int64, error)
	// AcknowledgeCelebrationCursor 用 GREATEST 语义推进游标（不倒退），返回落库值。
	AcknowledgeCelebrationCursor(ctx context.Context, userID int64, cursorID int64) (int64, error)
}

// CelebrationRow 是读面的原始一行（通知 + 描述符展示字段）。
type CelebrationRow struct {
	ID           int64
	Type         string
	TargetType   string
	TargetID     int64
	Payload      json.RawMessage
	Presentation json.RawMessage
	CreatedAt    time.Time
}

// descriptorPresentation 是 `notification_type_descriptors.presentation` 里庆祝层用到的字段。
type descriptorPresentation struct {
	Icon  string                          `json:"icon"`
	Label extensionmanifest.LocalizedText `json:"label"`
	Body  extensionmanifest.LocalizedText `json:"body"`
}

// coreCelebrationFallback 是 Core 庆祝类型的兜底文案（两语言都给）。
//
// 为什么服务端要准备这句话：前端在「i18n 模板缺少参数」时会回落到 `body` 纯文本，
// 而 Core 描述符的 presentation 里只有 icon（`202611100002` 的种子就是
// `{"icon":"i-lucide-award"}`），回落出来会是空串或描述符默认值 `Notification`——
// 前者是一张空卡，后者是错误文案。于是这条兜底只负责「有实义、可读」，
// **不承担翻译职责**：正常路径仍是前端 i18n 的 `titleKey`/`bodyKey`。
//
// 不放 `Support/Localization/messages.go`：那张表是**错误码**文案（API error.code 一一对应），
// 把展示兜底塞进去会让「每个错误码都有双语文案」的完整性测试被一类无关文本污染。
var coreCelebrationFallback = map[string]struct {
	Label CelebrationText
	Body  CelebrationText
}{
	TypeBadgeGranted: {
		Label: CelebrationText{Zh: "徽章", En: "Badge"},
		Body:  CelebrationText{Zh: "你获得了新徽章", En: "You earned a new badge"},
	},
}

// celebrationI18nKeys 给出 Core 庆祝类型的 i18n 键。
//
// 只登记 Core：插件类型的文案在它自己的清单里，服务端不替插件编键名
// （否则「插件文案」会变成 Core 的一堆 if 分支）。
func celebrationI18nKeys(typeID string) (titleKey, bodyKey *string) {
	if typeID != TypeBadgeGranted {
		return nil, nil
	}
	title, body := "celebrations.badge_granted.title", "celebrations.badge_granted.body"
	return &title, &body
}

// celebrationParams 抽取展示需要的参数（只认已知键，不透传 payload）。
//
// 透传 payload 会把「庆祝层能看到什么」变成一张无界表；这里显式列出庆祝卡会用到的
// 键，新增参数必须同时改这里、契约与前端 i18n。
//
// 为什么 `badgeSlug` 在白名单里：庆祝卡要显示与资料页徽章详情**一致**的等级/颜色/
// 说明/条件，而前端只能用 slug 去 `/profiles/{me}/badges` 取**当前**资料。把授予
// 时刻的外观快照塞进通知 payload 是另一种做法，但徽章改名、换档位颜色或停用后，
// 庆祝卡会与资料页对不上——两张卡描述同一枚徽章却长得不一样。
// 这也意味着庆祝卡只该信任 slug：外观随徽章定义实时变化，是特性不是漂移。
//
// `badgeId` 仍然不透传：数字 id 是内部主键，对外一律用 slug；其余 payload 字段
// （插件自定义的、收件人信息等）也一概不认。
func celebrationParams(typeID string, payload json.RawMessage) map[string]string {
	params := map[string]string{}
	if typeID != TypeBadgeGranted || len(payload) == 0 {
		return params
	}
	var decoded struct {
		BadgeName string `json:"badgeName"`
		BadgeSlug string `json:"badgeSlug"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return params
	}
	if decoded.BadgeName != "" {
		params["badgeName"] = decoded.BadgeName
	}
	// 旧通知行没有 badgeSlug（该键是后加的）：缺就**不输出这个键**，不要补空串——
	// 前端按「键存在」决定是否去查当前资料，空串会变成一次必然 404 的请求。
	if decoded.BadgeSlug != "" {
		params["badgeSlug"] = decoded.BadgeSlug
	}
	return params
}

// ListCelebrations 组装庆祝读面。
//
// 返回顺序是 id 升序（最早的先展示）：庆祝卡一次只显示一张，先来的先看，
// 与收件箱的倒序不同——收件箱是「最新最相关」，庆祝是「一次讲一件事」。
//
// 失败语义：解析目标失败只丢那一条（见 ResolveSafeTargets），存储错误才上抛。
func ListCelebrations(ctx context.Context, store CelebrationStore, targets TargetVisibilityResolver, userID int64, limit int) (CelebrationFeed, error) {
	feed := CelebrationFeed{Items: []CelebrationItem{}}
	if store == nil || userID <= 0 {
		return feed, nil
	}
	if limit <= 0 || limit > DefaultCelebrationLimit {
		limit = DefaultCelebrationLimit
	}
	cursor, initialized, err := store.CelebrationCursor(ctx, userID)
	if err != nil {
		return CelebrationFeed{}, err
	}
	if !initialized {
		// 首访：不回放历史。这里**不写**游标行——写了 initialized 就会变成 true，
		// 而前端要靠 false 走「先 ack 再拉取」这一步（见 useCelebrations）。
		latest, err := store.LatestNotificationID(ctx, userID)
		if err != nil {
			return CelebrationFeed{}, err
		}
		feed.Initialized = false
		feed.CursorID = latest
		return feed, nil
	}
	// 取 limit 条即可：是否还有更多由 total（独立 COUNT）回答，这里不需要探测行。
	// 少取一条也不会让 ack 出错——前端上报的是「已消费的最大 id」，而下面的 break
	// 只是防御性兜底（目标不可见的条目会被跳过，理论上仍可能凑不满 limit）。
	rows, total, err := store.ListCelebrationRows(ctx, userID, cursor, limit)
	if err != nil {
		return CelebrationFeed{}, err
	}
	// 用既有 ResolveSafeTargets 过滤：它的语义（不可见就清空 target/payload、
	// 解析失败按不可见处理）就是这一层要的，另写一份只会有两套「可见性」判据。
	page := Page{Items: make([]Notification, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, Notification{
			ID: row.ID, Type: row.Type, TargetType: row.TargetType, TargetID: row.TargetID,
			CreatedAt: row.CreatedAt,
		})
	}
	page, err = ResolveSafeTargets(ctx, targets, userID, page)
	if err != nil {
		return CelebrationFeed{}, err
	}
	feed.Initialized = true
	feed.CursorID = cursor
	feed.Total = total
	for index, item := range page.Items {
		if !item.TargetAvailable || item.TargetPath == "" {
			// 目标不可见：整条丢弃，不返回 targetPath=null 的空卡。
			continue
		}
		if len(feed.Items) >= limit {
			break
		}
		presented := presentCelebration(rows[index])
		targetPath := item.TargetPath
		presented.TargetPath = &targetPath
		feed.Items = append(feed.Items, presented)
	}
	return feed, nil
}

// presentCelebration 把一行原始数据翻成 wire 形状。
func presentCelebration(row CelebrationRow) CelebrationItem {
	var presentation descriptorPresentation
	if len(row.Presentation) > 0 {
		_ = json.Unmarshal(row.Presentation, &presentation)
	}
	titleKey, bodyKey := celebrationI18nKeys(row.Type)
	label := CelebrationText{Zh: presentation.Label.Resolve("zh-CN"), En: presentation.Label.Resolve("en-US")}
	body := CelebrationText{Zh: presentation.Body.Resolve("zh-CN"), En: presentation.Body.Resolve("en-US")}
	// 描述符自己写了文案就用它（运营/插件覆盖优先），否则用 Core 兜底。
	// 逐语言回落而不是整体二选一：描述符只补了中文时，英文仍拿到实义文案。
	if fallback, ok := coreCelebrationFallback[row.Type]; ok {
		if label.Zh == "" {
			label.Zh = fallback.Label.Zh
		}
		if label.En == "" {
			label.En = fallback.Label.En
		}
		if body.Zh == "" {
			body.Zh = fallback.Body.Zh
		}
		if body.En == "" {
			body.En = fallback.Body.En
		}
	}
	return CelebrationItem{
		ID:        row.ID,
		Type:      row.Type,
		Icon:      presentation.Icon,
		Label:     label,
		Body:      body,
		TitleKey:  titleKey,
		BodyKey:   bodyKey,
		Params:    celebrationParams(row.Type, row.Payload),
		CreatedAt: row.CreatedAt,
	}
}

// AcknowledgeCelebrations 推进庆祝游标（GREATEST 语义：给旧的 cursorId 不会回退）。
//
// 为什么不是「置等于」：前端的 ack 是尽力而为的（点击跳转后发、失败不重试），
// 两条 ack 乱序到达时「置等于」会把已经看过的新通知重新弹一遍。
func AcknowledgeCelebrations(ctx context.Context, store CelebrationStore, userID int64, cursorID int64) (int64, error) {
	if store == nil || userID <= 0 {
		return 0, nil
	}
	if cursorID < 0 {
		cursorID = 0
	}
	return store.AcknowledgeCelebrationCursor(ctx, userID, cursorID)
}
