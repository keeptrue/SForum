package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var (
	ErrInvalidJobDefinition = errors.New("invalid plugin job definition")

	jobIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	jobNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	jobSchemaPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

// JobDefinition 对齐可执行 ManifestJob + ExecuteJob。
// Name 对应 ManifestJob.name / JobRequest.job_kind。
// ContractVersion 是声明契约 id@positiveVersion；payload_version 是 schema 数字版本，二者不同。
// Cancel/Watch 仍是 wire-only（Host 当前返回 unavailable）。
type JobDefinition struct {
	ID                string
	ContractVersion   string
	Name              string
	Handler           string
	PayloadSchema     string
	RetryPolicy       string
	MaxAttempts       int
	RetryDelaySeconds int
	ConcurrencyLimit  int
	// Execute 是作者侧业务 handler（非 Manifest 字段）。
	Execute JobHandler
}

// JobCall 是校验后的 ExecuteJob 输入。
type JobCall struct {
	Context        *protocolwire.RequestContext
	JobID          string
	Kind           string
	PayloadVersion string
	Attempt        uint32
	Checkpoint     string
	Payload        *protocolwire.TypedDocument
	Progress       *ProgressStream
}

type JobHandler func(context.Context, *JobCall) error

type registeredJob struct {
	definition JobDefinition
}

// JobRegistry 按 job_kind（ManifestJob.name）分发 ExecuteJob progress stream。
type JobRegistry struct {
	byKind map[string]registeredJob
	order  []JobDefinition
}

func NewJobRegistry(definitions ...JobDefinition) (*JobRegistry, error) {
	registry := &JobRegistry{byKind: make(map[string]registeredJob, len(definitions))}
	seenIDs := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		prepared, err := prepareJobDefinition(definition)
		if err != nil {
			return nil, err
		}
		if _, exists := seenIDs[prepared.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate job id %q", ErrInvalidJobDefinition, prepared.ID)
		}
		seenIDs[prepared.ID] = struct{}{}
		if _, exists := registry.byKind[prepared.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate job kind %q", ErrInvalidJobDefinition, prepared.Name)
		}
		registry.byKind[prepared.Name] = registeredJob{definition: prepared}
		registry.order = append(registry.order, prepared)
	}
	sort.Slice(registry.order, func(i, j int) bool { return registry.order[i].Name < registry.order[j].Name })
	return registry, nil
}

func (r *JobRegistry) Definitions() []JobDefinition {
	if r == nil {
		return nil
	}
	out := make([]JobDefinition, len(r.order))
	for i, item := range r.order {
		copyDef := item
		copyDef.Execute = nil
		out[i] = copyDef
	}
	return out
}

// StreamHandler 返回可挂到 RuntimeStreams.Job / Server.WithJobRegistry 的分发器。
func (r *JobRegistry) StreamHandler() JobStreamHandler {
	return func(ctx context.Context, request *pluginwire.JobRequest, progress *ProgressStream) error {
		if r == nil {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
				"job.registry_unavailable", "Plugin job registry is unavailable.")
		}
		if request == nil {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"job.request_required", "A job request is required.")
		}
		if detail := validateFamilyRequestContext(request.GetContext(), "job"); detail != nil {
			return runtimeStreamErrorFromDetail(detail)
		}
		kind := strings.TrimSpace(request.GetJobKind())
		registered, ok := r.byKind[kind]
		if !ok {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
				"job.not_found", "The requested job kind is not registered.")
		}
		definition := registered.definition
		schemaID, schemaVersion, ok := SplitSchemaRef(definition.PayloadSchema)
		if !ok {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
				"job.payload_schema_invalid", "Registered job payload schema is invalid.")
		}
		// payload_version 是 schema 数字版本（非 contractVersion）。
		payloadVersion := strings.TrimSpace(request.GetPayloadVersion())
		if payloadVersion == "" {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"job.payload_version_required", "Job payload version is required.")
		}
		if payloadVersion != schemaVersion {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"job.payload_version_mismatch", "Job payload version does not match the registered declaration.")
		}
		if err := validateBoundDocument(request.GetPayload(), definition.PayloadSchema, "job", "payload"); err != nil {
			return jobStreamErrorFrom(err)
		}
		if request.GetPayload() != nil && request.GetPayload().GetSchemaId() != schemaID {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"job.payload_schema_mismatch", "Job payload schema id does not match the registered declaration.")
		}
		if request.GetPayload() != nil && request.GetPayload().GetSchemaVersion() != payloadVersion {
			return jobStreamError(protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
				"job.payload_version_mismatch", "Job payload versions in the request and typed document must match.")
		}
		handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
		defer cancel()
		err := definition.Execute(handlerCtx, &JobCall{
			Context: cloneRequestContext(request.GetContext()),
			JobID:   request.GetJobId(), Kind: definition.Name, PayloadVersion: payloadVersion,
			Attempt: request.GetAttempt(), Checkpoint: request.GetCheckpoint(),
			Payload: cloneTypedDocument(request.GetPayload()), Progress: progress,
		})
		return jobStreamErrorFrom(err)
	}
}

func jobStreamError(code protocolwire.ErrorCode, reason, message string) *RuntimeStreamError {
	return &RuntimeStreamError{Code: code, Reason: reason, Message: message}
}

func jobStreamErrorFrom(err error) error {
	var familyErr *FamilyError
	if errors.As(err, &familyErr) && familyErr != nil {
		return &RuntimeStreamError{
			Code: familyErr.Code, Reason: familyErr.Reason, Message: familyErr.Message,
			Retryable: familyErr.Retryable, RetryAfter: familyErr.RetryAfter, Metadata: familyErr.Metadata,
		}
	}
	return err
}

func prepareJobDefinition(definition JobDefinition) (JobDefinition, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.ContractVersion = strings.TrimSpace(definition.ContractVersion)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Handler = strings.TrimSpace(definition.Handler)
	definition.PayloadSchema = strings.TrimSpace(definition.PayloadSchema)
	definition.RetryPolicy = strings.ToLower(strings.TrimSpace(definition.RetryPolicy))
	if !jobNamePattern.MatchString(definition.Name) {
		return JobDefinition{}, fmt.Errorf("%w: invalid job kind %q", ErrInvalidJobDefinition, definition.Name)
	}
	if !jobIDPattern.MatchString(definition.ID) {
		return JobDefinition{}, fmt.Errorf("%w: invalid job id %q", ErrInvalidJobDefinition, definition.ID)
	}
	if !validContractVersion(definition.ContractVersion) {
		return JobDefinition{}, fmt.Errorf("%w: job %q contract version must be id@positiveVersion", ErrInvalidJobDefinition, definition.Name)
	}
	if !validManifestHandler(definition.Handler) {
		return JobDefinition{}, fmt.Errorf("%w: job %q requires executable handler", ErrInvalidJobDefinition, definition.Name)
	}
	if !jobSchemaPattern.MatchString(definition.PayloadSchema) {
		return JobDefinition{}, fmt.Errorf("%w: job %q requires versioned payload schema", ErrInvalidJobDefinition, definition.Name)
	}
	switch definition.RetryPolicy {
	case "none", "bounded", "exponential":
	case "":
		return JobDefinition{}, fmt.Errorf("%w: job %q requires retry policy", ErrInvalidJobDefinition, definition.Name)
	default:
		return JobDefinition{}, fmt.Errorf("%w: job %q has invalid retry policy %q", ErrInvalidJobDefinition, definition.Name, definition.RetryPolicy)
	}
	// Manifest V3 normalize 默认值。
	if definition.ConcurrencyLimit == 0 {
		definition.ConcurrencyLimit = extensionmanifest.PluginJobDefaultConcurrencyLimit
	}
	if definition.MaxAttempts == 0 {
		switch definition.RetryPolicy {
		case "none":
			definition.MaxAttempts = 1
		case "bounded":
			definition.MaxAttempts = extensionmanifest.PluginJobDefaultBoundedAttempts
		case "exponential":
			definition.MaxAttempts = extensionmanifest.PluginJobDefaultExponentialAttempts
		}
	}
	if definition.RetryPolicy == "bounded" && definition.RetryDelaySeconds == 0 {
		definition.RetryDelaySeconds = extensionmanifest.PluginJobDefaultRetryDelaySeconds
	}
	if definition.MaxAttempts < 1 || definition.MaxAttempts > extensionmanifest.PluginJobMaximumAttempts {
		return JobDefinition{}, fmt.Errorf("%w: job %q maxAttempts must be 1..%d",
			ErrInvalidJobDefinition, definition.Name, extensionmanifest.PluginJobMaximumAttempts)
	}
	if definition.ConcurrencyLimit < 1 || definition.ConcurrencyLimit > extensionmanifest.PluginJobMaximumConcurrencyLimit {
		return JobDefinition{}, fmt.Errorf("%w: job %q concurrencyLimit must be 1..%d",
			ErrInvalidJobDefinition, definition.Name, extensionmanifest.PluginJobMaximumConcurrencyLimit)
	}
	switch definition.RetryPolicy {
	case "none", "exponential":
		if definition.RetryDelaySeconds != 0 {
			return JobDefinition{}, fmt.Errorf("%w: job %q retryDelaySeconds must be 0 for %s",
				ErrInvalidJobDefinition, definition.Name, definition.RetryPolicy)
		}
	case "bounded":
		if definition.RetryDelaySeconds < 1 || definition.RetryDelaySeconds > extensionmanifest.PluginJobMaximumRetryDelaySeconds {
			return JobDefinition{}, fmt.Errorf("%w: job %q retryDelaySeconds must be 1..%d",
				ErrInvalidJobDefinition, definition.Name, extensionmanifest.PluginJobMaximumRetryDelaySeconds)
		}
	}
	if definition.Execute == nil {
		return JobDefinition{}, fmt.Errorf("%w: job %q has no execute handler", ErrInvalidJobDefinition, definition.Name)
	}
	return definition, nil
}
