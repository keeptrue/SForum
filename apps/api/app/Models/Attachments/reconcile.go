package attachments

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// ErrReferenceReconcileUnsupported 表示当前 store 不支持引用计数对账
// （只有 PostgreSQL 实现提供，测试用假 store 会走到这里）。
var ErrReferenceReconcileUnsupported = errors.New("attachments: store does not support reference count reconciliation")

// RecountResult 是一次引用计数对账的结果。
// Lowered/Raised 是漂移行数：reference_count 高于/低于实际引用行数；
// Unchanged 是计数本就一致的行数；Samples 是漂移行的附件 id（最多 20 条）。
type RecountResult struct {
	Lowered   int     `json:"lowered"`
	Raised    int     `json:"raised"`
	Unchanged int     `json:"unchanged"`
	Applied   bool    `json:"applied"`
	Samples   []int64 `json:"samples"`
}

// ReferenceCountReconciler 由 PostgresStore 实现；Store 接口刻意不含它，
// 避免把维护性能力推给每个实现（含测试假 store）。
type ReferenceCountReconciler interface {
	ReconcileReferenceCounts(ctx context.Context, dryRun bool) (RecountResult, error)
}

// ReconcileReferenceCounts 按 attachment_references 实际行数校正 attachments.reference_count。
// 历史漂移会让附件「计数大于 0 但没有引用行」而永远豁免孤儿清理，因此这里只提供
// 运营者显式触发的对账，绝不放进 cleanupOrphans 自动执行：历史内容可能存在
// 「有计数无引用行」的活引用，静默重算会在保留期后把它们物理删除。
func (s *Service) ReconcileReferenceCounts(ctx context.Context, actor identity.Actor, dryRun bool) (RecountResult, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return RecountResult{}, identity.ErrPermissionDenied
	}
	reconciler, ok := s.store.(ReferenceCountReconciler)
	if !ok {
		return RecountResult{}, ErrReferenceReconcileUnsupported
	}
	return reconciler.ReconcileReferenceCounts(ctx, dryRun)
}
