package identity

import (
	"context"
	"fmt"
	"time"
)

// UserAccount 是信任等级判定需要的账号级事实（注册时间 + 账号类型）。
//
// 刻意不是 Actor：Actor 还带权限集合，而论坛读面只需要给一页作者算等级，
// 不该为此把整条权限装载链路跑一遍。
type UserAccount struct {
	CreatedAt time.Time
	Kind      UserKind
}

// UserAccounts 批量读用户的注册时间与类型（一条 SQL，禁止按 id 循环）。
//
// 缺失的 id 不出现在返回的 map 里（作者行已删），调用方据此跳过该作者。
func (s *PostgresStore) UserAccounts(ctx context.Context, userIDs []int64) (map[int64]UserAccount, error) {
	if s == nil || s.pool == nil {
		return map[int64]UserAccount{}, nil
	}
	ids := uniqueUserAccountIDs(userIDs)
	if len(ids) == 0 {
		return map[int64]UserAccount{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, created_at
		FROM users
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("load user accounts: %w", err)
	}
	defer rows.Close()
	accounts := make(map[int64]UserAccount, len(ids))
	for rows.Next() {
		var (
			id        int64
			kindRaw   string
			createdAt time.Time
		)
		if err := rows.Scan(&id, &kindRaw, &createdAt); err != nil {
			return nil, fmt.Errorf("scan user account: %w", err)
		}
		account := UserAccount{CreatedAt: createdAt, Kind: UserKindHuman}
		// 与 LoadEffectiveActor 同口径：未知/空 kind 一律按人类处理。
		if ValidUserKind(UserKind(kindRaw)) {
			account.Kind = UserKind(kindRaw)
		}
		accounts[id] = account
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user accounts: %w", err)
	}
	return accounts, nil
}

// uniqueUserAccountIDs 去重并剔除非正 id，保持输入顺序。
func uniqueUserAccountIDs(userIDs []int64) []int64 {
	seen := make(map[int64]bool, len(userIDs))
	out := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
