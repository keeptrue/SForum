package bootstrap

import (
	"context"
	"strings"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

// productionNavigationRuntime 将 Manager exact-instance admission 适配到
// Navigation/Region Composer。声明式贡献（无 Handler）只需要 Available+Acquire；
// 带 Handler 的贡献在 Protocol V2 导航渲染接入前 fail closed，由 Composer 的
// optional replace fallback / selected replace fail-closed 语义处理。
type productionNavigationRuntime struct {
	source navigationRuntimeSource
}

type navigationRuntimeSource interface {
	RuntimeInstanceAvailable(extensionsruntime.RuntimeInstanceIdentity) bool
	AcquireRuntimeCall(
		context.Context,
		extensionsruntime.RuntimeInstanceIdentity,
		extensionsruntime.RuntimeCallClass,
	) (*extensionsruntime.RuntimeAdmissionLease, error)
	InspectRuntimeInstance(extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error)
}

func newProductionNavigationRuntime(source navigationRuntimeSource) *productionNavigationRuntime {
	if source == nil {
		return nil
	}
	return &productionNavigationRuntime{source: source}
}

func (r *productionNavigationRuntime) Available(artifact navigationregistry.Artifact) bool {
	if r == nil || r.source == nil || artifact.Core {
		return false
	}
	identity, ok := navigationRuntimeIdentity(artifact)
	if !ok {
		return false
	}
	if !r.source.RuntimeInstanceAvailable(identity) {
		return false
	}
	snapshot, err := r.source.InspectRuntimeInstance(identity)
	if err != nil {
		return false
	}
	// 精确制品：扩展版本、包摘要与 runtime instance 必须一致，防止升级窗口串包。
	return snapshot.Active &&
		snapshot.Identity.ExtensionID == artifact.ExtensionID &&
		snapshot.Identity.InstanceID == artifact.RuntimeInstanceID &&
		snapshot.ExtensionVersion == artifact.ExtensionVersion &&
		snapshot.ArtifactDigest == artifact.PackageDigest &&
		snapshot.VersionID == artifact.VersionID
}

func (r *productionNavigationRuntime) Acquire(
	ctx context.Context,
	artifact navigationregistry.Artifact,
) (navigationregistry.RuntimeLease, error) {
	if r == nil || r.source == nil || ctx == nil || artifact.Core {
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	identity, ok := navigationRuntimeIdentity(artifact)
	if !ok {
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	// 再次校验 exact artifact，避免 Available 与 Acquire 之间的升级窗口。
	if !r.Available(artifact) {
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	lease, err := r.source.AcquireRuntimeCall(ctx, identity, extensionsruntime.RuntimeCallProvider)
	if err != nil || lease == nil || lease.Context == nil {
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	if lease.Class != extensionsruntime.RuntimeCallProvider {
		lease.Release()
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	return &productionNavigationRuntimeLease{
		lease:     lease,
		runtimeID: artifact.RuntimeInstanceID,
	}, nil
}

// RenderNavigation：Manifest 当前无 Handler；Protocol V2 导航渲染未接入前 fail closed。
func (r *productionNavigationRuntime) RenderNavigation(
	context.Context,
	navigationregistry.RuntimeInvocation,
) (navigationregistry.RuntimeOutput, error) {
	return navigationregistry.RuntimeOutput{}, navigationregistry.ErrRuntimeUnavailable
}

// RenderRegion：与 RenderNavigation 同样 fail closed，直到可执行 region handler 落地。
func (r *productionNavigationRuntime) RenderRegion(
	context.Context,
	navigationregistry.RuntimeInvocation,
) (navigationregistry.RuntimeOutput, error) {
	return navigationregistry.RuntimeOutput{}, navigationregistry.ErrRuntimeUnavailable
}

type productionNavigationRuntimeLease struct {
	lease     *extensionsruntime.RuntimeAdmissionLease
	runtimeID string
}

func (l *productionNavigationRuntimeLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *productionNavigationRuntimeLease) RuntimeInstanceID() string {
	if l == nil {
		return ""
	}
	return l.runtimeID
}

func (l *productionNavigationRuntimeLease) Release() {
	if l != nil && l.lease != nil {
		l.lease.Release()
	}
}

func navigationRuntimeIdentity(artifact navigationregistry.Artifact) (extensionsruntime.RuntimeInstanceIdentity, bool) {
	extensionID := strings.TrimSpace(artifact.ExtensionID)
	instanceID := strings.TrimSpace(artifact.RuntimeInstanceID)
	if extensionID == "" || instanceID == "" ||
		strings.TrimSpace(artifact.ExtensionVersion) == "" ||
		strings.TrimSpace(artifact.PackageDigest) == "" ||
		artifact.VersionID <= 0 {
		return extensionsruntime.RuntimeInstanceIdentity{}, false
	}
	return extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: extensionID, InstanceID: instanceID,
	}, true
}
