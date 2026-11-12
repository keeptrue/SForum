package badges

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Palette"
)

// 本文件是「徽章等级」的领域形状与校验：等级表（badge_tiers）的读模型、
// 写入口，以及徽章 tier 字段的唯一校验规则。
//
// 它不 import TrustLevels：色板已上提到 app/Support/Palette，两边共用同一份白名单，
// 但没有任何领域依赖。这条边必须保持单向——一旦 Badges import TrustLevels，
// 「徽章」就会因为「信任等级」的存在而无法单独拆包/替换。

// tierKeyPattern 与迁移 202611120001 的 badge_tiers_key_check **逐字一致**。
//
// 两处都写是刻意的：迁移是最后防线（绕过 Go 的写路径也拦得住），Go 侧是为了给出
// 可读的 422 而不是把 23514 约束违例翻译成 500。
var tierKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// TierDef 是等级表的读模型（含运营字段）。
type TierDef struct {
	Key      string
	NameZh   string
	NameEn   string
	Icon     string
	Color    string
	Position int
	Enabled  bool
	// BadgeCount 是引用该等级的徽章数。删除前必须知道它：>0 时删除需要 migrateTo。
	BadgeCount int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Appearance 把等级行投影成徽章侧的外观字段（Badge / BadgeRef / 公开目录共用）。
func (t TierDef) Appearance() TierAppearance {
	return TierAppearance{NameZh: t.NameZh, NameEn: t.NameEn, Color: t.Color, Position: t.Position}
}

// TierInput 是等级创建/更新的入参。
//
// Key 只在创建时有意义：等级 key 是外键目标与 URL 段，PATCH 不允许改（改 key 等于
// 换一个等级，历史徽章会指向一个「新等级」，语义上应删旧建新）。
type TierInput struct {
	Key      string
	NameZh   string
	NameEn   string
	Icon     string
	Color    string
	Position int
	Enabled  bool
}

// TierReader 是等级表的窄读口，供徽章规则校验注入。
//
// 为什么单独定义而不是直接吃 Store：validateBadgeInput 只需要「这一档存在吗、启用吗」，
// 让它依赖整个 Store 会让单测被迫实现二十个方法才能测一条规则。
type TierReader interface {
	ListTiers(ctx context.Context) ([]TierDef, error)
	GetTier(ctx context.Context, key string) (TierDef, error)
}

// TierWriter 是等级表的写口（admin 面）。
type TierWriter interface {
	CreateTier(ctx context.Context, input TierInput) (TierDef, error)
	UpdateTier(ctx context.Context, key string, input TierInput) (TierDef, error)
	// DeleteTier 在单事务里把该等级的徽章迁到 migrateTo，再删行。
	DeleteTier(ctx context.Context, key string, migrateTo string) error
	// ReorderTiers 按 keys 的顺序重写 position（未列出的等级保持相对顺序追加在后）。
	ReorderTiers(ctx context.Context, keys []string) error
}

// normalizeTierKey 归一化等级 key：trim + 小写。
//
// 只做这两件事，不做「把非法字符替换掉」——slug 那套替换在这里是有害的：
// 运营输入 `Gold Tier` 时静默变成 `gold-tier`（不匹配 pattern，还是报错），
// 而输入 `GOLD` 变成 `gold` 却可能撞上已有 key，错误信息会变成「key 已占用」。
// 明确报「key 形状不合法」比猜意图更有用。
func normalizeTierKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// TierPositionAppend 是「追加到末尾」的哨兵值，只在**创建**时有效。
//
// 为什么需要它：position 是「最高级徽章」排序的第一关键字，而 0 是**合法且最高**
// 的位置。若把「调用方没传 position」当成 0，新等级会与金牌并列最高档，挂在它上面
// 的徽章会和金牌徽章争同一个名次。所以「没传」必须与「显式传 0」区分开，用一个
// 负数哨兵表达，由 store 在插入时换成 max(position)+1。
const TierPositionAppend = -1

// validateTierInput 是等级入参校验的唯一入口（创建与更新共用）。
//
// validateKey 为 false 时（更新）跳过 key 形状校验：key 不可变，形状已在创建时验过。
func validateTierInput(input TierInput, validateKey bool) (TierInput, error) {
	input.NameZh = strings.TrimSpace(input.NameZh)
	input.NameEn = strings.TrimSpace(input.NameEn)
	input.Icon = strings.TrimSpace(input.Icon)
	input.Color = strings.TrimSpace(input.Color)
	if validateKey {
		input.Key = normalizeTierKey(input.Key)
		if !tierKeyPattern.MatchString(input.Key) {
			return TierInput{}, fmt.Errorf("%w: tier key must match %s", ErrBadgeTierInvalid, tierKeyPattern.String())
		}
	}
	// 双语名都必填：缺一门语言时前端只能回退到 key，而 key 是给机器看的
	// （`activity_limited` 出现在徽章角标上就是事故）。
	if input.NameZh == "" || input.NameEn == "" {
		return TierInput{}, fmt.Errorf("%w: tier nameZh and nameEn are required", ErrBadgeTierInvalid)
	}
	if input.Icon == "" {
		input.Icon = "i-lucide-medal"
	}
	if !strings.HasPrefix(input.Icon, "i-lucide-") && !strings.HasPrefix(input.Icon, "i-tabler-") {
		return TierInput{}, fmt.Errorf("%w: tier icon must use i-lucide- or i-tabler- prefix", ErrBadgeTierInvalid)
	}
	// 空串表示「这一档没有显式识别色」（列默认值就是空串），非空必须是
	// 9 个语义 token 或小写 #rrggbb——白名单来自 Support/Palette，与信任等级同源。
	if input.Color != "" && !palette.IsTierColor(input.Color) {
		return TierInput{}, fmt.Errorf("%w: unknown tier color %q", ErrBadgeTierInvalid, input.Color)
	}
	if input.Position < 0 && input.Position != TierPositionAppend {
		return TierInput{}, fmt.Errorf("%w: tier position must be >= 0", ErrBadgeTierInvalid)
	}
	return input, nil
}

// resolveBadgeTier 校验徽章要写入的等级：必须存在；切档时还必须是启用状态。
//
// currentTier 非 nil 表示更新：**保持原值一律允许**，即使该档已停用。
// 否则运营停用一档之后，任何一次「改徽章名字」都会因为旧值不合法而 422——
// 那是把「停用」变成了「冻结所有引用它的徽章」。
func resolveBadgeTier(ctx context.Context, tiers TierReader, tier BadgeTier, currentTier *BadgeTier) error {
	if tier == "" {
		return fmt.Errorf("%w: tier is required", ErrBadgeTierInvalid)
	}
	if currentTier != nil && *currentTier == tier {
		return nil
	}
	if tiers == nil {
		return fmt.Errorf("%w: tier catalog unavailable", ErrBadgeTierInvalid)
	}
	def, err := tiers.GetTier(ctx, string(tier))
	if err != nil {
		if errors.Is(err, ErrBadgeTierNotFound) {
			return fmt.Errorf("%w: unknown tier %q", ErrBadgeTierInvalid, tier)
		}
		return err
	}
	if !def.Enabled {
		return fmt.Errorf("%w: tier %q is disabled", ErrBadgeTierInvalid, tier)
	}
	return nil
}

// ListTiers 是公开面的等级目录读（无 actor）。
//
// 放在 Service 而不是 AdminService：公开端点没有 actor，也不该为了读一份运营配置
// 去构造一个「假的管理员」。过滤（只要启用档）由调用方决定——公开目录要启用档，
// 后台列表要全部，同一条 SQL 服务两种视图。
func (s *Service) ListTiers(ctx context.Context) ([]TierDef, error) {
	if s == nil || s.store == nil {
		return []TierDef{}, nil
	}
	return s.store.ListTiers(ctx)
}
