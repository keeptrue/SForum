package extensionsruntime

import (
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

var (
	ErrRuntimeAdmissionInvalid     = extensionruntime.ErrRuntimeAdmissionInvalid
	ErrRuntimeAdmissionDraining    = extensionruntime.ErrRuntimeAdmissionDraining
	ErrRuntimeAdmissionQuarantined = extensionruntime.ErrRuntimeAdmissionQuarantined
	ErrRuntimeAdmissionForced      = extensionruntime.ErrRuntimeAdmissionForced
	ErrRuntimeAdmissionBusy        = extensionruntime.ErrRuntimeAdmissionBusy
)

type RuntimeCallClass = extensionruntime.RuntimeCallClass

const (
	RuntimeCallRoute            = extensionruntime.RuntimeCallRoute
	RuntimeCallGuard            = extensionruntime.RuntimeCallGuard
	RuntimeCallPage             = extensionruntime.RuntimeCallPage
	RuntimeCallHook             = extensionruntime.RuntimeCallHook
	RuntimeCallProvider         = extensionruntime.RuntimeCallProvider
	RuntimeCallService          = extensionruntime.RuntimeCallService
	RuntimeCallHost             = extensionruntime.RuntimeCallHost
	RuntimeCallJob              = extensionruntime.RuntimeCallJob
	RuntimeCallSchedule         = extensionruntime.RuntimeCallSchedule
	RuntimeCallCommand          = extensionruntime.RuntimeCallCommand
	RuntimeCallAdminSurface     = extensionruntime.RuntimeCallAdminSurface
	RuntimeCallLifecycleCleanup = extensionruntime.RuntimeCallLifecycleCleanup
)

type RuntimeInstanceIdentity = extensionruntime.RuntimeInstanceIdentity
type RuntimeAdmissionSnapshot = extensionruntime.RuntimeAdmissionSnapshot
type RuntimeAdmissionGate = extensionruntime.RuntimeAdmissionGate
type RuntimeAdmissionLease = extensionruntime.RuntimeAdmissionLease
type RuntimeInstanceSnapshot = extensionruntime.RuntimeInstanceSnapshot
type RuntimeInstanceArtifactIdentity = extensionruntime.RuntimeInstanceArtifactIdentity

var NewRuntimeAdmissionGate = extensionruntime.NewRuntimeAdmissionGate

// 制品完整性失败的类型化出口（issue #253）。构造入口只有 artifactintegrity 一处，
// 这两个函数是给 protocol.go / manager.go 用的薄适配：这两个错误点已达 legacy
// 行数上限，只能把一行裸 fmt.Errorf 换成一行同包调用，新增 import 就过不了架构门。
var ErrArtifactIncomplete = artifactintegrity.ErrArtifactUnusable

// NewArtifactEntryUnavailable 报告 manifest 声明的 backend 入口在制品里不可用。
func NewArtifactEntryUnavailable(extension extensions.Extension, entry string, cause error) error {
	return artifactintegrity.EntryUnavailable(extension.ID, extension.Version, extension.PackageDigest, entry, cause)
}

// NewArtifactEntryPathRejected 报告入口路径被解析拒绝（越界或非法相对路径）。
// ErrInvalidManifest 作为额外 cause 保留，既有调用方的 errors.Is 判断不受影响。
func NewArtifactEntryPathRejected(extension extensions.Extension, entry string) error {
	return artifactintegrity.EntryPathRejected(extension.ID, extension.Version, extension.PackageDigest,
		entry, extensions.ErrInvalidManifest)
}

var (
	ErrRuntimeInstanceNotFound   = extensionruntime.ErrRuntimeInstanceNotFound
	ErrRuntimeInstanceNotActive  = extensionruntime.ErrRuntimeInstanceNotActive
	ErrRuntimeInstanceActive     = extensionruntime.ErrRuntimeInstanceActive
	ErrRuntimeInstanceBusy       = extensionruntime.ErrRuntimeInstanceBusy
	ErrRuntimeInstanceConflict   = extensionruntime.ErrRuntimeInstanceConflict
	ErrRuntimeInstanceNotDrained = extensionruntime.ErrRuntimeInstanceNotDrained
	ErrRuntimeTrustRevoked       = extensionruntime.ErrRuntimeTrustRevoked
)
