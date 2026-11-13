package forum

// BadgeRef 是帖子/评论旁展示的「当前展示徽章」引用（Forum 侧的读模型）。
// 它可能是用户手动佩戴的那枚，未必是排序里的最高级——chip 与资料页展示位同源。
//
// 它只有一份来源：读 SQL 的 topBadgeSelectSQL/topBadgeJoinSQL（见
// badge_ref_scan.go）在同一个 SELECT 里把徽章四列与等级外观三列一起带出。
// 曾有过第二条「Badges 包按用户批量补齐」的投影路径，生产从未装配，两套实现
// 让「论坛读模型有没有徽章外观」取决于走了哪条路——那条路径已删除。
//
// actor 无关：同一个作者的徽章对所有人一样，因此它可以随主题/评论进缓存，
// 不像 likedByMe / bookmarkedByMe 必须每请求重算。
type BadgeRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Tier string `json:"tier"`
	Icon string `json:"icon"`
	// TierName / TierColor 是等级表里的双语名与识别色。
	//
	// 与 Tier 一起下发而不是让前端按 key 映射：等级自 202611120001 起是运营可改的
	// 数据（可以新增档位、换颜色），前端硬编码映射在新增档位时会退化成「无颜色」。
	// 两者都与 locale 无关（双语并列 + 一个色值），所以随主题/评论进缓存是安全的。
	TierName  BadgeTierName `json:"tierName"`
	TierColor string        `json:"tierColor"`
}

// BadgeTierName 是等级的双语名。两个字段都下发：服务端不替调用方选语言，
// 否则同一个响应体对不同 locale 的读者就不再相同，缓存键必须带上 locale。
type BadgeTierName struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}
