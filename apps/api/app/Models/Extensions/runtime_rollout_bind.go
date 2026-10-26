package extensions

import (
	"context"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	runtimerollout "github.com/keeptrue/sforum/apps/api/app/Support/RuntimeRollout"
)

// RuntimeRolloutCoordinator is the minimal RuntimeRollout surface used by upgrades.
type RuntimeRolloutCoordinator interface {
	CreatePlan(ctx context.Context, extensionID, sourceDigest, targetDigest, actor string, canaryPercent, retain int) (runtimerollout.Plan, error)
	MarkMigrationReady(ctx context.Context, planID, actor string) (runtimerollout.Plan, error)
	AckNode(ctx context.Context, planID, nodeID, phase, health string, canary bool) (runtimerollout.Plan, error)
	SelectCanary(ctx context.Context, planID, actor string) (runtimerollout.Plan, error)
	BeginDrain(ctx context.Context, planID, actor string) (runtimerollout.Plan, error)
	PromoteAtomic(ctx context.Context, planID, actor string) (runtimerollout.Plan, error)
	Rollback(ctx context.Context, planID, actor, reason string) (runtimerollout.Plan, error)
	Fail(ctx context.Context, planID, actor, message string) (runtimerollout.Plan, error)
	ActivePlan(ctx context.Context, extensionID string) (runtimerollout.Plan, bool, error)
}

// RuntimeRolloutBinding 是 runtimeRollout 的聚焦协作者。
//
// 为什么它不再是 Service 上的一个方法：`Service` 是历史公开契约的门面，它的
// receiver 方法数是非增长基线（当前上限 70）。每加一个「只写一个字段」的绑定器
// 就永久占掉一格，而这类迟到绑定本来就不该住在门面上——它不读 store、不校验
// 权限、不产生事件，只在一个已有锁下赋值。方法与字段名的语义完全不变，变的只是
// 谁持有它：这与仓库既有的 `TaxonomyService` / `EditableSourceService` /
// `CitationService`（`New*Service(service *Service)`）是同一种形状。
//
// 另外提醒后来者：本函数目前**没有任何调用方**（全仓搜索 `BindRuntimeRollout`
// 只有定义本身）。也就是说 `runtimeRollout` 在运行期恒为 nil，
// `DriveRuntimeRolloutForStagedUpgrade` 因此恒返回空 plan。这是本次抽取之前就
// 存在的事实，本次不改行为；接上 P12 生产绑定时应当调用
// `NewRuntimeRolloutBinding(service).BindRuntimeRollout(coord)`。
type RuntimeRolloutBinding struct {
	service *Service
}

// NewRuntimeRolloutBinding 构造绑定协作者。service 为 nil 时所有方法安全空转。
func NewRuntimeRolloutBinding(service *Service) *RuntimeRolloutBinding {
	return &RuntimeRolloutBinding{service: service}
}

// BindRuntimeRollout late-binds multi-node staged rollout after P12 bootstrap.
func (b *RuntimeRolloutBinding) BindRuntimeRollout(coord RuntimeRolloutCoordinator) *RuntimeRolloutBinding {
	if b == nil || b.service == nil {
		return b
	}
	b.service.assetPublicationMu.Lock()
	b.service.runtimeRollout = coord
	b.service.assetPublicationMu.Unlock()
	return b
}

// DriveRuntimeRolloutForStagedUpgrade creates/drives a rollout plan for staged upgrade.
// Migration failure marks the plan failed without promoting.
func (s *LifecycleService) DriveRuntimeRolloutForStagedUpgrade(
	ctx context.Context,
	actor identity.Actor,
	source, target Extension,
	migrationOK bool,
	migrationErr error,
) (runtimerollout.Plan, error) {
	if s == nil || s.runtimeRollout == nil {
		return runtimerollout.Plan{}, nil
	}
	sourceDigest := strings.ToLower(strings.TrimSpace(source.PackageDigest))
	targetDigest := strings.ToLower(strings.TrimSpace(target.PackageDigest))
	if sourceDigest == "" || targetDigest == "" || sourceDigest == targetDigest {
		return runtimerollout.Plan{}, nil
	}
	actorID := settingsActorID(actor)
	plan, err := s.runtimeRollout.CreatePlan(ctx, source.ID, sourceDigest, targetDigest, actorID, 10, 3)
	if err != nil {
		if err == runtimerollout.ErrConflict {
			// 复用已有活动 plan。
			existing, ok, getErr := s.runtimeRollout.ActivePlan(ctx, source.ID)
			if getErr != nil {
				return runtimerollout.Plan{}, getErr
			}
			if ok {
				plan = existing
			} else {
				return runtimerollout.Plan{}, err
			}
		} else {
			return runtimerollout.Plan{}, err
		}
	}
	if !migrationOK {
		msg := "migration failed"
		if migrationErr != nil {
			msg = migrationErr.Error()
		}
		return s.runtimeRollout.Fail(ctx, plan.PlanID, actorID, msg)
	}
	if _, err := s.runtimeRollout.MarkMigrationReady(ctx, plan.PlanID, actorID); err != nil {
		return plan, err
	}
	// Node health is an external, node-bound proof. A process-local upgrade
	// helper must never manufacture an AckNode or promote without one.
	return plan, fmt.Errorf("runtime rollout staged; waiting for external node health acknowledgements")
}
