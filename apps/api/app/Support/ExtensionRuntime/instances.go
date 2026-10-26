package extensionruntime

import (
	"errors"

	extensionprotocol "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionProtocol"
)

var (
	ErrRuntimeInstanceNotFound   = errors.New("extension runtime instance was not found")
	ErrRuntimeInstanceNotActive  = errors.New("extension runtime instance is not active")
	ErrRuntimeInstanceActive     = errors.New("extension runtime instance is active")
	ErrRuntimeInstanceBusy       = errors.New("extension runtime instance still has active calls")
	ErrRuntimeInstanceConflict   = errors.New("extension runtime instance already exists")
	ErrRuntimeInstanceNotDrained = errors.New("extension runtime instance must be drained before transition")
	ErrRuntimeTrustRevoked       = errors.New("extension executable trust was revoked")
)

// RuntimeInstanceSnapshot is the Host-owned handle for one exact process.
type RuntimeInstanceSnapshot struct {
	Identity         RuntimeInstanceIdentity
	ExtensionVersion string
	ArtifactDigest   string
	VersionID        int64
	Target           extensionprotocol.RouteTarget
	Active           bool
	Admission        RuntimeAdmissionSnapshot
}

// RuntimeInstanceArtifactIdentity binds an incident to immutable process
// identity so a stale event cannot quarantine a replacement.
type RuntimeInstanceArtifactIdentity struct {
	RuntimeInstanceIdentity
	ExtensionVersion string
	ArtifactDigest   string
}
