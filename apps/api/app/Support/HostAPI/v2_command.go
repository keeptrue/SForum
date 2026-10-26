package hostapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const protocolV2CommandRollbackTimeout = 5 * time.Second

var errProtocolV2CommandAuthorityGateContract = errors.New("hostapi: identity authority gate callback contract violated")

type protocolV2CommandServer struct {
	hostv2.UnimplementedHostCommandServiceServer
	core *protocolV2Core
}

func (s *protocolV2CommandServer) Plan(ctx context.Context, request *hostv2.CommandRequest) (*hostv2.CommandPlan, error) {
	if s == nil || s.core == nil || s.core.commands == nil {
		return unavailableProtocolV2CommandPlan(request), nil
	}
	return s.core.commands.plan(ctx, request)
}

func (s *protocolV2CommandServer) Execute(ctx context.Context, request *hostv2.CommandRequest) (*hostv2.CommandResult, error) {
	if s == nil || s.core == nil || s.core.commands == nil {
		return &hostv2.CommandResult{
			Context: protocolV2ResponseContext(request.GetContext()),
			State:   hostv2.CommandState_COMMAND_STATE_REJECTED,
			Error:   protocolV2CommandUnavailable(),
		}, nil
	}
	return s.core.commands.execute(ctx, request)
}

// protocolV2CommandBackend keeps the idempotency ledger and audit event in the
// same pgx transaction as the command's business writes. LockIdempotency must
// serialize one extension/key pair until tx ends. Domain commands are bound by
// a separate Host-owned catalog.
type protocolV2CommandBackend interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	ResolveScope(ctx context.Context, tx pgx.Tx, requested protocolV2CommandScope) (protocolV2CommandScope, error)
	LockIdempotency(ctx context.Context, tx pgx.Tx, scope protocolV2CommandScope) (*protocolV2CommandReceipt, error)
	AuthorizeActorDelegation(ctx context.Context, tx pgx.Tx, scope protocolV2CommandScope, delegation protocolV2VerifiedActorDelegation, fingerprint string, receiptExists bool, requiredPermissions []string) (int64, error)
	SaveResult(ctx context.Context, tx pgx.Tx, scope protocolV2CommandScope, receipt protocolV2CommandReceipt) error
	AppendAudit(ctx context.Context, tx pgx.Tx, event protocolV2CommandAudit) (string, error)
}

type protocolV2CommandScope struct {
	ExtensionID        string
	ExtensionVersionID int64
	ExtensionVersion   string
	PackageDigest      string
	AuthorityType      string
	TrustGrantID       int64
	RuntimeEpoch       int64
	RuntimeInstanceID  string
	CommandID          string
	CommandVersion     string
	IdempotencyKey     string
}

type protocolV2CommandReceipt struct {
	Fingerprint string
	Result      *hostv2.CommandResult
}

type protocolV2CommandAudit struct {
	Scope          protocolV2CommandScope
	ExtensionID    string
	ActorUserID    int64
	CommandID      string
	CommandVersion string
	TransactionID  string
	IdempotencyKey string
	Impact         []*hostv2.ImpactItem
}

type protocolV2CommandPreparation struct {
	Policy          []*hostv2.PolicyDecision
	Impact          []*hostv2.ImpactItem
	ProjectedResult *protocolv2.TypedDocument
	// private carries Host-only prepared state from the authoritative read phase
	// into Execute. It is never serialized into a plugin-visible plan or audit.
	private any
}

type protocolV2CommandExecution struct {
	Output            *protocolv2.TypedDocument
	CommittedRevision string
}

type protocolV2CommandActorMode string

const (
	protocolV2CommandActorService   protocolV2CommandActorMode = "service"
	protocolV2CommandActorDelegated protocolV2CommandActorMode = "delegated"
)

type protocolV2CommandDefinition struct {
	ID                  string
	Version             string
	InputSchemaID       string
	InputSchemaVersion  string
	OutputSchemaID      string
	OutputSchemaVersion string
	ActorMode           protocolV2CommandActorMode
	RequiredPermissions []string
	// RequiredCapability 是可选的进程能力（非人类 RBAC）。空表示不额外要求。
	RequiredCapability string
	// RunAuthorityMutation is set only for commands that mutate identity
	// authority shared with an accepted session effect. It must run before the
	// command borrows a transaction from the main pool.
	RunAuthorityMutation func(context.Context, func() error) error
	// Preview is read-only guidance for Plan. Execute never trusts its result.
	Preview func(context.Context, *hostv2.CommandRequest) (*protocolV2CommandPreparation, error)
	// Prepare performs authoritative reads through tx immediately before writes.
	Prepare func(context.Context, pgx.Tx, *hostv2.CommandRequest) (*protocolV2CommandPreparation, error)
	Execute func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error)
}

type protocolV2CommandKey struct {
	id      string
	version string
}

// protocolV2CommandEngine is immutable after construction, so planning and
// execution can run concurrently without a mutable registration surface.
type protocolV2CommandEngine struct {
	backend               protocolV2CommandBackend
	delegations           *ProtocolV2ActorDelegationAuthority
	queryInvalidationJobs *supportjobs.Dispatcher
	capabilities          CapabilitySource
	definitions           map[protocolV2CommandKey]protocolV2CommandDefinition
}

// ProtocolV2CommandRuntime is a sealed Host-owned command catalog. P5's
// PostgreSQL command package constructs one and binds it before broker boot.
type ProtocolV2CommandRuntime interface {
	commandEngine() *protocolV2CommandEngine
}

type protocolV2CommandRuntime struct {
	engine *protocolV2CommandEngine
}

func (r *protocolV2CommandRuntime) commandEngine() *protocolV2CommandEngine {
	if r == nil {
		return nil
	}
	return r.engine
}

func newProtocolV2CommandRuntime(engine *protocolV2CommandEngine) ProtocolV2CommandRuntime {
	return &protocolV2CommandRuntime{engine: engine}
}

func newProtocolV2CommandEngine(backend protocolV2CommandBackend, definitions ...protocolV2CommandDefinition) (*protocolV2CommandEngine, error) {
	return newProtocolV2CommandEngineWithActorDelegation(backend, nil, definitions...)
}

func newProtocolV2CommandEngineWithActorDelegation(
	backend protocolV2CommandBackend,
	delegations *ProtocolV2ActorDelegationAuthority,
	definitions ...protocolV2CommandDefinition,
) (*protocolV2CommandEngine, error) {
	return newProtocolV2CommandEngineWithInvalidationJobs(backend, delegations, nil, definitions...)
}

func newProtocolV2CommandEngineWithInvalidationJobs(
	backend protocolV2CommandBackend,
	delegations *ProtocolV2ActorDelegationAuthority,
	queryInvalidationJobs *supportjobs.Dispatcher,
	definitions ...protocolV2CommandDefinition,
) (*protocolV2CommandEngine, error) {
	registered := make(map[protocolV2CommandKey]protocolV2CommandDefinition, len(definitions))
	for _, definition := range definitions {
		definition.ID = strings.TrimSpace(definition.ID)
		definition.Version = strings.TrimSpace(definition.Version)
		key := protocolV2CommandKey{id: definition.ID, version: definition.Version}
		if definition.ID == "" || definition.Version == "" || definition.Preview == nil || definition.Prepare == nil || definition.Execute == nil {
			return nil, fmt.Errorf("hostapi: command id, version, preview, prepare, and execute are required")
		}
		if (definition.InputSchemaID == "") != (definition.InputSchemaVersion == "") ||
			(definition.OutputSchemaID == "") != (definition.OutputSchemaVersion == "") {
			return nil, fmt.Errorf("hostapi: command schema id and version must be declared together")
		}
		if definition.ActorMode == "" {
			definition.ActorMode = protocolV2CommandActorService
		}
		permissions := make([]string, 0, len(definition.RequiredPermissions))
		seenPermissions := make(map[string]bool, len(definition.RequiredPermissions))
		for _, permission := range definition.RequiredPermissions {
			permission = strings.TrimSpace(permission)
			if permission == "" || seenPermissions[permission] {
				return nil, fmt.Errorf("hostapi: command permissions must be non-empty and unique")
			}
			seenPermissions[permission] = true
			permissions = append(permissions, permission)
		}
		sort.Strings(permissions)
		definition.RequiredPermissions = permissions
		switch definition.ActorMode {
		case protocolV2CommandActorService:
			if len(permissions) != 0 {
				return nil, fmt.Errorf("hostapi: actorless service command cannot require actor permissions")
			}
		case protocolV2CommandActorDelegated:
			if delegations == nil {
				return nil, fmt.Errorf("hostapi: delegated command requires an actor delegation authority")
			}
		default:
			return nil, fmt.Errorf("hostapi: command actor mode is unsupported")
		}
		if _, exists := registered[key]; exists {
			return nil, fmt.Errorf("hostapi: duplicate command %s@%s", definition.ID, definition.Version)
		}
		registered[key] = definition
	}
	return &protocolV2CommandEngine{
		backend: backend, delegations: delegations, queryInvalidationJobs: queryInvalidationJobs,
		definitions: registered,
	}, nil
}

// BindCapabilitySource 在 bootstrap 二阶段注入进程能力源（扩展 manage 等门禁）。
func (e *protocolV2CommandEngine) BindCapabilitySource(source CapabilitySource) {
	if e != nil {
		e.capabilities = source
	}
}

// BindCapabilitySource 将能力源转发到已绑定的 Command 引擎。
func (r *protocolV2CommandRuntime) BindCapabilitySource(source CapabilitySource) {
	if r != nil && r.engine != nil {
		r.engine.BindCapabilitySource(source)
	}
}

func (e *protocolV2CommandEngine) plan(ctx context.Context, request *hostv2.CommandRequest) (*hostv2.CommandPlan, error) {
	definition, _, plan := e.definition(ctx, request)
	if plan.GetError() != nil {
		return plan, nil
	}
	// Plan validates the signed binding but deliberately does not expose an actor
	// context: live status and permissions are authoritative only inside Execute's
	// PostgreSQL transaction.
	preparation, err := definition.Preview(ctx, request)
	plan = finalizeProtocolV2CommandPlan(request, definition, preparation, err, plan)
	return plan, nil
}

func (e *protocolV2CommandEngine) definition(ctx context.Context, request *hostv2.CommandRequest) (protocolV2CommandDefinition, *protocolV2VerifiedActorDelegation, *hostv2.CommandPlan) {
	plan := &hostv2.CommandPlan{
		Context:        protocolV2ResponseContext(request.GetContext()),
		CommandId:      strings.TrimSpace(request.GetCommandId()),
		CommandVersion: strings.TrimSpace(request.GetCommandVersion()),
	}
	if e == nil {
		plan.Error = protocolV2CommandUnavailable()
		return protocolV2CommandDefinition{}, nil, plan
	}
	definition, ok := e.definitions[protocolV2CommandKey{id: plan.GetCommandId(), version: plan.GetCommandVersion()}]
	if !ok {
		plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.command_unsupported", "The command id or version is not registered.", false)
		return protocolV2CommandDefinition{}, nil, plan
	}
	if strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId()) == "" {
		plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_extension_required", "The authenticated extension identity is required.", false)
		return definition, nil, plan
	}
	if tags := request.GetQueryInvalidationTags(); len(tags) > 0 {
		if e.queryInvalidationJobs == nil {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.command_query_invalidation_unavailable", "Query cache invalidation is unavailable.", true)
			return definition, nil, plan
		}
		canonical, err := queryregistry.CanonicalSemanticCacheTags(
			request.GetContext().GetExtension().GetExtensionId(), tags,
		)
		if err != nil || !slices.Equal(canonical, tags) {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.command_query_invalidation_invalid", "Query cache invalidation tags must be canonical and owned by the caller.", false)
			return definition, nil, plan
		}
	}
	if request.GetContext().GetActor() != nil {
		plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.command_actor_unattested", "Plugin-initiated Host Commands cannot supply an actor.", false)
		return definition, nil, plan
	}
	if detail := validateProtocolV2CommandDocument(request.GetInput(), definition.InputSchemaID, definition.InputSchemaVersion, "input"); detail != nil {
		plan.Error = detail
		return definition, nil, plan
	}
	if capability := strings.TrimSpace(definition.RequiredCapability); capability != "" {
		extensionID := strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId())
		if e.capabilities == nil || extensionID == "" {
			plan.Error = commandError(
				protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"host.command_capability_denied",
				"The caller does not hold the required process capability.",
				false,
			)
			return definition, nil, plan
		}
		caps, err := e.capabilities.CapabilitiesFor(ctx, extensionID)
		if err != nil || caps.Require(capability) != nil {
			plan.Error = commandError(
				protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
				"host.command_capability_denied",
				"The caller does not hold the required process capability.",
				false,
			)
			return definition, nil, plan
		}
	}
	switch definition.ActorMode {
	case protocolV2CommandActorService:
		if strings.TrimSpace(request.GetActorDelegation()) != "" {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.command_actor_delegation_unexpected", "This Host Command accepts only actorless service authority.", false)
		}
		return definition, nil, plan
	case protocolV2CommandActorDelegated:
		if strings.TrimSpace(request.GetActorDelegation()) == "" {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED, "host.command_actor_delegation_required", "A Host-signed actor delegation is required.", false)
			return definition, nil, plan
		}
		idempotencyKey, detail := protocolV2CommandIdempotencyKey(request, true)
		if detail != nil {
			plan.Error = detail
			return definition, nil, plan
		}
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if runtime == nil || request.GetContext().GetExtension().GetExtensionId() != runtime.GetExtensionId() || e.delegations == nil {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED, "host.command_actor_delegation_invalid", "The Host-signed actor delegation is invalid or stale.", false)
			return definition, nil, plan
		}
		delegation, err := e.delegations.verifyActorDelegationForCommand(
			request.GetActorDelegation(), runtime, definition.ID, definition.Version, idempotencyKey,
		)
		if err != nil {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED, "host.command_actor_delegation_invalid", "The Host-signed actor delegation is invalid or stale.", false)
			return definition, nil, plan
		}
		return definition, &delegation, plan
	default:
		plan.Error = protocolV2CommandUnavailable()
		return definition, nil, plan
	}
}

func finalizeProtocolV2CommandPlan(
	request *hostv2.CommandRequest,
	definition protocolV2CommandDefinition,
	preparation *protocolV2CommandPreparation,
	err error,
	plan *hostv2.CommandPlan,
) *hostv2.CommandPlan {
	if err != nil {
		plan.Error = protocolV2CommandErrorDetail(err, "host.command_plan_failed")
		return plan
	}
	if preparation == nil {
		plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.command_plan_invalid", "The command returned no impact plan.", false)
		return plan
	}
	plan.Policy = cloneProtocolV2Policies(preparation.Policy)
	plan.Impact = cloneProtocolV2Impact(preparation.Impact)
	plan.ProjectedResult = cloneProtocolV2Document(preparation.ProjectedResult)
	if len(plan.GetPolicy()) == 0 {
		plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.command_policy_required", "The command returned no Host policy decision.", false)
		return plan
	}
	if detail := validateProtocolV2CommandDocument(plan.GetProjectedResult(), definition.OutputSchemaID, definition.OutputSchemaVersion, "projected result"); detail != nil {
		plan.Error = detail
		return plan
	}
	for _, decision := range plan.GetPolicy() {
		if decision == nil || strings.TrimSpace(decision.GetPolicyId()) == "" {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.command_policy_invalid", "The command returned an invalid policy decision.", false)
			return plan
		}
		if !decision.GetAllowed() {
			plan.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.command_policy_denied", "Host policy denied the command.", false)
		}
	}
	plan.PlanId = protocolV2CommandPlanID(request, plan)
	return plan
}

func (e *protocolV2CommandEngine) execute(ctx context.Context, request *hostv2.CommandRequest) (*hostv2.CommandResult, error) {
	definition, delegation, plan := e.definition(ctx, request)
	result := &hostv2.CommandResult{Context: protocolV2ResponseContext(request.GetContext())}
	if plan.GetError() != nil {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = cloneProtocolV2CommandError(plan.GetError())
		return result, nil
	}
	if request.GetDryRun() {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.command_dry_run_requires_plan", "Use HostCommandService.Plan for dry-run impact data.", false)
		return result, nil
	}
	idempotencyKey, detail := protocolV2CommandIdempotencyKey(request, true)
	if detail != nil {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = detail
		return result, nil
	}
	if e.backend == nil {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = protocolV2CommandUnavailable()
		return result, nil
	}
	fingerprint, err := protocolV2CommandExecutionFingerprint(ctx, request, delegation)
	if err != nil {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = protocolV2CommandErrorDetail(err, "host.command_fingerprint_failed")
		return result, nil
	}
	scope := protocolV2CommandScope{
		ExtensionID: strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId()),
		CommandID:   definition.ID, CommandVersion: definition.Version, IdempotencyKey: idempotencyKey,
	}
	if runtime := ProtocolV2RuntimeIdentityFromContext(ctx); runtime != nil && runtime.GetRuntimeEpoch() <= uint64(^uint64(0)>>1) {
		scope.RuntimeEpoch = int64(runtime.GetRuntimeEpoch())
		scope.RuntimeInstanceID = runtime.GetInstanceId()
	}
	if definition.RunAuthorityMutation == nil {
		return e.executeTransaction(ctx, request, definition, delegation, fingerprint, scope, result)
	}
	commandResult, commandErr, gateErr := runProtocolV2CommandAuthorityMutation(
		ctx,
		definition.RunAuthorityMutation,
		func() (*hostv2.CommandResult, error) {
			return e.executeTransaction(
				ctx, request, definition, delegation, fingerprint, scope, result,
			)
		},
	)
	if commandErr != nil {
		return commandResult, commandErr
	}
	if commandResult != nil {
		// A completed command result is terminal. A malformed gate cannot turn a
		// committed or rolled-back transaction into a retryable response.
		return commandResult, nil
	}
	result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
	result.Error = protocolV2CommandAuthorityErrorDetail(gateErr)
	return result, nil
}

func runProtocolV2CommandAuthorityMutation(
	ctx context.Context,
	gate func(context.Context, func() error) error,
	run func() (*hostv2.CommandResult, error),
) (*hostv2.CommandResult, error, error) {
	if gate == nil || run == nil {
		return nil, nil, errProtocolV2CommandAuthorityGateContract
	}

	var mu sync.Mutex
	open := true
	started := false
	var commandResult *hostv2.CommandResult
	var commandErr error
	var callbackPanic any
	done := make(chan struct{})

	callback := func() (result error) {
		mu.Lock()
		if !open || started {
			mu.Unlock()
			return errProtocolV2CommandAuthorityGateContract
		}
		if err := ctx.Err(); err != nil {
			mu.Unlock()
			return err
		}
		started = true
		mu.Unlock()

		defer func() {
			panicValue := recover()
			if panicValue != nil {
				result = errProtocolV2CommandAuthorityGateContract
			}
			mu.Lock()
			commandErr = result
			callbackPanic = panicValue
			close(done)
			mu.Unlock()
		}()
		commandResult, result = run()
		return result
	}

	var gateErr error
	var gatePanic any
	func() {
		defer func() { gatePanic = recover() }()
		gateErr = gate(ctx, callback)
	}()

	mu.Lock()
	open = false
	wasStarted := started
	mu.Unlock()
	if wasStarted {
		<-done
	}
	mu.Lock()
	result := commandResult
	err := commandErr
	panicValue := callbackPanic
	mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
	if gatePanic != nil {
		panic(gatePanic)
	}
	if wasStarted {
		if err != nil || result != nil {
			return result, err, nil
		}
		return nil, nil, errProtocolV2CommandAuthorityGateContract
	}
	if gateErr != nil {
		return nil, nil, gateErr
	}
	return nil, nil, errProtocolV2CommandAuthorityGateContract
}

func protocolV2CommandAuthorityErrorDetail(err error) *protocolv2.ErrorDetail {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return protocolV2CommandErrorDetail(err, "host.command_authority_unavailable")
	case errors.Is(err, errProtocolV2CommandAuthorityGateContract):
		return commandError(
			protocolv2.ErrorCode_ERROR_CODE_INTERNAL,
			"host.command_authority_contract_invalid",
			"The Host identity authority gate violated its callback contract.",
			false,
		)
	default:
		return commandError(
			protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"host.command_authority_unavailable",
			"The Host identity authority gate is unavailable.",
			true,
		)
	}
}

func (e *protocolV2CommandEngine) executeTransaction(
	ctx context.Context,
	request *hostv2.CommandRequest,
	definition protocolV2CommandDefinition,
	delegation *protocolV2VerifiedActorDelegation,
	fingerprint string,
	scope protocolV2CommandScope,
	result *hostv2.CommandResult,
) (*hostv2.CommandResult, error) {
	tx, err := e.backend.Begin(ctx)
	if err != nil {
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.command_transaction_unavailable", "The Host command transaction could not be started.", true)
		return result, nil
	}

	resolvedScope, err := e.backend.ResolveScope(ctx, tx, scope)
	if err != nil {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), protocolV2CommandRollbackTimeout)
		defer cancelRollback()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return nil, status.Error(codes.Internal, "Host command identity rollback failed; transaction outcome is unknown")
		}
		result.State = hostv2.CommandState_COMMAND_STATE_REJECTED
		result.Error = protocolV2CommandErrorDetail(err, "host.command_identity_invalid")
		return result, nil
	}
	committed, txnErr := e.executeInTransaction(ctx, tx, resolvedScope, fingerprint, definition, delegation, request)
	if txnErr != nil {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), protocolV2CommandRollbackTimeout)
		defer cancelRollback()
		if rollbackErr := tx.Rollback(rollbackCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return nil, status.Error(codes.Internal, "Host command rollback failed; transaction outcome is unknown")
		}
		result.State = hostv2.CommandState_COMMAND_STATE_ROLLED_BACK
		result.Error = protocolV2CommandErrorDetail(txnErr, "host.command_rolled_back")
		return result, nil
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			result.State = hostv2.CommandState_COMMAND_STATE_ROLLED_BACK
			result.Error = commandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.command_commit_rolled_back", "The database rejected the commit and rolled the command back.", true)
			return result, nil
		}
		return nil, status.Error(codes.Internal, "Host command commit failed; transaction outcome is unknown")
	}
	committed.Context = result.GetContext()
	return committed, nil
}

func (e *protocolV2CommandEngine) executeInTransaction(
	ctx context.Context,
	tx pgx.Tx,
	scope protocolV2CommandScope,
	fingerprint string,
	definition protocolV2CommandDefinition,
	delegation *protocolV2VerifiedActorDelegation,
	request *hostv2.CommandRequest,
) (*hostv2.CommandResult, error) {
	receipt, err := e.backend.LockIdempotency(ctx, tx, scope)
	if err != nil {
		return nil, fmt.Errorf("lock idempotency key: %w", err)
	}
	actorUserID := int64(0)
	commandCtx := ctx
	if delegation != nil {
		actorUserID, err = e.backend.AuthorizeActorDelegation(
			ctx, tx, scope, *delegation, fingerprint, receipt != nil, definition.RequiredPermissions,
		)
		if err != nil {
			return nil, fmt.Errorf("authorize actor delegation: %w", err)
		}
		commandCtx = contextWithProtocolV2CommandActor(ctx, delegation)
	}
	if receipt != nil {
		if receipt.Fingerprint != fingerprint || receipt.Result == nil {
			return nil, newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.command_idempotency_conflict", "The idempotency key was already used for a different command request.", false)
		}
		replayed := proto.Clone(receipt.Result).(*hostv2.CommandResult)
		replayed.State = hostv2.CommandState_COMMAND_STATE_REPLAYED
		return replayed, nil
	}
	preparation, err := definition.Prepare(commandCtx, tx, request)
	if err != nil {
		return nil, fmt.Errorf("prepare command: %w", err)
	}
	plan := finalizeProtocolV2CommandPlan(request, definition, preparation, nil, &hostv2.CommandPlan{
		CommandId: definition.ID, CommandVersion: definition.Version,
	})
	if plan.GetError() != nil {
		return nil, &protocolV2CommandError{detail: cloneProtocolV2CommandError(plan.GetError())}
	}

	execution, err := definition.Execute(commandCtx, tx, request, preparation)
	if err != nil {
		return nil, err
	}
	if execution == nil {
		return nil, newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.command_result_invalid", "The command returned no result.", false)
	}
	if detail := validateProtocolV2CommandDocument(execution.Output, definition.OutputSchemaID, definition.OutputSchemaVersion, "output"); detail != nil {
		return nil, &protocolV2CommandError{detail: detail}
	}
	if len(request.GetQueryInvalidationTags()) > 0 {
		if _, err := queryregistryjobs.EnqueueInvalidationTx(
			ctx, e.queryInvalidationJobs, tx, scope.ExtensionID, request.GetQueryInvalidationTags(),
		); err != nil {
			return nil, fmt.Errorf("enqueue Query cache invalidation: %w", err)
		}
	}
	transactionID, err := newProtocolV2CommandID("txn")
	if err != nil {
		return nil, fmt.Errorf("create transaction id: %w", err)
	}
	auditID, err := e.backend.AppendAudit(ctx, tx, protocolV2CommandAudit{
		Scope:       scope,
		ExtensionID: scope.ExtensionID,
		ActorUserID: actorUserID,
		CommandID:   definition.ID, CommandVersion: definition.Version,
		TransactionID: transactionID, IdempotencyKey: scope.IdempotencyKey,
		Impact: cloneProtocolV2Impact(preparation.Impact),
	})
	if err != nil {
		return nil, fmt.Errorf("append command audit: %w", err)
	}
	if strings.TrimSpace(auditID) == "" {
		return nil, fmt.Errorf("append command audit: empty event id")
	}
	committed := &hostv2.CommandResult{
		State:             hostv2.CommandState_COMMAND_STATE_COMMITTED,
		TransactionId:     transactionID,
		AuditEventId:      auditID,
		CommittedRevision: execution.CommittedRevision,
		Output:            cloneProtocolV2Document(execution.Output),
	}
	if err := e.backend.SaveResult(ctx, tx, scope, protocolV2CommandReceipt{Fingerprint: fingerprint, Result: proto.Clone(committed).(*hostv2.CommandResult)}); err != nil {
		return nil, fmt.Errorf("save command result: %w", err)
	}
	return committed, nil
}

type protocolV2CommandActorContextKey struct{}

// ProtocolV2CommandActorUserID returns only the Host-verified actor installed
// by the command engine. RequestContext.actor is never consulted.
func ProtocolV2CommandActorUserID(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	userID, ok := ctx.Value(protocolV2CommandActorContextKey{}).(int64)
	return userID, ok && userID > 0
}

func contextWithProtocolV2CommandActor(ctx context.Context, delegation *protocolV2VerifiedActorDelegation) context.Context {
	if ctx == nil || delegation == nil || delegation.ActorUserID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, protocolV2CommandActorContextKey{}, delegation.ActorUserID)
}
