package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	RecoveryOperationStart    = "recovery.start"
	RecoveryOperationComplete = "recovery.complete"

	RecoveryStartStatusContinue  = "continue"
	RecoveryStartStatusRedirect  = "redirect"
	RecoveryStartStatusChallenge = "challenge"
)

var (
	ErrRecoveryProviderInvalid     = errors.New("identity: recovery provider flow input is invalid")
	ErrRecoveryProviderUnavailable = errors.New("identity: recovery provider flow is unavailable")
	ErrRecoveryProviderNotFound    = errors.New("identity: recovery provider was not found")
)

// RecoveryProviderStartInput 是 Host 拥有的账户恢复启动声明。
type RecoveryProviderStartInput struct {
	ProviderID        string
	CorrelationID     string
	DeviceFingerprint string
	ClientClass       string
	// AccountHint 是用户提供的非权威提示（如邮箱前缀），不是最终身份。
	AccountHint string
}

// RecoveryProviderCompleteInput 是 Host 拥有的恢复完成声明。
type RecoveryProviderCompleteInput struct {
	ProviderID        string
	CorrelationID     string
	CompletionToken   string
	DeviceFingerprint string
	ClientClass       string
}

// RecoveryProviderStartResult 是 recovery.start 的 Host 解析结果。
type RecoveryProviderStartResult struct {
	ProviderID     string
	Status         string
	CorrelationID  string
	ContinueToken  string
	RedirectURL    string
	ChallengeKind  string
	ProviderOutput map[string]any
}

// RecoveryProviderCompleteResult 是 recovery.complete 的 Host 解析结果。
// 插件仅断言恢复流程完成；最终密码重置/会话仍由 Host 拥有。
type RecoveryProviderCompleteResult struct {
	ProviderID     string
	SubjectDigest  string
	UserHintID     int64
	ProviderOutput map[string]any
}

// RecoveryProviderSource 按精确 id 解析活跃 recovery 提供方。
type RecoveryProviderSource interface {
	ResolveRecoveryProvider(ctx context.Context, providerID string) (identityregistry.ProviderContribution, error)
}

// RecoveryProviderInvoker 调用单个 exact recovery 提供方操作。
type RecoveryProviderInvoker interface {
	InvokeExact(
		ctx context.Context,
		provider identityregistry.ProviderContribution,
		operation string,
		actorUserID int64,
		input map[string]any,
		accept func(context.Context, map[string]any, func() error) error,
	) error
}

// RecoveryProviderFlow 是 Host 拥有的外部恢复工作流。始终 actorless。
type RecoveryProviderFlow struct {
	source  RecoveryProviderSource
	invoker RecoveryProviderInvoker
}

func NewRecoveryProviderFlow(
	source RecoveryProviderSource,
	invoker RecoveryProviderInvoker,
) (*RecoveryProviderFlow, error) {
	if source == nil || invoker == nil {
		return nil, ErrRecoveryProviderUnavailable
	}
	return &RecoveryProviderFlow{source: source, invoker: invoker}, nil
}

// Start 调用选定 recovery 提供方的 recovery.start。
func (f *RecoveryProviderFlow) Start(ctx context.Context, input RecoveryProviderStartInput) (RecoveryProviderStartResult, error) {
	if f == nil || f.source == nil || f.invoker == nil {
		return RecoveryProviderStartResult{}, ErrRecoveryProviderUnavailable
	}
	prepared, err := prepareRecoveryStartInput(input)
	if err != nil {
		return RecoveryProviderStartResult{}, err
	}
	provider, err := f.resolveRecoveryProvider(ctx, prepared.ProviderID, RecoveryOperationStart)
	if err != nil {
		return RecoveryProviderStartResult{}, err
	}
	requestInput := map[string]any{
		"correlationId":     prepared.CorrelationID,
		"deviceFingerprint": prepared.DeviceFingerprint,
		"clientClass":       prepared.ClientClass,
	}
	if prepared.AccountHint != "" {
		requestInput["accountHint"] = prepared.AccountHint
	}
	var result RecoveryProviderStartResult
	err = f.invoker.InvokeExact(
		ctx, provider, RecoveryOperationStart, 0, requestInput,
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseAuthStartOutput(output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			result = RecoveryProviderStartResult{
				ProviderID:     provider.ID,
				Status:         parsed.status,
				CorrelationID:  prepared.CorrelationID,
				ContinueToken:  parsed.continueToken,
				RedirectURL:    parsed.redirectURL,
				ChallengeKind:  parsed.challengeKind,
				ProviderOutput: cloneAuthDocument(output),
			}
			return nil
		},
	)
	if err != nil {
		return RecoveryProviderStartResult{}, mapRecoveryProviderInvokeError(err)
	}
	if result.ProviderID == "" {
		return RecoveryProviderStartResult{}, ErrRecoveryProviderUnavailable
	}
	return result, nil
}

// Complete 调用选定 recovery 提供方的 recovery.complete。
func (f *RecoveryProviderFlow) Complete(ctx context.Context, input RecoveryProviderCompleteInput) (RecoveryProviderCompleteResult, error) {
	if f == nil || f.source == nil || f.invoker == nil {
		return RecoveryProviderCompleteResult{}, ErrRecoveryProviderUnavailable
	}
	prepared, err := prepareRecoveryCompleteInput(input)
	if err != nil {
		return RecoveryProviderCompleteResult{}, err
	}
	provider, err := f.resolveRecoveryProvider(ctx, prepared.ProviderID, RecoveryOperationComplete)
	if err != nil {
		return RecoveryProviderCompleteResult{}, err
	}
	requestInput := map[string]any{
		"correlationId":     prepared.CorrelationID,
		"completionToken":   prepared.CompletionToken,
		"deviceFingerprint": prepared.DeviceFingerprint,
		"clientClass":       prepared.ClientClass,
	}
	var result RecoveryProviderCompleteResult
	err = f.invoker.InvokeExact(
		ctx, provider, RecoveryOperationComplete, 0, requestInput,
		func(_ context.Context, output map[string]any, fence func() error) error {
			if output == nil {
				return ErrRecoveryProviderUnavailable
			}
			digest := strings.ToLower(strings.TrimSpace(stringFromAuthOutput(output, "providerSubjectDigest")))
			if digest == "" {
				digest = strings.ToLower(strings.TrimSpace(stringFromAuthOutput(output, "subjectDigest")))
			}
			if digest != "" && !isAuthSubjectDigest(digest) {
				return ErrRecoveryProviderUnavailable
			}
			var userHintID int64
			switch raw := output["userHintId"].(type) {
			case float64:
				userHintID = int64(raw)
			case int64:
				userHintID = raw
			case int:
				userHintID = int64(raw)
			case nil:
			default:
				return ErrRecoveryProviderUnavailable
			}
			if userHintID < 0 {
				return ErrRecoveryProviderUnavailable
			}
			// 恢复完成必须至少给出主体摘要或用户提示之一。
			if digest == "" && userHintID == 0 {
				return ErrRecoveryProviderUnavailable
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			result = RecoveryProviderCompleteResult{
				ProviderID:     provider.ID,
				SubjectDigest:  digest,
				UserHintID:     userHintID,
				ProviderOutput: cloneAuthDocument(output),
			}
			return nil
		},
	)
	if err != nil {
		return RecoveryProviderCompleteResult{}, mapRecoveryProviderInvokeError(err)
	}
	if result.ProviderID == "" {
		return RecoveryProviderCompleteResult{}, ErrRecoveryProviderUnavailable
	}
	return result, nil
}

func (f *RecoveryProviderFlow) resolveRecoveryProvider(
	ctx context.Context,
	providerID, operation string,
) (identityregistry.ProviderContribution, error) {
	provider, err := f.source.ResolveRecoveryProvider(ctx, providerID)
	if err != nil {
		if errors.Is(err, identityregistry.ErrNotFound) {
			return identityregistry.ProviderContribution{}, ErrRecoveryProviderNotFound
		}
		return identityregistry.ProviderContribution{}, ErrRecoveryProviderUnavailable
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindRecovery {
		return identityregistry.ProviderContribution{}, ErrRecoveryProviderNotFound
	}
	if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
		return identityregistry.ProviderContribution{}, ErrRecoveryProviderUnavailable
	}
	if !authProviderHasOperation(provider, operation) {
		return identityregistry.ProviderContribution{}, ErrRecoveryProviderUnavailable
	}
	return provider, nil
}

func prepareRecoveryStartInput(input RecoveryProviderStartInput) (RecoveryProviderStartInput, error) {
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	if input.ProviderID == "" {
		return RecoveryProviderStartInput{}, ErrRecoveryProviderInvalid
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if input.CorrelationID == "" || len(input.CorrelationID) > 200 {
		return RecoveryProviderStartInput{}, ErrRecoveryProviderInvalid
	}
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	input.AccountHint = strings.TrimSpace(input.AccountHint)
	if len(input.AccountHint) > 320 {
		return RecoveryProviderStartInput{}, ErrRecoveryProviderInvalid
	}
	return input, nil
}

func prepareRecoveryCompleteInput(input RecoveryProviderCompleteInput) (RecoveryProviderCompleteInput, error) {
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	if input.ProviderID == "" {
		return RecoveryProviderCompleteInput{}, ErrRecoveryProviderInvalid
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	input.CompletionToken = strings.TrimSpace(input.CompletionToken)
	if input.CorrelationID == "" || len(input.CorrelationID) > 200 {
		return RecoveryProviderCompleteInput{}, ErrRecoveryProviderInvalid
	}
	if input.CompletionToken == "" || len(input.CompletionToken) > 4000 {
		return RecoveryProviderCompleteInput{}, ErrRecoveryProviderInvalid
	}
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	return input, nil
}

func mapRecoveryProviderInvokeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRecoveryProviderInvalid) ||
		errors.Is(err, ErrRecoveryProviderUnavailable) ||
		errors.Is(err, ErrRecoveryProviderNotFound) ||
		errors.Is(err, ErrAuthProviderFlowUnavailable) {
		// parseAuthStartOutput 复用了 auth 错误；映射为 recovery 不可用。
		if errors.Is(err, ErrAuthProviderFlowUnavailable) {
			return ErrRecoveryProviderUnavailable
		}
		return err
	}
	return fmt.Errorf("%w: %v", ErrRecoveryProviderUnavailable, err)
}

// RegistryRecoveryProviderSource 从 Identity Registry 解析精确 recovery 提供方。
type RegistryRecoveryProviderSource struct {
	Registry *identityregistry.Registry
}

func (s RegistryRecoveryProviderSource) ResolveRecoveryProvider(_ context.Context, providerID string) (identityregistry.ProviderContribution, error) {
	if s.Registry == nil {
		return identityregistry.ProviderContribution{}, ErrRecoveryProviderUnavailable
	}
	provider, err := s.Registry.ResolveProvider(providerID)
	if err != nil {
		return identityregistry.ProviderContribution{}, err
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindRecovery {
		return identityregistry.ProviderContribution{}, identityregistry.ErrNotFound
	}
	return provider, nil
}
