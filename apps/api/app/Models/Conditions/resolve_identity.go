package conditions

import (
	"context"
	"fmt"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// loadIdentity 处理六个身份类条件。它们各自是「一次查询」，按 type 分派。
func (r *Resolver) loadIdentity(ctx context.Context, typ string, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	switch typ {
	case KeyIdentityAccountAge:
		return r.loadAccountAge(ctx, leaves)
	case KeyIdentityEmailVerify:
		return r.loadEmailVerified(ctx, leaves)
	case KeyIdentityOAuthLink:
		return r.loadOAuthLink(ctx, leaves)
	case KeyIdentityRole:
		return r.loadRole(ctx, leaves)
	case KeyIdentityAvatar:
		return r.loadAvatar(ctx, leaves)
	case KeyIdentityProfile:
		return r.loadProfile(ctx, leaves)
	default:
		return nil, fmt.Errorf("conditions: 未知身份条件 %q", typ)
	}
}

// accountAgeSQL 只取注册时刻，**日期折算全部在 Go 侧**完成。
//
// 旧写法是 `GREATEST($2::date - users.created_at::date, 0)`：左操作数是 Go 的进程
// 本地日，右操作数是 created_at 在 **PG 会话时区**里的日历日。两个日界不同源，于是
// 会话时区与进程时区不一致时，凡注册本地时间落在偏移窗口内的用户会**永久**多算
// 1 天（2026-11-13 实测：进程 CST、会话 UTC，CST 日历差 10 天被算成 11）。
// 现在把原始时刻取回，用 resolver 的同一个 clock/location 折算（见 calendarDaysBetween），
// 日界只剩一个来源；查询形状不变（仍是每 type 一次、按主键取一行）。
const accountAgeSQL = `SELECT users.created_at FROM users WHERE users.id = $1`

func (r *Resolver) loadAccountAge(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var createdAt time.Time
	if err := r.pool.QueryRow(ctx, accountAgeSQL, r.userID).Scan(&createdAt); err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.account_age 失败：%w", err)
	}
	days := calendarDaysBetween(createdAt, r.nowTime())
	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(days)
	}
	return out, nil
}

// calendarDaysBetween 返回 from → to 在 to 所在时区日历下的整天数；负数夹到 0。
//
// 为什么不用 `to.Sub(from).Hours()/24`：夏令时切换日只有 23 或 25 小时，按小时折算
// 会把「差 1 个日历日」算成 0 或 1.x。做法是把两个时刻各自折算成**该时区里的日历日**，
// 再映射到 UTC 午夜相减——UTC 没有夏令时，除法因此是精确的。
//
// 负数夹到 0 是既有语义（原 SQL 的 GREATEST(...,0)）：时钟回拨或 created_at 在
// 「今天」之后时，负年龄会让 `lte` 条件意外成立。
func calendarDaysBetween(from, to time.Time) int64 {
	location := to.Location()
	fromDate := dateAtUTCMidnight(from.In(location))
	toDate := dateAtUTCMidnight(to)
	days := int64(toDate.Sub(fromDate) / (24 * time.Hour))
	if days < 0 {
		return 0
	}
	return days
}

func dateAtUTCMidnight(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

const emailVerifiedSQL = `SELECT users.email_verified_at IS NOT NULL FROM users WHERE users.id = $1`

func (r *Resolver) loadEmailVerified(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var verified bool
	if err := r.pool.QueryRow(ctx, emailVerifiedSQL, r.userID).Scan(&verified); err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.email_verified 失败：%w", err)
	}
	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(verified)
	}
	return out, nil
}

// oauthLinkSQL：只认 `status='active'` 的绑定。
//
// 解绑/失效的历史行会留在表里（审计需要），把 `status<>'active'` 也算「已绑定」
// 会让「绑定过又解绑」的用户永久满足条件。
const oauthLinkSQL = `
SELECT provider_id,
       EXISTS (
         SELECT 1 FROM identity_external_links AS links
          WHERE links.user_id = $1 AND links.provider_id = candidates.provider_id
            AND links.status = 'active')
FROM unnest($2::text[]) AS candidates(provider_id)
`

func (r *Resolver) loadOAuthLink(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	providers := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		providers = append(providers, stringParam(leaf, "providerId"))
	}
	rows, err := r.pool.Query(ctx, oauthLinkSQL, r.userID, providers)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.oauth_link 失败：%w", err)
	}
	defer rows.Close()

	linked := make(map[string]bool, len(providers))
	for rows.Next() {
		var provider string
		var hit bool
		if err := rows.Scan(&provider, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 identity.oauth_link 结果失败：%w", err)
		}
		linked[provider] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 identity.oauth_link 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		provider := stringParam(leaf, "providerId")
		hit, ok := linked[provider]
		if !ok {
			return nil, fmt.Errorf("conditions: identity.oauth_link 缺少 %q 的结果", provider)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}

// roleSQL：用 `roles.key`（不是 alias），且只认启用中的角色。
//
// 停用角色不再授予任何权限，把它算成「拥有」会让规则与实际能力脱节；授予行本身
// 保留（历史），判定时按 `is_enabled` 收敛。
const roleSQL = `
SELECT role_key,
       EXISTS (
         SELECT 1 FROM user_roles AS ur
           JOIN roles AS r ON r.id = ur.role_id
          WHERE ur.user_id = $1 AND r.key = candidates.role_key AND r.is_enabled)
FROM unnest($2::text[]) AS candidates(role_key)
`

func (r *Resolver) loadRole(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	keys := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		keys = append(keys, stringParam(leaf, "roleKey"))
	}
	rows, err := r.pool.Query(ctx, roleSQL, r.userID, keys)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.role 失败：%w", err)
	}
	defer rows.Close()

	held := make(map[string]bool, len(keys))
	for rows.Next() {
		var key string
		var hit bool
		if err := rows.Scan(&key, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 identity.role 结果失败：%w", err)
		}
		held[key] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 identity.role 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		key := stringParam(leaf, "roleKey")
		hit, ok := held[key]
		if !ok {
			return nil, fmt.Errorf("conditions: identity.role 缺少 %q 的结果", key)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}

// avatarSQL：头像存的是附件 id，不是 URL；NULL 表示从未上传。
const avatarSQL = `
SELECT EXISTS (
  SELECT 1 FROM user_profiles AS profiles
   WHERE profiles.user_id = $1 AND profiles.avatar_attachment_id IS NOT NULL)
`

func (r *Resolver) loadAvatar(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var hasAvatar bool
	if err := r.pool.QueryRow(ctx, avatarSQL, r.userID).Scan(&hasAvatar); err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.avatar 失败：%w", err)
	}
	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hasAvatar)
	}
	return out, nil
}

// profileFilledSQL：资料行的四个文本列都是 TEXT NOT NULL DEFAULT 空串（迁移
// 202607070004），所以判据只能是**非空串**——`IS NOT NULL` 对它们恒真，等于每个
// 用户都满足；也不加 `trim`（文案是「填了就算，不看内容质量」）。
//
// 这里的 NULL 有两个来源，必须显式区分，这是本条件最容易写错的地方：
//   - **没有资料行**：行是「按需 upsert」的，注册期不插行（同一迁移的说明），所以
//     「无行」是常态而不是故障。子查询无行 → NULL → coalesce 到 FALSE，即「确定不
//     满足」。若让 ErrNoRows 冒出去，整条规则会变成 unknown，对大多数用户永久失效；
//   - **field 不在白名单**（脏规则）：在 Go 侧查询前就返回 error（引擎转 unknown）。
//     **不靠扫描 NULL 猜**：把未知取值当 FALSE 会在 `not` 子树下反转成 true，那是
//     fail-open，比条件不成立更贵。
//
// 形状与 oauthLinkSQL / roleSQL 一致（unnest + 每行一个 bool）：同一个 type 的多个
// 叶子只查一次，`Prepare` 的「查询数 = type 数」口径不变。
const profileFilledSQL = `
SELECT candidates.field,
       coalesce((
         SELECT CASE candidates.field
                  WHEN 'bio'       THEN profiles.bio         <> ''
                  WHEN 'signature' THEN profiles.signature   <> ''
                  WHEN 'website'   THEN profiles.website_url <> ''
                  WHEN 'location'  THEN profiles.location    <> ''
                END
           FROM user_profiles AS profiles
          WHERE profiles.user_id = $1), FALSE)
FROM unnest($2::text[]) AS candidates(field)
`

func (r *Resolver) loadProfile(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	fields := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		field := stringParam(leaf, "field")
		if !isProfileField(field) {
			// 脏规则：未知取值无法判定 → error（引擎转 unknown）。见 profileFilledSQL
			// 的注释：静默 false 会在 `not` 子树下反转成 true。
			return nil, fmt.Errorf("conditions: identity.profile 的 field 取值未知：%q", field)
		}
		fields = append(fields, field)
	}
	rows, err := r.pool.Query(ctx, profileFilledSQL, r.userID, fields)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.profile 失败：%w", err)
	}
	defer rows.Close()

	filled := make(map[string]bool, len(fields))
	for rows.Next() {
		var field string
		var hit bool
		if err := rows.Scan(&field, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 identity.profile 结果失败：%w", err)
		}
		filled[field] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 identity.profile 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		field := stringParam(leaf, "field")
		hit, ok := filled[field]
		if !ok {
			return nil, fmt.Errorf("conditions: identity.profile 缺少 %q 的结果", field)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}

// isProfileField 判断 `field` 是否在枚举白名单里（白名单与注册项的 Options 同源）。
func isProfileField(field string) bool {
	for _, candidate := range profileFieldOptions() {
		if candidate == field {
			return true
		}
	}
	return false
}
