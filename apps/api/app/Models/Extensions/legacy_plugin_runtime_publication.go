package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

var ErrRuntimeContentPublicationUnavailable = errors.New("extensions: runtime content publication boundary is unavailable")
var ErrRuntimeEditorPublicationUnavailable = errors.New("extensions: runtime editor publication boundary is unavailable")

type RuntimeContentPublicationMutation interface {
	Rollback() error
}

// RuntimeContentPublicationBoundary keeps Models independent from the Host
// Content Registry. Plugins without Lifecycle V2 still publish and quarantine
// declarations against the exact process artifact during legacy lifecycle.
type RuntimeContentPublicationBoundary interface {
	PublishRuntimeContent(context.Context, Extension) (RuntimeContentPublicationMutation, error)
	QuarantineRuntimeContent(context.Context, Extension) (RuntimeContentPublicationMutation, error)
}

// RuntimeEditorPublicationMutation is an exact Host-owned Editor Registry
// mutation. Rollback is artifact-CAS guarded and cannot replace a newer graph.
type RuntimeEditorPublicationMutation interface {
	Rollback() error
}

// RuntimeEditorPublicationBoundary keeps legacy lifecycle paths independent
// from the process-local Editor Registry implementation.
type RuntimeEditorPublicationBoundary interface {
	PublishRuntimeEditor(context.Context, Extension) (RuntimeEditorPublicationMutation, error)
	QuarantineRuntimeEditor(context.Context, Extension) (RuntimeEditorPublicationMutation, error)
}

func WithRuntimeContentPublications(boundary RuntimeContentPublicationBoundary) ServiceOption {
	return func(s *Service) { s.contentPublications = boundary }
}

func WithRuntimeEditorPublications(boundary RuntimeEditorPublicationBoundary) ServiceOption {
	return func(s *Service) {
		if s != nil && s.lifecycle != nil {
			s.lifecycle.editorPublications = boundary
		}
	}
}

func hasRuntimeContentPublication(manifest Manifest) bool {
	return len(manifest.Content) > 0
}

func hasRuntimeEditorPublication(manifest Manifest) bool {
	return len(manifest.Editor) > 0
}

func rollbackRuntimeContentMutation(mutation RuntimeContentPublicationMutation, cause error) error {
	if mutation == nil {
		return cause
	}
	if err := mutation.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore content publication: %w", err))
	}
	return cause
}

func rollbackRuntimeEditorMutation(mutation RuntimeEditorPublicationMutation, cause error) error {
	if mutation == nil {
		return cause
	}
	if err := mutation.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("restore editor publication: %w", err))
	}
	return cause
}

// compensateLegacyEditorEnable unwinds declarations in reverse publication
// order before reusing the existing runtime/query/cache compensation paths.
func compensateLegacyEditorEnable(
	s *serviceCore,
	ctx context.Context,
	enabled Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	contentMutation RuntimeContentPublicationMutation,
	editorMutation RuntimeEditorPublicationMutation,
	actorUserID int64,
	cause error,
) error {
	cause = rollbackRuntimeEditorMutation(editorMutation, cause)
	cause = rollbackRuntimeContentMutation(contentMutation, cause)
	if cacheMutation != nil {
		return s.compensateLegacyCacheEnable(
			ctx, enabled, assetMutation, queryMutation, cacheMutation, actorUserID, cause,
		)
	}
	if queryMutation != nil {
		return s.compensateLegacyQueryEnable(ctx, enabled, assetMutation, queryMutation, actorUserID, cause)
	}
	return compensateLegacyContentEnable(s, ctx, enabled, assetMutation, nil, actorUserID, cause)
}

func publishLegacyRuntimeEditor(
	s *LifecycleService,
	ctx context.Context,
	enabled Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	contentMutation RuntimeContentPublicationMutation,
	actorUserID int64,
) (RuntimeEditorPublicationMutation, error) {
	if !hasRuntimeEditorPublication(enabled.Manifest) {
		return nil, nil
	}
	mutation, err := s.editorPublications.PublishRuntimeEditor(ctx, enabled)
	if err == nil && mutation != nil {
		return mutation, nil
	}
	if err == nil {
		err = ErrRuntimeEditorPublicationUnavailable
	}
	return nil, compensateLegacyEditorEnable(
		s.serviceCore, ctx, enabled, assetMutation, queryMutation, cacheMutation,
		contentMutation, mutation, actorUserID, err,
	)
}

func quarantineLegacyRuntimeEditor(
	s *LifecycleService,
	ctx context.Context,
	extension Extension,
	assetMutation exactAssetMutation,
	queryMutation RuntimeQueryPublicationMutation,
	cacheMutation RuntimeCachePublicationMutation,
	contentMutation RuntimeContentPublicationMutation,
	hasRuntimeCaches bool,
	hasRuntimeQuerySurfaces bool,
) (RuntimeEditorPublicationMutation, error) {
	if !hasRuntimeEditorPublication(extension.Manifest) {
		return nil, nil
	}
	mutation, err := s.editorPublications.QuarantineRuntimeEditor(ctx, extension)
	if err == nil && mutation != nil {
		return mutation, nil
	}
	if err == nil {
		err = ErrRuntimeEditorPublicationUnavailable
	}
	err = rollbackRuntimeEditorMutation(mutation, err)
	err = rollbackRuntimeContentMutation(contentMutation, err)
	if hasRuntimeCaches {
		err = s.compensateLegacyCacheDisable(assetMutation, queryMutation, cacheMutation, nil, err)
	} else if hasRuntimeQuerySurfaces {
		err = s.compensateLegacyQueryDisable(assetMutation, queryMutation, nil, err)
	} else if restoreErr := s.rollbackExactAssetMutation(assetMutation); restoreErr != nil {
		err = errors.Join(err, restoreErr)
	}
	return nil, err
}

func compensateLegacyContentEnable(
	s *serviceCore,
	ctx context.Context,
	enabled Extension,
	assetMutation exactAssetMutation,
	contentMutation RuntimeContentPublicationMutation,
	actorUserID int64,
	cause error,
) error {
	errs := []error{rollbackRuntimeContentMutation(contentMutation, cause)}
	if s.runtime != nil {
		if err := s.runtime.Stop(ctx, enabled); err != nil {
			errs = append(errs, fmt.Errorf("stop runtime: %w", err))
		}
	}
	if _, err := s.disableLegacyPluginState(ctx, enabled, actorUserID); err != nil {
		errs = append(errs, fmt.Errorf("disable extension: %w", err))
	}
	if err := s.rollbackExactAssetMutation(assetMutation); err != nil {
		errs = append(errs, fmt.Errorf("restore asset publication: %w", err))
	}
	return errors.Join(errs...)
}

func compensateLegacyContentDisable(
	s *serviceCore,
	assetMutation exactAssetMutation,
	contentMutation RuntimeContentPublicationMutation,
	cause error,
) error {
	err := s.rollbackExactAssetMutation(assetMutation)
	return rollbackRuntimeContentMutation(contentMutation, errors.Join(cause, err))
}

func disableLegacyContentPlugin(
	s *serviceCore,
	ctx context.Context,
	extension Extension,
	assetMutation exactAssetMutation,
	contentMutation RuntimeContentPublicationMutation,
	actorUserID int64,
) (Extension, error) {
	if contentMutation == nil {
		return Extension{}, ErrRuntimeContentPublicationUnavailable
	}
	if err := s.clearPluginProviderSelections(ctx, extension.ID, keepMailProviderSelection); err != nil {
		return Extension{}, compensateLegacyContentDisable(s, assetMutation, contentMutation, err)
	}
	disabled, err := s.disableLegacyPluginState(ctx, extension, actorUserID)
	if err != nil {
		return Extension{}, compensateLegacyContentDisable(s, assetMutation, contentMutation, err)
	}
	if s.pageRegistry != nil {
		s.pageRegistry.ClearExtension(extension.ID)
	}
	if s.runtime != nil {
		_ = s.runtime.Stop(ctx, extension)
		if extension.Status == StatusEnabled {
			s.runtime.EmitHook(ctx, appevents.ExtensionDisabled, map[string]any{
				"extensionId": extension.ID, "reason": "lifecycle_drain",
			})
		}
	}
	return disabled, nil
}

// LegacyPluginRuntimePublicationStore keeps the V1 Service call surface while
// allowing the production PostgreSQL store to commit mutable extension state
// and the immutable P12 desired full-set in one transaction. Test and
// third-party Store implementations that do not expose this additive boundary
// retain the pre-P12 behavior.
type LegacyPluginRuntimePublicationStore interface {
	EnableLegacyPluginRuntime(
		context.Context,
		Extension,
		int64,
	) (Extension, PluginRuntimePublication, error)
	DisableLegacyPluginRuntime(
		context.Context,
		Extension,
		int64,
	) (Extension, PluginRuntimePublication, error)
}

func (s *serviceCore) enableLegacyPluginState(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, error) {
	if publisher, ok := s.store.(LegacyPluginRuntimePublicationStore); ok {
		enabled, _, err := publisher.EnableLegacyPluginRuntime(ctx, target, actorUserID)
		return enabled, err
	}
	return s.store.Enable(ctx, target.ID, target.Type)
}

func (s *serviceCore) disableLegacyPluginState(
	ctx context.Context,
	target Extension,
	actorUserID int64,
) (Extension, error) {
	if publisher, ok := s.store.(LegacyPluginRuntimePublicationStore); ok {
		disabled, _, err := publisher.DisableLegacyPluginRuntime(ctx, target, actorUserID)
		return disabled, err
	}
	return s.store.Disable(ctx, target.ID)
}

// componentPackageRuntimePublisher 是组件包运行时的窄接口：由 Support 层的
// ComponentRegistry 实现（含包级实例 id 派生），Models 层只按此接口调用，
// 避免把实例 id 的推导复制成第二处真相。
type componentPackageRuntimePublisher interface {
	PublishComponentPackageRuntime(extension Extension) error
	RemoveComponentPackageRuntime(extensionID string) (bool, error)
}

// publishLegacyComponentRuntime 让 legacy 启用路径收敛到与 Host 启动一致的状态。
//
// 只有公开 L2 组件、没有可执行后端的包不会走 Lifecycle V2（usesLifecycleV2 要求
// Backend.ProtocolVersion==2 且声明 lifecycle），而组件注册表的 reconcile 只存在于
// V2 发布事务里；结果是这样启用后的包要等到下一次 Host 启动（RestoreRuntimes）才
// 能被公开前端准入。这里在启用成功后补齐同一步骤。
//
// 失败不阻断启用：本调用发生在启用已提交之后，最坏情况是退回「下次启动才生效」的
// 既有行为，而不是把非致命缺口升级成 enable 失败。
func publishLegacyComponentRuntime(ctx context.Context, s *serviceCore, extension Extension) {
	if s == nil || len(extension.Manifest.Components) == 0 || extension.Status != StatusEnabled {
		return
	}
	publisher, ok := s.componentRegistry.(componentPackageRuntimePublisher)
	if !ok || publisher == nil {
		return
	}
	if err := publisher.PublishComponentPackageRuntime(extension); err != nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: extension.ID,
			Action:      EventEnabled,
			Message:     "component package runtime publication deferred to next host start: " + err.Error(),
		})
	}
}

// removeLegacyComponentRuntime 是禁用路径的对称收敛：移除组件包运行时注册。
// 未注册与失败都不阻断禁用（下次 Host 启动同样收敛）。
func removeLegacyComponentRuntime(ctx context.Context, s *serviceCore, extensionID string) {
	if s == nil || strings.TrimSpace(extensionID) == "" {
		return
	}
	publisher, ok := s.componentRegistry.(componentPackageRuntimePublisher)
	if !ok || publisher == nil {
		return
	}
	if _, err := publisher.RemoveComponentPackageRuntime(extensionID); err != nil {
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: extensionID,
			Action:      EventDisabled,
			Message:     "component package runtime removal deferred to next host start: " + err.Error(),
		})
	}
}

// PluginSessionStateInvalidator 是宿主「插件命名空间会话状态」存储的最窄形状。
//
// 声明在消费方（而不是导入存储包）的理由与 AuthSession/Identity 侧一致：Models 不该
// 依赖一个具体存储与它的 Redis 客户端。语义上这是**擦除**钩子，不是访问控制——
// 被禁用扩展的状态本来就不可达，这里是为了不让字节残留。
type PluginSessionStateInvalidator interface {
	InvalidateExtension(ctx context.Context, extensionID string) (int, error)
}

// erasePluginSessionState 在扩展被禁用/卸载之后尽力擦除它的会话状态。
//
// 与 publishLegacyComponentRuntime 同一形态的自由函数而不是 serviceCore 方法：
// app/Models/Extensions 的接收者方法数与扁平目录都在上限上，新增方法会撞架构门禁。
// 刻意不返回错误：调用发生在生命周期提交之后，清理失败不能把禁用/卸载变成用户可见故障。
func erasePluginSessionState(ctx context.Context, s *serviceCore, extensionID string) {
	if s == nil || s.pluginSessionState == nil {
		return
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = s.pluginSessionState.InvalidateExtension(ctx, extensionID)
	}()
}

// WithPluginSessionStateInvalidator 注入插件命名空间会话状态擦除钩子。
//
// 为什么扩展生命周期需要它：禁用/卸载一个扩展之后，它的会话状态不得继续残留
// （否则重新启用或换上同 id 的新版本会读到上一份状态的字节）。与组件运行时收敛
// 一样，这是**提交之后**的 best-effort 副作用：失败只能退化为「下次启动」而不是让
// 禁用/卸载失败。
func WithPluginSessionStateInvalidator(invalidator PluginSessionStateInvalidator) ServiceOption {
	return func(s *Service) {
		s.pluginSessionState = invalidator
	}
}
