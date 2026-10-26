package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

var (
	ErrRouteRuntimeIncidentInvalid  = errors.New("route runtime incident is invalid")
	ErrRouteRuntimeIncidentNotFound = errors.New("route runtime incident was not found")
	ErrRouteRuntimeIncidentConflict = errors.New("route runtime incident conflicts with durable evidence")
)

type RouteRuntimeIncidentLocalResult string

const (
	RouteRuntimeIncidentPending       RouteRuntimeIncidentLocalResult = "pending"
	RouteRuntimeIncidentQuarantined   RouteRuntimeIncidentLocalResult = "quarantined"
	RouteRuntimeIncidentStaleMissing  RouteRuntimeIncidentLocalResult = "stale_missing"
	RouteRuntimeIncidentStaleArtifact RouteRuntimeIncidentLocalResult = "stale_artifact"
	RouteRuntimeIncidentFailed        RouteRuntimeIncidentLocalResult = "failed"
)

type RouteRuntimeIncidentEvidence struct {
	IncidentKey              string
	RouteRevision            uint64
	StepIndex                int
	Phase                    routes.RouteExecutionPhase
	InvocationStage          routes.InvocationStage
	Action                   string
	Mode                     string
	RouteID                  string
	ContractVersion          string
	Method                   string
	PathSignature            string
	FailureCode              routes.RouteFailureCode
	CauseClass               string
	RuntimeExecutionObserved bool
	ActorID                  int64
	ResponseStatus           int
	CommitState              routes.RouteExecutionCommitState
	Artifact                 routes.PluginArtifact
}

type RouteRuntimeIncidentRecord struct {
	ID                 int64
	Evidence           RouteRuntimeIncidentEvidence
	ExtensionVersionID int64
	AuditEventID       int64
	LocalResult        RouteRuntimeIncidentLocalResult
	CreatedAt          time.Time
	ResolvedAt         *time.Time
}

type RouteRuntimeIncidentStore interface {
	CreatePending(context.Context, RouteRuntimeIncidentEvidence) (RouteRuntimeIncidentRecord, bool, error)
	Resolve(context.Context, string, RouteRuntimeIncidentLocalResult) (RouteRuntimeIncidentRecord, error)
}

type PostgresRouteRuntimeIncidentStore struct{ pool *pgxpool.Pool }

func NewPostgresRouteRuntimeIncidentStore(pool *pgxpool.Pool) *PostgresRouteRuntimeIncidentStore {
	return &PostgresRouteRuntimeIncidentStore{pool: pool}
}

var routeRuntimeIncidentHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	routeRuntimeIncidentContract = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	routeRuntimeIncidentMethod   = regexp.MustCompile("^[A-Z][A-Z0-9!#$%&'*+.^_`|~-]{0,31}$")
)

func NewRouteRuntimeIncidentKey() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate route runtime incident key: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *PostgresRouteRuntimeIncidentStore) CreatePending(
	ctx context.Context,
	evidence RouteRuntimeIncidentEvidence,
) (record RouteRuntimeIncidentRecord, created bool, err error) {
	if s == nil || s.pool == nil || ctx == nil || !validRouteRuntimeIncidentEvidence(evidence) {
		return record, false, ErrRouteRuntimeIncidentInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return record, false, fmt.Errorf("begin route runtime incident: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended('routes.runtime_incident:' || $1, 0)
	)`, evidence.IncidentKey); err != nil {
		return record, false, fmt.Errorf("lock route runtime incident: %w", err)
	}
	existing, err := scanRouteRuntimeIncident(tx.QueryRow(ctx, routeRuntimeIncidentSelectByKey, evidence.IncidentKey))
	if err == nil {
		if existing.Evidence != evidence {
			return record, false, ErrRouteRuntimeIncidentConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return record, false, fmt.Errorf("commit replayed route runtime incident: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return record, false, fmt.Errorf("read route runtime incident: %w", err)
	}

	var extensionVersionID int64
	if err := tx.QueryRow(ctx, `
		SELECT version.id
		FROM extension_versions AS version
		JOIN extensions AS extension
		  ON extension.id = version.extension_id AND extension.type = 'plugin'
		WHERE version.extension_id = $1
		  AND version.version = $2
		  AND version.package_digest = $3
		FOR KEY SHARE OF extension, version
	`, evidence.Artifact.ExtensionID, evidence.Artifact.ExtensionVersion, evidence.Artifact.PackageDigest).Scan(&extensionVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return record, false, fmt.Errorf("%w: exact extension version", ErrRouteRuntimeIncidentInvalid)
		}
		return record, false, fmt.Errorf("resolve route runtime incident artifact: %w", err)
	}
	metadata, err := routeRuntimeIncidentAuditMetadata(evidence)
	if err != nil {
		return record, false, err
	}
	var actor any
	if evidence.ActorID > 0 {
		actor = evidence.ActorID
	}
	var auditEventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, $2, $3::jsonb)
		RETURNING id
	`, actor, audit.ActionRouteRuntimeIncident, metadata).Scan(&auditEventID); err != nil {
		return record, false, fmt.Errorf("insert route runtime incident audit: %w", err)
	}
	var responseStatus any
	if evidence.ResponseStatus > 0 {
		responseStatus = evidence.ResponseStatus
	}
	var id int64
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO extension_route_runtime_incidents (
		  incident_key, route_revision, step_index, phase, invocation_stage,
		  action, mode, route_id, contract_version, method, path_signature,
		  failure_code, cause_class, runtime_execution_observed, actor_user_id,
		  response_status, commit_state, extension_id, extension_version_id,
		  extension_version, package_digest, runtime_instance_id, audit_event_id
		) VALUES (
		  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
		  $18,$19,$20,$21,$22,$23
		)
		RETURNING id, created_at
	`,
		evidence.IncidentKey, evidence.RouteRevision, evidence.StepIndex,
		evidence.Phase, evidence.InvocationStage, evidence.Action, evidence.Mode,
		evidence.RouteID, evidence.ContractVersion, evidence.Method, evidence.PathSignature,
		evidence.FailureCode, evidence.CauseClass, evidence.RuntimeExecutionObserved,
		actor, responseStatus, evidence.CommitState, evidence.Artifact.ExtensionID,
		extensionVersionID, evidence.Artifact.ExtensionVersion, evidence.Artifact.PackageDigest,
		evidence.Artifact.RuntimeInstanceID, auditEventID,
	).Scan(&id, &createdAt); err != nil {
		return record, false, fmt.Errorf("insert route runtime incident: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return record, false, fmt.Errorf("commit route runtime incident: %w", err)
	}
	return RouteRuntimeIncidentRecord{
		ID: id, Evidence: evidence, ExtensionVersionID: extensionVersionID,
		AuditEventID: auditEventID, LocalResult: RouteRuntimeIncidentPending,
		CreatedAt: createdAt,
	}, true, nil
}

func (s *PostgresRouteRuntimeIncidentStore) Resolve(
	ctx context.Context,
	incidentKey string,
	result RouteRuntimeIncidentLocalResult,
) (RouteRuntimeIncidentRecord, error) {
	if s == nil || s.pool == nil || ctx == nil || !routeRuntimeIncidentHex.MatchString(incidentKey) ||
		!validRouteRuntimeIncidentFinalResult(result) {
		return RouteRuntimeIncidentRecord{}, ErrRouteRuntimeIncidentInvalid
	}
	record, err := scanRouteRuntimeIncident(s.pool.QueryRow(ctx, `
		UPDATE extension_route_runtime_incidents
		SET local_quarantine_result = $2, resolved_at = statement_timestamp()
		WHERE incident_key = $1 AND local_quarantine_result = 'pending'
		RETURNING `+routeRuntimeIncidentColumns, incidentKey, result))
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RouteRuntimeIncidentRecord{}, fmt.Errorf("resolve route runtime incident: %w", err)
	}
	record, err = scanRouteRuntimeIncident(s.pool.QueryRow(ctx, routeRuntimeIncidentSelectByKey, incidentKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return RouteRuntimeIncidentRecord{}, ErrRouteRuntimeIncidentNotFound
	}
	if err != nil {
		return RouteRuntimeIncidentRecord{}, fmt.Errorf("read resolved route runtime incident: %w", err)
	}
	if record.LocalResult != result {
		return RouteRuntimeIncidentRecord{}, ErrRouteRuntimeIncidentConflict
	}
	return record, nil
}

const routeRuntimeIncidentColumns = `
	id, incident_key, route_revision, step_index, phase, invocation_stage,
	action, mode, route_id, contract_version, method, path_signature,
	failure_code, cause_class, runtime_execution_observed,
	COALESCE(actor_user_id, 0), COALESCE(response_status, 0), commit_state,
	extension_id, extension_version_id, extension_version, package_digest,
	runtime_instance_id, audit_event_id, local_quarantine_result, created_at,
	COALESCE(resolved_at, created_at), resolved_at IS NOT NULL
`

const routeRuntimeIncidentSelectByKey = `
	SELECT ` + routeRuntimeIncidentColumns + `
	FROM extension_route_runtime_incidents
	WHERE incident_key = $1
`

type routeRuntimeIncidentRow interface{ Scan(...any) error }

func scanRouteRuntimeIncident(row routeRuntimeIncidentRow) (RouteRuntimeIncidentRecord, error) {
	var (
		record                            RouteRuntimeIncidentRecord
		revision                          int64
		phase, stage, failureCode, commit string
		resolvedAt                        time.Time
		hasResolved                       bool
	)
	err := row.Scan(
		&record.ID, &record.Evidence.IncidentKey, &revision, &record.Evidence.StepIndex,
		&phase, &stage, &record.Evidence.Action, &record.Evidence.Mode,
		&record.Evidence.RouteID, &record.Evidence.ContractVersion, &record.Evidence.Method,
		&record.Evidence.PathSignature, &failureCode, &record.Evidence.CauseClass,
		&record.Evidence.RuntimeExecutionObserved, &record.Evidence.ActorID,
		&record.Evidence.ResponseStatus, &commit, &record.Evidence.Artifact.ExtensionID,
		&record.ExtensionVersionID, &record.Evidence.Artifact.ExtensionVersion,
		&record.Evidence.Artifact.PackageDigest, &record.Evidence.Artifact.RuntimeInstanceID,
		&record.AuditEventID, &record.LocalResult, &record.CreatedAt, &resolvedAt, &hasResolved,
	)
	if err != nil {
		return RouteRuntimeIncidentRecord{}, err
	}
	if revision <= 0 {
		return RouteRuntimeIncidentRecord{}, ErrRouteRuntimeIncidentInvalid
	}
	record.Evidence.RouteRevision = uint64(revision)
	record.Evidence.Phase = routes.RouteExecutionPhase(phase)
	record.Evidence.InvocationStage = routes.InvocationStage(stage)
	record.Evidence.FailureCode = routes.RouteFailureCode(failureCode)
	record.Evidence.CommitState = routes.RouteExecutionCommitState(commit)
	if hasResolved {
		record.ResolvedAt = &resolvedAt
	}
	return record, nil
}

func validRouteRuntimeIncidentEvidence(value RouteRuntimeIncidentEvidence) bool {
	if !routeRuntimeIncidentHex.MatchString(value.IncidentKey) || value.RouteRevision == 0 ||
		value.RouteRevision > math.MaxInt64 || value.StepIndex < 0 || value.StepIndex > math.MaxInt32 ||
		!value.RuntimeExecutionObserved ||
		value.ActorID < 0 || value.ResponseStatus < 0 || value.ResponseStatus > 599 ||
		(value.ResponseStatus > 0 && value.ResponseStatus < 100) ||
		!validTrimmedRouteRuntimeIncidentText(value.RouteID, 200) ||
		!routeRuntimeIncidentContract.MatchString(value.ContractVersion) ||
		!routeRuntimeIncidentMethod.MatchString(value.Method) ||
		len(value.PathSignature) == 0 || len(value.PathSignature) > 1024 ||
		!validTrimmedRouteRuntimeIncidentText(value.Artifact.ExtensionID, 200) ||
		!validTrimmedRouteRuntimeIncidentText(value.Artifact.ExtensionVersion, 100) ||
		!routeRuntimeIncidentHex.MatchString(value.Artifact.PackageDigest) ||
		!validTrimmedRouteRuntimeIncidentText(value.Artifact.RuntimeInstanceID, 200) {
		return false
	}
	if value.InvocationStage == routes.InvocationStageHandler {
		return routes.ValidRouteStreamFailure(routes.RouteStreamFailure{
			Revision: value.RouteRevision, StepIndex: value.StepIndex, Phase: value.Phase,
			InvocationStage: value.InvocationStage, Action: value.Action, Mode: value.Mode,
			RouteID: value.RouteID, ContractVersion: value.ContractVersion,
			Method: value.Method, PathSignature: value.PathSignature,
			FailureCode: value.FailureCode, CauseClass: routes.RouteStreamFailureClass(value.CauseClass),
			RuntimeExecutionObserved: value.RuntimeExecutionObserved, ActorID: value.ActorID,
			ResponseStatus: value.ResponseStatus, CommitState: value.CommitState,
			Artifact: value.Artifact,
		})
	}
	if value.InvocationStage != routes.InvocationStageResponse || value.Mode != "http" ||
		value.ResponseStatus == 0 || value.CommitState != routes.RouteCommitFinal ||
		!routes.ValidInvocationStageForStep(value.Phase, value.Action, value.InvocationStage) {
		return false
	}
	return value.FailureCode == routes.RouteFailureTransportFailed && value.CauseClass == "runtime_transport" ||
		value.FailureCode == routes.RouteFailureResponseSchemaRejected && value.CauseClass == "response_schema"
}

func validTrimmedRouteRuntimeIncidentText(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxBytes
}

func validRouteRuntimeIncidentFinalResult(result RouteRuntimeIncidentLocalResult) bool {
	switch result {
	case RouteRuntimeIncidentQuarantined, RouteRuntimeIncidentStaleMissing,
		RouteRuntimeIncidentStaleArtifact, RouteRuntimeIncidentFailed:
		return true
	default:
		return false
	}
}

func routeRuntimeIncidentAuditMetadata(evidence RouteRuntimeIncidentEvidence) (string, error) {
	var responseStatus any
	if evidence.ResponseStatus > 0 {
		responseStatus = evidence.ResponseStatus
	}
	metadata, err := json.Marshal(map[string]any{
		"incidentKey": evidence.IncidentKey, "revision": evidence.RouteRevision,
		"stepIndex": evidence.StepIndex, "phase": evidence.Phase,
		"invocationStage": evidence.InvocationStage, "action": evidence.Action, "mode": evidence.Mode,
		"routeId": evidence.RouteID, "contractVersion": evidence.ContractVersion,
		"method": evidence.Method, "pathSignature": evidence.PathSignature,
		"failureCode": evidence.FailureCode, "causeClass": evidence.CauseClass,
		"runtimeExecutionObserved": evidence.RuntimeExecutionObserved,
		"responseStatus":           responseStatus, "commitState": evidence.CommitState,
		"extensionId":       evidence.Artifact.ExtensionID,
		"extensionVersion":  evidence.Artifact.ExtensionVersion,
		"packageDigest":     evidence.Artifact.PackageDigest,
		"runtimeInstanceId": evidence.Artifact.RuntimeInstanceID,
	})
	if err != nil {
		return "", fmt.Errorf("encode route runtime incident audit: %w", err)
	}
	return string(metadata), nil
}

var _ RouteRuntimeIncidentStore = (*PostgresRouteRuntimeIncidentStore)(nil)
