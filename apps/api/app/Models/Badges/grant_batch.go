package badges

import (
	"context"
	"errors"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件是批量授予：一次给一个徽章授予多个用户。
//
// 为什么需要它：手动徽章（活动限定、贡献者、内测）在现实里是「一批人一起给」，
// 逐个点 50 次不只是慢——中途失败时运营不知道停在哪一个，也没法复述做过什么。
//
// 三条设计约束：
//   - **部分成功是正常结果**，不是错误。已持有的人跳过、单个人失败记录原因，
//     整批返回 200 + 三个计数，让运营看到「51 人里 48 成功 2 已持有 1 失败」。
//   - 上限 50：请求体与逐人 SQL 都是线性成本，一次几百人的批量应该走规则而不是手点。
//   - 只接受手动徽章：自动徽章的授予由规则评估决定，手动塞进去会在下一次评估前
//     表现为「已授予但规则不满足」的假状态（与 Grant 同一口径）。

// MaxBatchGrantUsers 是单次批量授予的用户数上限。
const MaxBatchGrantUsers = 50

// BatchGrantInput 是批量授予入参。
type BatchGrantInput struct {
	BadgeID   int64
	UserIDs   []int64
	Reason    string
	GrantedBy int64
}

// BatchGrantFailure 是单个用户的失败原因。
//
// Reason 是**稳定错误码**（与 HTTP 错误码同族），不是 err.Error()：
// 领域错误串（"badges: not found"）是给日志看的，把它下发到 UI 只会让前端无法翻译。
type BatchGrantFailure struct {
	UserID   int64
	Username string
	Reason   string
}

// BatchGrantResult 是批量授予的三个计数。
type BatchGrantResult struct {
	Granted int
	Skipped int
	Failed  []BatchGrantFailure
}

// BatchGrant 逐人授予并汇总结果。
//
// 逐人而不是一条 INSERT：每个用户都要区分「新授予 / 已持有 / 失败」，一条
// INSERT ... SELECT 只能给出总行数，运营就无法知道谁没成功。50 人上限正是为了让
// 这个「逐个可解释」的选择在成本上站得住。
func (a *AdminService) BatchGrant(ctx context.Context, actor identity.Actor, input BatchGrantInput) (BatchGrantResult, error) {
	if err := requireBadgePermission(actor, PermissionBadgeManage); err != nil {
		return BatchGrantResult{}, err
	}
	if a == nil || a.store == nil {
		return BatchGrantResult{}, ErrBadgesDisabled
	}
	userIDs := normalizeBatchUserIDs(input.UserIDs)
	if len(userIDs) == 0 {
		return BatchGrantResult{}, ErrBadgeGrantBatchInvalid
	}
	if len(userIDs) > MaxBatchGrantUsers {
		return BatchGrantResult{}, ErrBadgeGrantBatchInvalid
	}
	badge, err := a.store.GetBadge(ctx, input.BadgeID)
	if err != nil {
		return BatchGrantResult{}, err
	}
	if badge.GrantMode != GrantManual {
		return BatchGrantResult{}, ErrBadgeManualOnly
	}

	result := BatchGrantResult{Failed: make([]BatchGrantFailure, 0)}
	grantedUsers := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		_, err := a.store.GrantManual(ctx, GrantInput{
			UserID:      userID,
			BadgeID:     input.BadgeID,
			GrantedBy:   input.GrantedBy,
			GrantReason: strings.TrimSpace(input.Reason),
		})
		switch {
		case err == nil:
			result.Granted++
			grantedUsers = append(grantedUsers, userID)
		case errors.Is(err, ErrBadgeAlreadyGranted):
			// 已持有不是失败：批量重跑同一批人是常规操作（补上后来新增的人）。
			result.Skipped++
		default:
			result.Failed = append(result.Failed, BatchGrantFailure{UserID: userID, Reason: failureReason(err)})
		}
	}

	// 只重算真正拿到新徽章的人：跳过的人 top_badge_id 不会因为这次调用变化。
	for _, userID := range grantedUsers {
		if err := a.store.RecomputeTopBadge(ctx, userID); err != nil {
			return BatchGrantResult{}, err
		}
	}
	if len(result.Failed) > 0 {
		result.Failed = a.fillFailureUsernames(ctx, result.Failed)
	}
	return result, nil
}

// fillFailureUsernames 用一次查询补上失败行的用户名。
//
// 失败时用户名是给人看的（"用户 42" 对运营毫无意义），但为了一条失败去查一次
// Identity 域是不划算的；UsernamesByIDs 一次覆盖全部失败行，且只读 users 表。
// 查不到就留空串——不能因为「补不上名字」把整批结果变成 500。
func (a *AdminService) fillFailureUsernames(ctx context.Context, failures []BatchGrantFailure) []BatchGrantFailure {
	ids := make([]int64, 0, len(failures))
	for _, failure := range failures {
		ids = append(ids, failure.UserID)
	}
	names, err := a.store.UsernamesByIDs(ctx, ids)
	if err != nil {
		return failures
	}
	for i := range failures {
		if name, ok := names[failures[i].UserID]; ok {
			failures[i].Username = name
		}
	}
	return failures
}

// normalizeBatchUserIDs 去重并丢掉非法 id（<=0），保留首次出现顺序。
//
// 去重是必须的：前端多选控件在「全选/反选」后经常重复提交同一个 id，不去重就会
// 把同一个人算成「1 次授予 + 1 次跳过」，计数看起来像出了 bug。
func normalizeBatchUserIDs(raw []int64) []int64 {
	seen := make(map[int64]struct{}, len(raw))
	out := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// failureReason 把错误翻成可下发的稳定码；未知错误统一 internal_error。
func failureReason(err error) string {
	if code := ErrorCode(err); code != "" {
		return code
	}
	return "internal_error"
}
