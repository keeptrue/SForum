package conditions

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// loadIdentity 处理五个身份类条件。它们各自是「一次查询」，按 type 分派。
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
	default:
		return nil, fmt.Errorf("conditions: 未知身份条件 %q", typ)
	}
}

// accountAgeSQL：账户年龄按**进程本地日**算（$2::date 由 Go 传，与 streak 同一来源）。
// 用 CURRENT_DATE 会在 API 与 PG 时区不一致时跨零点差一天；GREATEST(...,0) 挡住
// 「时钟回拨导致的负年龄」——负值会让 lte 条件意外成立。
const accountAgeSQL = `
SELECT GREATEST($2::date - users.created_at::date, 0)
FROM users WHERE users.id = $1
`

func (r *Resolver) loadAccountAge(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var days int64
	if err := r.pool.QueryRow(ctx, accountAgeSQL, r.userID, r.today()).Scan(&days); err != nil {
		return nil, fmt.Errorf("conditions: 查询 identity.account_age 失败：%w", err)
	}
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
