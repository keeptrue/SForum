package extensionsruntime

import extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"

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

var (
	ErrRuntimeInstanceNotFound   = extensionruntime.ErrRuntimeInstanceNotFound
	ErrRuntimeInstanceNotActive  = extensionruntime.ErrRuntimeInstanceNotActive
	ErrRuntimeInstanceActive     = extensionruntime.ErrRuntimeInstanceActive
	ErrRuntimeInstanceBusy       = extensionruntime.ErrRuntimeInstanceBusy
	ErrRuntimeInstanceConflict   = extensionruntime.ErrRuntimeInstanceConflict
	ErrRuntimeInstanceNotDrained = extensionruntime.ErrRuntimeInstanceNotDrained
	ErrRuntimeTrustRevoked       = extensionruntime.ErrRuntimeTrustRevoked
)
