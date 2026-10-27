package attachments

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func (s *Service) Cleanup(ctx context.Context, actor identity.Actor, limit int) (CleanupResult, error) {
	if !actor.Can(identity.PermissionAttachmentManage) {
		return CleanupResult{}, identity.ErrPermissionDenied
	}
	return s.cleanupOrphans(ctx, limit)
}

func (s *Service) CleanupOrphanAttachments(ctx context.Context, limit int) error {
	_, err := s.cleanupOrphans(ctx, limit)
	return err
}

func (s *Service) cleanupOrphans(ctx context.Context, limit int) (CleanupResult, error) {
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return CleanupResult{}, err
	}
	cutoff := time.Now().AddDate(0, 0, -settings.CleanupOrphanAfterDays)
	items, err := s.store.ListCleanupCandidates(ctx, cutoff, limit)
	if err != nil {
		return CleanupResult{}, err
	}
	result := CleanupResult{RetentionDays: settings.CleanupOrphanAfterDays}
	// 宽限期内未到期的孤儿候选不进候选列表，但要如实反馈条数：否则「删除 0 条」
	// 会被读成「没有孤儿」。统计失败不阻断清理，Skipped 保持 0。
	if deferred, ok := s.store.(interface {
		CountDeferredCleanupCandidates(context.Context, time.Time) (int, error)
	}); ok {
		if count, countErr := deferred.CountDeferredCleanupCandidates(ctx, cutoff); countErr == nil {
			result.Skipped = count
		}
	}
	for _, item := range items {
		adapter, err := s.adapterForSettings(ctx, settings, item.Provider)
		if err != nil {
			result.Failed++
			continue
		}
		if variantStore, ok := s.store.(interface {
			ListAttachmentVariants(context.Context, int64) ([]AttachmentVariant, error)
		}); ok {
			variants, listErr := variantStore.ListAttachmentVariants(ctx, item.ID)
			if listErr != nil {
				result.Failed++
				continue
			}
			variantDeleteFailed := false
			for _, variant := range variants {
				variantAdapter := adapter
				if variant.Provider != item.Provider {
					variantAdapter, err = s.adapterForSettings(ctx, settings, variant.Provider)
				}
				if err != nil || variantAdapter.Delete(ctx, variant.ObjectKey) != nil {
					variantDeleteFailed = true
					break
				}
			}
			if variantDeleteFailed {
				result.Failed++
				continue
			}
		}
		if err := adapter.Delete(ctx, item.ObjectKey); err != nil {
			result.Failed++
			continue
		}
		if err := s.store.DeleteMetadata(ctx, item.ID); err != nil {
			result.Failed++
			continue
		}
		result.Deleted++
	}
	return result, nil
}
