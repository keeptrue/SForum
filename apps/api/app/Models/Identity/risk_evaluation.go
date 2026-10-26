package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	riskEvaluateOperation = "risk.evaluate"

	RiskDispositionAllow  = SessionPolicyDispositionAllow
	RiskDispositionDeny   = SessionPolicyDispositionDeny
	RiskDispositionStepUp = SessionPolicyDispositionStepUp
)

var (
	ErrRiskEvaluationInvalid     = errors.New("identity: risk evaluation input is invalid")
	ErrRiskEvaluationDenied      = errors.New("identity: risk evaluation denied the request")
	ErrRiskEvaluationStepUp      = errors.New("identity: risk evaluation requires step-up verification")
	ErrRiskEvaluationUnavailable = errors.New("identity: risk evaluation is unavailable")
)

// RiskEvaluationInput 是 Host 拥有的风险评估工作流声明。
// 不包含密码、cookie、session id 或 CSRF。
type RiskEvaluationInput struct {
	UserID            int64
	Purpose           string // login | register | recovery
	CorrelationID     string
	DeviceFingerprint string
	ClientClass       string // 粗粒度客户端类，非原始 UA
}

// RiskEvaluationResult 是所有活跃 risk 提供方的合成处置。
type RiskEvaluationResult struct {
	Disposition string
	// ProviderIDs 记录按执行顺序参与合成的提供方。
	ProviderIDs []string
	// DenyingProvider 在最终处置为 deny/step_up 时指向主导提供方。
	DenyingProvider string
}

// RiskProviderSource 返回确定性排序后的活跃 risk 提供方快照。
type RiskProviderSource interface {
	RiskProviders(ctx context.Context) ([]identityregistry.ProviderContribution, error)
}

// RiskEvaluateInvoker 调用单个 exact risk 提供方。
// 复用 session 评估的 accept 形状：output + commit fence。
type RiskEvaluateInvoker interface {
	InvokeExact(
		ctx context.Context,
		provider identityregistry.ProviderContribution,
		operation string,
		actorUserID int64,
		input map[string]any,
		accept func(context.Context, map[string]any, func() error) error,
	) error
}

// RiskEvaluator 以确定性优先级/id 顺序组合全部活跃 risk 提供方。
// deny 与 step_up 压过 allow；任一提供方失败关闭。
type RiskEvaluator struct {
	source  RiskProviderSource
	invoker RiskEvaluateInvoker
}

func NewRiskEvaluator(source RiskProviderSource, invoker RiskEvaluateInvoker) (*RiskEvaluator, error) {
	if source == nil {
		return nil, ErrRiskEvaluationUnavailable
	}
	return &RiskEvaluator{source: source, invoker: invoker}, nil
}

// Evaluate 组合全部活跃 risk 提供方。无提供方时返回 allow（Core 默认路径）。
func (e *RiskEvaluator) Evaluate(ctx context.Context, input RiskEvaluationInput) (RiskEvaluationResult, error) {
	if e == nil || e.source == nil {
		return RiskEvaluationResult{}, ErrRiskEvaluationUnavailable
	}
	prepared, err := prepareRiskEvaluationInput(input)
	if err != nil {
		return RiskEvaluationResult{}, err
	}
	providers, err := e.source.RiskProviders(ctx)
	if err != nil {
		return RiskEvaluationResult{}, ErrRiskEvaluationUnavailable
	}
	if len(providers) == 0 {
		return RiskEvaluationResult{Disposition: RiskDispositionAllow}, nil
	}
	if e.invoker == nil {
		return RiskEvaluationResult{}, ErrRiskEvaluationUnavailable
	}

	result := RiskEvaluationResult{Disposition: RiskDispositionAllow}
	requestInput := map[string]any{
		"userId":            prepared.UserID,
		"purpose":           prepared.Purpose,
		"correlationId":     prepared.CorrelationID,
		"deviceFingerprint": prepared.DeviceFingerprint,
		"clientClass":       prepared.ClientClass,
	}
	for _, provider := range providers {
		if !riskProviderHasEvaluate(provider) {
			return RiskEvaluationResult{}, ErrRiskEvaluationUnavailable
		}
		result.ProviderIDs = append(result.ProviderIDs, provider.ID)
		disposition, invokeErr := e.invokeOne(ctx, provider, prepared.UserID, requestInput)
		if invokeErr != nil {
			return RiskEvaluationResult{}, invokeErr
		}
		result.Disposition = composeRiskDisposition(result.Disposition, disposition)
		if disposition == RiskDispositionDeny || disposition == RiskDispositionStepUp {
			result.DenyingProvider = provider.ID
		}
		// deny 已主导，仍继续调用其余提供方以保持审计顺序与 fail-closed 语义：
		// 后续提供方失败必须关闭整条评估。
	}
	return result, nil
}

// RequireAllow 在评估后仅允许 allow 处置通过。
func (e *RiskEvaluator) RequireAllow(ctx context.Context, input RiskEvaluationInput) (RiskEvaluationResult, error) {
	result, err := e.Evaluate(ctx, input)
	if err != nil {
		return RiskEvaluationResult{}, err
	}
	return result, requireRiskAllow(result.Disposition)
}

func (e *RiskEvaluator) invokeOne(
	ctx context.Context,
	provider identityregistry.ProviderContribution,
	actorUserID int64,
	input map[string]any,
) (string, error) {
	var disposition string
	err := e.invoker.InvokeExact(
		ctx, provider, riskEvaluateOperation, actorUserID, input,
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseRiskDisposition(output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			disposition = parsed
			return nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRiskEvaluationUnavailable, err)
	}
	if disposition == "" {
		return "", ErrRiskEvaluationUnavailable
	}
	return disposition, nil
}

func prepareRiskEvaluationInput(input RiskEvaluationInput) (RiskEvaluationInput, error) {
	input.Purpose = strings.ToLower(strings.TrimSpace(input.Purpose))
	switch input.Purpose {
	case "login", "register", "recovery":
	default:
		return RiskEvaluationInput{}, ErrRiskEvaluationInvalid
	}
	if input.UserID <= 0 {
		return RiskEvaluationInput{}, ErrRiskEvaluationInvalid
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	return input, nil
}

func riskProviderHasEvaluate(provider identityregistry.ProviderContribution) bool {
	for _, operation := range provider.Operations {
		if strings.TrimSpace(operation.Name) == riskEvaluateOperation {
			return true
		}
	}
	return false
}

func parseRiskDisposition(output map[string]any) (string, error) {
	if output == nil {
		return "", ErrRiskEvaluationUnavailable
	}
	raw, _ := output["disposition"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RiskDispositionAllow, RiskDispositionDeny, RiskDispositionStepUp:
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", ErrRiskEvaluationUnavailable
	}
}

// composeRiskDisposition：deny > step_up > allow。
func composeRiskDisposition(current, next string) string {
	rank := func(value string) int {
		switch value {
		case RiskDispositionDeny:
			return 3
		case RiskDispositionStepUp:
			return 2
		case RiskDispositionAllow:
			return 1
		default:
			return 0
		}
	}
	if rank(next) > rank(current) {
		return next
	}
	return current
}

func requireRiskAllow(disposition string) error {
	switch disposition {
	case RiskDispositionAllow:
		return nil
	case RiskDispositionDeny:
		return ErrRiskEvaluationDenied
	case RiskDispositionStepUp:
		return ErrRiskEvaluationStepUp
	default:
		return ErrRiskEvaluationUnavailable
	}
}

// RegistryRiskProviderSource 从 Identity Registry 读取活跃 risk 提供方。
type RegistryRiskProviderSource struct {
	Registry *identityregistry.Registry
}

func (s RegistryRiskProviderSource) RiskProviders(context.Context) ([]identityregistry.ProviderContribution, error) {
	if s.Registry == nil {
		return nil, ErrRiskEvaluationUnavailable
	}
	providers := s.Registry.Providers(identityregistry.ProviderKindRisk)
	// 仅保留声明了 risk.evaluate 的可执行提供方。
	out := make([]identityregistry.ProviderContribution, 0, len(providers))
	for _, provider := range providers {
		if riskProviderHasEvaluate(provider) {
			out = append(out, provider)
		}
	}
	return out, nil
}
