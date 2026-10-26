package hostapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	semver "github.com/Masterminds/semver/v3"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const (
	ServiceActionAdd     = "add"
	ServiceActionBefore  = "before"
	ServiceActionAfter   = "after"
	ServiceActionWrap    = "wrap"
	ServiceActionReplace = "replace"
)

var (
	ErrInvalidServiceRegistration = errors.New("invalid service registration")
	ErrInvalidServiceConstraint   = errors.New("invalid service version constraint")
	ErrServiceNotFound            = errors.New("service not found")
)

// ServiceProvider invokes one unary plugin service. ErrorDetail represents a
// remote application failure, while error is reserved for broker/transport
// failures that the Host should expose as unavailable.
type ServiceProvider interface {
	Invoke(context.Context, *protocolv2.RequestContext, string, string, string, *protocolv2.TypedDocument) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error)
}

// ServiceBidiStream is the transport-neutral stream after the Host has consumed
// and validated the opening frame. Context cancellation must interrupt Recv.
type ServiceBidiStream interface {
	Context() context.Context
	Send(*protocolv2.TypedDocument) error
	Recv() (*protocolv2.TypedDocument, error)
	CloseSend() error
}

// ServiceStreamingProvider forwards a validated bidirectional service stream.
// As with Invoke, ErrorDetail is a remote failure and error is a transport
// failure. CloseSend preserves the caller's half-close across the broker.
type ServiceStreamingProvider interface {
	Stream(context.Context, *protocolv2.RequestContext, string, string, string, ServiceBidiStream) (*protocolv2.ErrorDetail, error)
}

// ServiceRegistration joins one handshake descriptor with its exact runtime
// instance and Manifest composition metadata.
type ServiceRegistration struct {
	ExtensionID string
	InstanceID  string
	Action      string
	TargetID    string
	Priority    int
	Descriptor  *protocolv2.ServiceDescriptor
	Provider    ServiceProvider
}

// ServiceCaller carries the broker-attested exact identity and authority used
// by both dependency admission and provider authority checks.
type ServiceCaller struct {
	ExtensionID      string
	ExtensionVersion string
	ArtifactDigest   string
	TrustGrantID     string
	RuntimeEpoch     uint64
	InstanceID       string
	GrantedAuthority []string
	Attested         bool
}

// ServiceAuthorityDecision is deterministic and safe to include in audit data.
type ServiceAuthorityDecision struct {
	Allowed  bool
	Required []string
	Granted  []string
	Missing  []string
}

// ResolvedService is one effective version plus all same-version candidates.
// Candidate zero is always the deterministic winner.
type ResolvedService struct {
	ServiceID  string
	Revision   uint64
	Winner     ServiceRegistration
	Candidates []ServiceRegistration
	snapshot   *serviceRegistrySnapshot
}

func (s ResolvedService) HasConflict() bool {
	return len(s.Candidates) > 1
}

func (s ResolvedService) Authorize(caller ServiceCaller) ServiceAuthorityDecision {
	if s.Winner.Descriptor == nil {
		return ServiceAuthorityDecision{Allowed: true}
	}
	return CheckServiceAuthority(s.Winner.Descriptor.GetRequiredAuthority(), caller.GrantedAuthority)
}

// ServiceConflict keeps every deterministic candidate for operator inspection.
type ServiceConflict struct {
	ServiceID  string
	Version    string
	Candidates []ServiceRegistration
}

// ServiceExtensionSnapshot is a caller-owned view of one extension's complete
// exact-runtime service set. An extension with no declared services has no
// snapshot entry; the immutable Manifest remains the authority for that case.
type ServiceExtensionSnapshot struct {
	Revision      uint64
	ExtensionID   string
	InstanceID    string
	Registrations []ServiceRegistration
}

type preparedServiceRegistration struct {
	registration ServiceRegistration
	publishedID  string
	version      *semver.Version
}

type serviceRegistrySnapshot struct {
	revision           uint64
	registrations      []preparedServiceRegistration
	runtimes           map[string]preparedServiceRuntime
	dependencyEnforced bool
}

// ServiceRegistry publishes immutable read snapshots. A failed extension
// replacement never changes the active snapshot or revision.
type ServiceRegistry struct {
	writeMu  sync.Mutex
	snapshot atomic.Pointer[serviceRegistrySnapshot]
}

func NewServiceRegistry() *ServiceRegistry {
	r := &ServiceRegistry{}
	r.snapshot.Store(&serviceRegistrySnapshot{runtimes: map[string]preparedServiceRuntime{}})
	return r
}

// ReplaceExtension validates the complete contribution set before atomically
// replacing every service owned by extensionID.
func (r *ServiceRegistry) ReplaceExtension(extensionID string, registrations []ServiceRegistration) error {
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidServiceRegistration)
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return fmt.Errorf("%w: extension id is required", ErrInvalidServiceRegistration)
	}

	prepared := make([]preparedServiceRegistration, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		item, err := prepareServiceRegistration(extensionID, registration)
		if err != nil {
			return err
		}
		key := item.publishedID + "\x00" + item.registration.Descriptor.GetVersion()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: extension %q declares %s@%s more than once", ErrInvalidServiceRegistration, extensionID, item.publishedID, item.registration.Descriptor.GetVersion())
		}
		seen[key] = struct{}{}
		prepared = append(prepared, item)
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current := r.loadSnapshot()
	next := make([]preparedServiceRegistration, 0, len(current.registrations)+len(prepared))
	for _, item := range current.registrations {
		if item.registration.ExtensionID != extensionID {
			next = append(next, item)
		}
	}
	next = append(next, prepared...)
	sortPreparedServices(next)
	r.snapshot.Store(&serviceRegistrySnapshot{
		revision: current.revision + 1, registrations: next,
		runtimes: clonePreparedServiceRuntimes(current.runtimes), dependencyEnforced: current.dependencyEnforced,
	})
	return nil
}

// UnregisterExtension atomically removes one runtime's complete service set.
// It returns false without advancing the revision when nothing was registered.
func (r *ServiceRegistry) UnregisterExtension(extensionID string) bool {
	if r == nil {
		return false
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return false
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current := r.loadSnapshot()
	next := make([]preparedServiceRegistration, 0, len(current.registrations))
	for _, item := range current.registrations {
		if item.registration.ExtensionID != extensionID {
			next = append(next, item)
		}
	}
	runtimes := clonePreparedServiceRuntimes(current.runtimes)
	_, hadRuntime := runtimes[extensionID]
	delete(runtimes, extensionID)
	if len(next) == len(current.registrations) && !hadRuntime {
		return false
	}
	r.snapshot.Store(&serviceRegistrySnapshot{
		revision: current.revision + 1, registrations: next,
		runtimes: runtimes, dependencyEnforced: current.dependencyEnforced,
	})
	return true
}

// UnregisterProtocolV2ServiceInstance removes an extension's complete service
// set only when it still belongs to instanceID. This prevents a stale runtime
// shutdown from removing the replacement runtime's registrations.
func (r *ServiceRegistry) UnregisterProtocolV2ServiceInstance(extensionID, instanceID string) bool {
	if r == nil {
		return false
	}
	extensionID = strings.TrimSpace(extensionID)
	instanceID = strings.TrimSpace(instanceID)
	if extensionID == "" || instanceID == "" {
		return false
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	current := r.loadSnapshot()
	found := false
	if runtime, ok := current.runtimes[extensionID]; ok {
		found = true
		if runtime.publication.InstanceID != instanceID {
			return false
		}
	}
	for _, item := range current.registrations {
		if item.registration.ExtensionID != extensionID {
			continue
		}
		found = true
		if item.registration.InstanceID != instanceID {
			return false
		}
	}
	if !found {
		return false
	}
	next := make([]preparedServiceRegistration, 0, len(current.registrations))
	for _, item := range current.registrations {
		if item.registration.ExtensionID != extensionID {
			next = append(next, item)
		}
	}
	runtimes := clonePreparedServiceRuntimes(current.runtimes)
	delete(runtimes, extensionID)
	r.snapshot.Store(&serviceRegistrySnapshot{
		revision: current.revision + 1, registrations: next,
		runtimes: runtimes, dependencyEnforced: current.dependencyEnforced,
	})
	return true
}

func (r *ServiceRegistry) Revision() uint64 {
	return r.loadSnapshot().revision
}

func (r *ServiceRegistry) ExtensionSnapshot(extensionID string) (ServiceExtensionSnapshot, bool, error) {
	extensionID = strings.TrimSpace(extensionID)
	if r == nil || extensionID == "" {
		return ServiceExtensionSnapshot{}, false, fmt.Errorf("%w: extension id is required", ErrInvalidServiceRegistration)
	}
	snapshot := r.loadSnapshot()
	result := ServiceExtensionSnapshot{Revision: snapshot.revision, ExtensionID: extensionID}
	if runtime, ok := snapshot.runtimes[extensionID]; ok {
		result.InstanceID = runtime.publication.InstanceID
	}
	for _, item := range snapshot.registrations {
		if item.registration.ExtensionID != extensionID {
			continue
		}
		if result.InstanceID == "" {
			result.InstanceID = item.registration.InstanceID
		} else if result.InstanceID != item.registration.InstanceID {
			return ServiceExtensionSnapshot{}, false, fmt.Errorf(
				"%w: extension %q spans multiple runtime instances", ErrInvalidServiceRegistration, extensionID,
			)
		}
		result.Registrations = append(result.Registrations, cloneServiceRegistration(item.registration))
	}
	if len(result.Registrations) == 0 && result.InstanceID == "" {
		return ServiceExtensionSnapshot{}, false, nil
	}
	return result, true, nil
}

// List returns one effective provider for each matching service/version pair.
// Results are sorted by service id, version descending, then provider order.
func (r *ServiceRegistry) List(serviceID, versionConstraint string) ([]ResolvedService, error) {
	serviceID = strings.TrimSpace(serviceID)
	constraint, err := parseStrictServiceConstraint(versionConstraint)
	if err != nil {
		return nil, err
	}
	snapshot := r.loadSnapshot()
	result := make([]ResolvedService, 0)
	for index := 0; index < len(snapshot.registrations); {
		first := snapshot.registrations[index]
		end := index + 1
		for end < len(snapshot.registrations) && samePublishedVersion(first, snapshot.registrations[end]) {
			end++
		}
		if (serviceID == "" || first.publishedID == serviceID) && (constraint == nil || constraint.Check(first.version)) {
			candidates := make([]ServiceRegistration, 0, end-index)
			for _, candidate := range snapshot.registrations[index:end] {
				candidates = append(candidates, cloneServiceRegistration(candidate.registration))
			}
			result = append(result, ResolvedService{
				ServiceID: first.publishedID, Revision: snapshot.revision,
				Winner: candidates[0], Candidates: candidates, snapshot: snapshot,
			})
		}
		index = end
	}
	return result, nil
}

// Resolve selects the highest matching semantic version and then the
// deterministic provider winner for that exact version.
func (r *ServiceRegistry) Resolve(serviceID, versionConstraint string) (ResolvedService, error) {
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		return ResolvedService{}, fmt.Errorf("%w: service id is required", ErrInvalidServiceRegistration)
	}
	services, err := r.List(serviceID, versionConstraint)
	if err != nil {
		return ResolvedService{}, err
	}
	if len(services) == 0 {
		return ResolvedService{}, fmt.Errorf("%w: %s %q", ErrServiceNotFound, serviceID, strings.TrimSpace(versionConstraint))
	}
	return services[0], nil
}

// ResolveExact matches the descriptor's original version string. SemVer
// constraints deliberately ignore build metadata for precedence, but an
// invocation is artifact-facing and must not cross build identities.
func (r *ServiceRegistry) ResolveExact(serviceID, version string) (ResolvedService, error) {
	serviceID = strings.TrimSpace(serviceID)
	version = strings.TrimSpace(version)
	if serviceID == "" {
		return ResolvedService{}, fmt.Errorf("%w: service id is required", ErrInvalidServiceRegistration)
	}
	if _, err := semver.StrictNewVersion(version); err != nil {
		return ResolvedService{}, fmt.Errorf("%w: exact version %q is not strict SemVer: %v", ErrInvalidServiceConstraint, version, err)
	}
	snapshot := r.loadSnapshot()
	for index := 0; index < len(snapshot.registrations); {
		first := snapshot.registrations[index]
		end := index + 1
		for end < len(snapshot.registrations) && samePublishedVersion(first, snapshot.registrations[end]) {
			end++
		}
		if first.publishedID == serviceID && first.registration.Descriptor.GetVersion() == version {
			candidates := make([]ServiceRegistration, 0, end-index)
			for _, candidate := range snapshot.registrations[index:end] {
				candidates = append(candidates, cloneServiceRegistration(candidate.registration))
			}
			return ResolvedService{
				ServiceID: serviceID, Revision: snapshot.revision,
				Winner: candidates[0], Candidates: candidates, snapshot: snapshot,
			}, nil
		}
		index = end
	}
	return ResolvedService{}, fmt.Errorf("%w: %s %q", ErrServiceNotFound, serviceID, version)
}

func (r *ServiceRegistry) Conflicts() []ServiceConflict {
	snapshot := r.loadSnapshot()
	conflicts := make([]ServiceConflict, 0)
	for index := 0; index < len(snapshot.registrations); {
		first := snapshot.registrations[index]
		end := index + 1
		for end < len(snapshot.registrations) && samePublishedVersion(first, snapshot.registrations[end]) {
			end++
		}
		if end-index > 1 {
			candidates := make([]ServiceRegistration, 0, end-index)
			for _, candidate := range snapshot.registrations[index:end] {
				candidates = append(candidates, cloneServiceRegistration(candidate.registration))
			}
			conflicts = append(conflicts, ServiceConflict{
				ServiceID: first.publishedID, Version: first.registration.Descriptor.GetVersion(), Candidates: candidates,
			})
		}
		index = end
	}
	return conflicts
}

func CheckServiceAuthority(required, granted []string) ServiceAuthorityDecision {
	required = normalizedStringSet(required)
	granted = normalizedStringSet(granted)
	grantSet := make(map[string]struct{}, len(granted))
	for _, authority := range granted {
		grantSet[authority] = struct{}{}
	}
	missing := make([]string, 0)
	for _, authority := range required {
		if _, ok := grantSet[authority]; !ok {
			missing = append(missing, authority)
		}
	}
	return ServiceAuthorityDecision{
		Allowed: len(missing) == 0, Required: required, Granted: granted, Missing: missing,
	}
}

func prepareServiceRegistration(extensionID string, registration ServiceRegistration) (preparedServiceRegistration, error) {
	registration.ExtensionID = strings.TrimSpace(registration.ExtensionID)
	if registration.ExtensionID != "" && registration.ExtensionID != extensionID {
		return preparedServiceRegistration{}, fmt.Errorf("%w: extension id %q does not match owner %q", ErrInvalidServiceRegistration, registration.ExtensionID, extensionID)
	}
	registration.ExtensionID = extensionID
	registration.InstanceID = strings.TrimSpace(registration.InstanceID)
	if registration.InstanceID == "" {
		return preparedServiceRegistration{}, fmt.Errorf("%w: instance id is required", ErrInvalidServiceRegistration)
	}
	if registration.Provider == nil {
		return preparedServiceRegistration{}, fmt.Errorf("%w: provider is required", ErrInvalidServiceRegistration)
	}
	if registration.Descriptor == nil {
		return preparedServiceRegistration{}, fmt.Errorf("%w: descriptor is required", ErrInvalidServiceRegistration)
	}
	descriptor := proto.Clone(registration.Descriptor).(*protocolv2.ServiceDescriptor)
	descriptor.ServiceId = strings.TrimSpace(descriptor.GetServiceId())
	descriptor.Version = strings.TrimSpace(descriptor.GetVersion())
	descriptor.RequestSchemaId = strings.TrimSpace(descriptor.GetRequestSchemaId())
	descriptor.ResponseSchemaId = strings.TrimSpace(descriptor.GetResponseSchemaId())
	if descriptor.ServiceId == "" || descriptor.RequestSchemaId == "" || descriptor.ResponseSchemaId == "" {
		return preparedServiceRegistration{}, fmt.Errorf("%w: service id and request/response schema ids are required", ErrInvalidServiceRegistration)
	}
	if _, _, ok := splitServiceSchemaRef(descriptor.RequestSchemaId); !ok {
		return preparedServiceRegistration{}, fmt.Errorf("%w: service %q request schema must be a versioned reference", ErrInvalidServiceRegistration, descriptor.ServiceId)
	}
	if _, _, ok := splitServiceSchemaRef(descriptor.ResponseSchemaId); !ok {
		return preparedServiceRegistration{}, fmt.Errorf("%w: service %q response schema must be a versioned reference", ErrInvalidServiceRegistration, descriptor.ServiceId)
	}
	version, err := semver.StrictNewVersion(descriptor.Version)
	if err != nil {
		return preparedServiceRegistration{}, fmt.Errorf("%w: service %q version %q is not strict SemVer: %v", ErrInvalidServiceRegistration, descriptor.ServiceId, descriptor.Version, err)
	}
	authority, err := normalizeRequiredAuthority(descriptor.GetRequiredAuthority())
	if err != nil {
		return preparedServiceRegistration{}, err
	}
	descriptor.RequiredAuthority = authority
	registration.Descriptor = descriptor
	registration.Action = strings.ToLower(strings.TrimSpace(registration.Action))
	if !validServiceAction(registration.Action) {
		return preparedServiceRegistration{}, fmt.Errorf("%w: unsupported action %q", ErrInvalidServiceRegistration, registration.Action)
	}
	if registration.Action == ServiceActionBefore || registration.Action == ServiceActionAfter || registration.Action == ServiceActionWrap {
		return preparedServiceRegistration{}, fmt.Errorf("%w: action %q requires the composition chain", ErrInvalidServiceRegistration, registration.Action)
	}
	registration.TargetID = strings.TrimSpace(registration.TargetID)
	publishedID := descriptor.ServiceId
	if registration.Action != ServiceActionAdd {
		if registration.TargetID == "" {
			return preparedServiceRegistration{}, fmt.Errorf("%w: action %q requires a target id", ErrInvalidServiceRegistration, registration.Action)
		}
		publishedID = registration.TargetID
	} else if registration.TargetID != "" {
		return preparedServiceRegistration{}, fmt.Errorf("%w: add action must not declare a target id", ErrInvalidServiceRegistration)
	}
	return preparedServiceRegistration{registration: registration, publishedID: publishedID, version: version}, nil
}

func parseStrictServiceConstraint(value string) (*semver.Constraints, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	constraint, err := semver.NewConstraint(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidServiceConstraint, value, err)
	}
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '|'
	}) {
		if token == "-" || isServiceConstraintOperator(token) {
			continue
		}
		literal := token
		for _, operator := range []string{"!=", ">=", "<=", "~>", "^", "~", ">", "<", "="} {
			if strings.HasPrefix(literal, operator) {
				literal = strings.TrimPrefix(literal, operator)
				break
			}
		}
		if literal == "" {
			return nil, fmt.Errorf("%w: %q", ErrInvalidServiceConstraint, value)
		}
		if _, err := semver.StrictNewVersion(literal); err != nil {
			return nil, fmt.Errorf("%w: %q contains non-strict version %q", ErrInvalidServiceConstraint, value, literal)
		}
	}
	return constraint, nil
}

func validServiceAction(action string) bool {
	switch action {
	case ServiceActionAdd, ServiceActionBefore, ServiceActionAfter, ServiceActionWrap, ServiceActionReplace:
		return true
	default:
		return false
	}
}

func isServiceConstraintOperator(value string) bool {
	switch value {
	case "!=", ">=", "<=", "~>", "^", "~", ">", "<", "=":
		return true
	default:
		return false
	}
}

func normalizeRequiredAuthority(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%w: required authority contains an empty value", ErrInvalidServiceRegistration)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizedStringSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func splitServiceSchemaRef(schemaID string) (string, string, bool) {
	index := strings.LastIndex(schemaID, "@")
	if index <= 0 || index == len(schemaID)-1 {
		return "", "", false
	}
	version := schemaID[index+1:]
	if version[0] == '0' {
		return "", "", false
	}
	for _, value := range version {
		if value < '0' || value > '9' {
			return "", "", false
		}
	}
	return schemaID[:index], version, true
}

func sortPreparedServices(values []preparedServiceRegistration) {
	sort.SliceStable(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.publishedID != right.publishedID {
			return left.publishedID < right.publishedID
		}
		if compared := left.version.Compare(right.version); compared != 0 {
			return compared > 0
		}
		leftVersion, rightVersion := left.registration.Descriptor.GetVersion(), right.registration.Descriptor.GetVersion()
		if leftVersion != rightVersion {
			return leftVersion < rightVersion
		}
		if left.registration.Priority != right.registration.Priority {
			return left.registration.Priority > right.registration.Priority
		}
		if left.registration.ExtensionID != right.registration.ExtensionID {
			return left.registration.ExtensionID < right.registration.ExtensionID
		}
		if left.registration.InstanceID != right.registration.InstanceID {
			return left.registration.InstanceID < right.registration.InstanceID
		}
		return left.registration.Descriptor.GetServiceId() < right.registration.Descriptor.GetServiceId()
	})
}

func samePublishedVersion(left, right preparedServiceRegistration) bool {
	return left.publishedID == right.publishedID && left.registration.Descriptor.GetVersion() == right.registration.Descriptor.GetVersion()
}

func cloneServiceRegistration(value ServiceRegistration) ServiceRegistration {
	if value.Descriptor != nil {
		value.Descriptor = proto.Clone(value.Descriptor).(*protocolv2.ServiceDescriptor)
	}
	return value
}

func (r *ServiceRegistry) loadSnapshot() *serviceRegistrySnapshot {
	if r == nil {
		return &serviceRegistrySnapshot{runtimes: map[string]preparedServiceRuntime{}}
	}
	if snapshot := r.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return &serviceRegistrySnapshot{runtimes: map[string]preparedServiceRuntime{}}
}
