package extensions

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// LifecyclePublicError deliberately omits opaque metadata returned by trusted
// code. Recovery screens only need stable classification and operator text.
type LifecyclePublicError struct {
	Code       string     `json:"code,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Message    string     `json:"message,omitempty"`
	Retryable  bool       `json:"retryable"`
	RetryAfter *time.Time `json:"retryAfter,omitempty"`
}

type LifecycleOperationSummary struct {
	ID                   int64                `json:"id"`
	ExtensionID          string               `json:"extensionId"`
	ExtensionVersion     string               `json:"extensionVersion"`
	PackageDigest        string               `json:"packageDigest"`
	Operation            string               `json:"operation"`
	State                string               `json:"state"`
	PlanVersion          string               `json:"planVersion"`
	RemovalMode          string               `json:"removalMode,omitempty"`
	Forced               bool                 `json:"forced"`
	AttemptCount         int                  `json:"attemptCount"`
	Revision             int64                `json:"revision"`
	CurrentStepID        string               `json:"currentStepId,omitempty"`
	TerminalResult       string               `json:"terminalResult,omitempty"`
	RequestedByUserID    int64                `json:"requestedByUserId,omitempty"`
	AuditEventID         int64                `json:"auditEventId,omitempty"`
	RecoveryActorUserID  int64                `json:"recoveryActorUserId,omitempty"`
	RecoveryAuditEventID int64                `json:"recoveryAuditEventId,omitempty"`
	Error                LifecyclePublicError `json:"error"`
	CreatedAt            time.Time            `json:"createdAt"`
	UpdatedAt            time.Time            `json:"updatedAt"`
	StartedAt            *time.Time           `json:"startedAt,omitempty"`
	CompletedAt          *time.Time           `json:"completedAt,omitempty"`

	// Closed 是「操作已到达 durable 终态」的唯一判据（completed_at 非空），
	// 与 ListOpenLifecycleOperations 的 fence、RecoverLifecycleOperation 和
	// `sforum extension recovery lifecycle-close` 完全同源。State 只是最后一次
	// 阶段（uninstall 收尾于 uninstalling、disable 收尾于 draining），不能拿来
	// 判断操作是否还在进行。
	Closed bool `json:"closed"`
}

type LifecycleRecoverySummary struct {
	ID               int64     `json:"id"`
	OperationAttempt int       `json:"operationAttempt"`
	Decision         string    `json:"decision"`
	EscalateForced   bool      `json:"escalateForced"`
	Reason           string    `json:"reason,omitempty"`
	ActorUserID      int64     `json:"actorUserId"`
	AuditEventID     int64     `json:"auditEventId"`
	CreatedAt        time.Time `json:"createdAt"`
}

type LifecycleStepSummary struct {
	ID              int64                `json:"id"`
	StepID          string               `json:"stepId"`
	LifecycleAction string               `json:"lifecycleAction"`
	PlanVersion     string               `json:"planVersion"`
	Attempt         int                  `json:"attempt"`
	Status          string               `json:"status"`
	CompletedUnits  int64                `json:"completedUnits"`
	TotalUnits      int64                `json:"totalUnits"`
	ProgressMessage string               `json:"progressMessage,omitempty"`
	SkipReason      string               `json:"skipReason,omitempty"`
	Forced          bool                 `json:"forced"`
	ActorUserID     int64                `json:"actorUserId,omitempty"`
	AuditEventID    int64                `json:"auditEventId,omitempty"`
	Error           LifecyclePublicError `json:"error"`
	CreatedAt       time.Time            `json:"createdAt"`
	UpdatedAt       time.Time            `json:"updatedAt"`
	StartedAt       *time.Time           `json:"startedAt,omitempty"`
	CompletedAt     *time.Time           `json:"completedAt,omitempty"`
}

type LifecycleOperationDetail struct {
	LifecycleOperationSummary
	Steps      []LifecycleStepSummary     `json:"steps"`
	Recoveries []LifecycleRecoverySummary `json:"recoveries"`
}

func WithLifecycleInspectionRepository(repository LifecycleInspectionRepository) ServiceOption {
	return func(service *Service) {
		service.lifecycleInspector = repository
	}
}

func (s *LifecycleService) LifecycleOperations(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	limit int,
) ([]LifecycleOperationSummary, error) {
	if !canViewExtensions(actor) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.lifecycleInspector == nil {
		return nil, ErrLifecycleCoordinatorUnavailable
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" {
		return nil, ErrLifecycleInvalidInput
	}
	operations, err := s.lifecycleInspector.ListOperations(ctx, extensionID, limit)
	if err != nil {
		return nil, err
	}
	result := make([]LifecycleOperationSummary, 0, len(operations))
	for _, operation := range operations {
		if operation.ExtensionID != extensionID {
			return nil, ErrLifecycleInvalidInput
		}
		result = append(result, lifecycleOperationSummary(operation))
	}
	return result, nil
}

func (s *LifecycleService) LifecycleOperation(
	ctx context.Context,
	actor identity.Actor,
	extensionID string,
	operationID int64,
) (LifecycleOperationDetail, error) {
	if !canViewExtensions(actor) {
		return LifecycleOperationDetail{}, identity.ErrPermissionDenied
	}
	return s.lifecycleOperationDetail(ctx, extensionID, operationID)
}

func (s *serviceCore) lifecycleOperationDetail(
	ctx context.Context,
	extensionID string,
	operationID int64,
) (LifecycleOperationDetail, error) {
	if s == nil || s.lifecycleInspector == nil {
		return LifecycleOperationDetail{}, ErrLifecycleCoordinatorUnavailable
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" || operationID <= 0 {
		return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
	}
	operation, err := s.lifecycleInspector.Operation(ctx, extensionID, operationID)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	if operation.ExtensionID != extensionID || operation.ID != operationID {
		return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
	}
	attempts, err := s.lifecycleInspector.ListStepAttempts(ctx, operationID)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	steps := make([]LifecycleStepSummary, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.OperationID != operationID {
			return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
		}
		steps = append(steps, lifecycleStepSummary(attempt))
	}
	decisions, err := s.lifecycleInspector.ListRecoveryDecisions(ctx, operationID)
	if err != nil {
		return LifecycleOperationDetail{}, err
	}
	recoveries := make([]LifecycleRecoverySummary, 0, len(decisions))
	for _, decision := range decisions {
		if decision.OperationID != operationID {
			return LifecycleOperationDetail{}, ErrLifecycleInvalidInput
		}
		recoveries = append(recoveries, lifecycleRecoverySummary(decision))
	}
	return LifecycleOperationDetail{
		LifecycleOperationSummary: lifecycleOperationSummary(operation), Steps: steps, Recoveries: recoveries,
	}, nil
}

func lifecycleOperationSummary(operation LifecycleOperation) LifecycleOperationSummary {
	return LifecycleOperationSummary{
		ID: operation.ID, ExtensionID: operation.ExtensionID,
		ExtensionVersion: operation.ExtensionVersion, PackageDigest: operation.PackageDigest,
		Operation: operation.Operation, State: operation.State, PlanVersion: operation.PlanVersion,
		RemovalMode: operation.RemovalMode, Forced: operation.Forced,
		AttemptCount: operation.AttemptCount, Revision: operation.Revision,
		CurrentStepID: operation.CurrentStepID, TerminalResult: operation.TerminalResult,
		Closed:            operation.CompletedAt != nil,
		RequestedByUserID: operation.RequestedByUserID, AuditEventID: operation.AuditEventID,
		RecoveryActorUserID: operation.RecoveryActorUserID, RecoveryAuditEventID: operation.RecoveryAuditEventID,
		Error: lifecyclePublicError(operation.Error), CreatedAt: operation.CreatedAt,
		UpdatedAt: operation.UpdatedAt, StartedAt: operation.StartedAt, CompletedAt: operation.CompletedAt,
	}
}

func lifecycleRecoverySummary(decision LifecycleRecoveryDecision) LifecycleRecoverySummary {
	return LifecycleRecoverySummary{
		ID: decision.ID, OperationAttempt: decision.OperationAttempt, Decision: decision.Decision,
		EscalateForced: decision.EscalateForced, Reason: decision.Reason,
		ActorUserID: decision.ActorUserID, AuditEventID: decision.AuditEventID, CreatedAt: decision.CreatedAt,
	}
}

func lifecycleStepSummary(attempt LifecycleStepAttempt) LifecycleStepSummary {
	return LifecycleStepSummary{
		ID: attempt.ID, StepID: attempt.StepID, LifecycleAction: attempt.LifecycleAction,
		PlanVersion: attempt.PlanVersion, Attempt: attempt.Attempt, Status: attempt.Status,
		CompletedUnits: attempt.CompletedUnits, TotalUnits: attempt.TotalUnits,
		SkipReason: attempt.SkipReason,
		Forced:     attempt.Forced, ActorUserID: attempt.ActorUserID, AuditEventID: attempt.AuditEventID,
		Error: lifecyclePublicError(attempt.Error), CreatedAt: attempt.CreatedAt,
		UpdatedAt: attempt.UpdatedAt, StartedAt: attempt.StartedAt, CompletedAt: attempt.CompletedAt,
	}
}

func lifecyclePublicError(value LifecycleExecutionError) LifecyclePublicError {
	if value.Code == "" && value.Reason == "" && value.Message == "" {
		return LifecyclePublicError{}
	}
	code := value.Code
	if _, ok := lifecyclePublicErrorMessages[code]; !ok {
		code = value.Reason
	}
	message, ok := lifecyclePublicErrorMessages[code]
	if !ok {
		code = "lifecycle.action_failed"
		message = lifecyclePublicErrorMessages[code]
	}
	return LifecyclePublicError{
		Code: code, Reason: code, Message: message,
		Retryable: value.Retryable, RetryAfter: value.RetryAfter,
	}
}

var lifecyclePublicErrorMessages = map[string]string{
	"lifecycle.action_failed":           "The lifecycle action failed.",
	"lifecycle.cancelled":               "The lifecycle operation was cancelled.",
	"lifecycle.coordinator_interrupted": "The lifecycle operation was interrupted and can be retried.",
	"lifecycle.deadline_exceeded":       "The lifecycle operation deadline expired.",
	"lifecycle.execution_failed":        "The lifecycle operation failed.",
}
