package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	AuthOperationRegistrationStart    = "registration.start"
	AuthOperationRegistrationComplete = "registration.complete"
	AuthOperationLoginStart           = "login.start"
	AuthOperationLoginComplete        = "login.complete"
	AuthOperationLinkStart            = "link.start"
	AuthOperationLinkComplete         = "link.complete"
	// AuthOperationProviderProbe 是有界、版本化的配置/可达性探测（T8B）。
	// 无账户、链接或会话效应；不得用 probe_pending 冒充产品实现。
	AuthOperationProviderProbe = "provider.probe"

	AuthStartStatusContinue  = "continue"
	AuthStartStatusRedirect  = "redirect"
	AuthStartStatusChallenge = "challenge"
)

var (
	ErrAuthProviderFlowInvalid     = errors.New("identity: auth provider flow input is invalid")
	ErrAuthProviderFlowUnavailable = errors.New("identity: auth provider flow is unavailable")
	ErrAuthProviderFlowDenied      = errors.New("identity: auth provider flow was denied")
	ErrAuthProviderNotFound        = errors.New("identity: auth provider was not found")
)

// AuthProviderStartInput 是 Host 拥有的外部认证/注册/链接启动声明。
// 不包含密码、cookie、session id 或 CSRF。
type AuthProviderStartInput struct {
	ProviderID        string
	Operation         string
	ActorUserID       int64 // link.start 需要已登录用户；registration/login 可为 0
	CorrelationID     string
	DeviceFingerprint string
	ClientClass       string
	RedirectHint      string
	// Host 拥有的 OAuth 材料（见 plans/2026-07-27 M1 freeze）：
	// state/PKCE 由 Host 生成并存储于一次性 callback 事务；callbackUrl 是保留 Core 路由。
	// 插件用这些构造外部 authorize URL，但永不存储或回传它们。
	State         string
	CodeChallenge string
	CallbackURL   string
}

// AuthProviderCompleteInput 是 Host 拥有的完成声明。插件返回的是外部主体断言，
// 不是授权结果；Host 仍负责用户创建、链接与会话。
//
// CodeVerifier / CallbackURL 必须由 Host 从一次性 callback 事务与可信站点基址注入；
// 绝不可从浏览器输入重建。
type AuthProviderCompleteInput struct {
	ProviderID        string
	Operation         string
	ActorUserID       int64 // link.complete 必须等于目标用户；registration.complete 必须为 0
	TargetUserID      int64 // link.complete 的目标用户；registration 为 0
	CorrelationID     string
	CompletionToken   string
	DeviceFingerprint string
	ClientClass       string
	IdempotencyKey    string
	// Host 拥有的 OAuth 材料：与 start 时写入 callback 事务的 verifier / 绝对 callback URL 一致。
	CodeVerifier string
	CallbackURL  string
}

// AuthProviderStartResult 是 start 操作的 Host 解析结果。
type AuthProviderStartResult struct {
	ProviderID     string
	Operation      string
	Status         string
	CorrelationID  string
	ContinueToken  string
	RedirectURL    string
	ChallengeKind  string
	ProviderOutput map[string]any
}

// AuthProviderCompleteResult 是 complete 操作的 Host 解析结果。
//
// Core-HMAC 模式（plans/2026-07-27 M0 freeze）：
//   - ProviderSubject 是 raw 外部 subject（如 GitHub 数字 id），仅在此结构内短暂
//     存在，由 Core 在 ExternalAuthService 内立即转为 keyed digest，永不进入
//     浏览器/API/日志/审计/持久化存储之外的位置；
//   - SubjectDigest 为兼容旧 fixture（如 membership-reference）保留；当插件
//     返回 providerSubject 时为空，由 Core 计算。
//
// T1A：Complete 只返回断言，不写入 external link。link 持久化必须发生在
// 当前会话 actor、recent-auth、operation/activation 与 live exact-artifact
// 校验之后（见 ExternalAuthService.CompleteLink）。
type AuthProviderCompleteResult struct {
	ProviderID              string
	Operation               string
	ProviderSubject         string // raw subject（Core-HMAC 模式）
	SubjectDigest           string // 兼容旧 fixture 的 plugin-computed digest
	UsernameHint            string
	DisplayName             string
	EmailHint               string
	EmailVerified           bool
	ProviderContractVersion string
	// Live artifact 快照（解析自当前 Registry，供 callback 与事务比对）。
	OwnerExtensionID      string
	OwnerExtensionVersion string
	OwnerPackageDigest    string
	ProviderOutput        map[string]any
}

// AuthProviderSource 按精确 id 解析活跃 auth 提供方。
type AuthProviderSource interface {
	ResolveAuthProvider(ctx context.Context, providerID string) (identityregistry.ProviderContribution, error)
}

// AuthProviderInvoker 调用单个 exact auth 提供方操作。
type AuthProviderInvoker interface {
	InvokeExact(
		ctx context.Context,
		provider identityregistry.ProviderContribution,
		operation string,
		actorUserID int64,
		input map[string]any,
		accept func(context.Context, map[string]any, func() error) error,
	) error
}

// AuthProviderFlow 是 Host 拥有的外部 auth 工作流。认证、注册与链接的最终
// 效应（用户、会话、链接表）仍由 Host 决定；插件输出仅为提案。
type AuthProviderFlow struct {
	source  AuthProviderSource
	invoker AuthProviderInvoker
	links   ExternalIdentityLinkStore
}

func NewAuthProviderFlow(
	source AuthProviderSource,
	invoker AuthProviderInvoker,
	links ExternalIdentityLinkStore,
) (*AuthProviderFlow, error) {
	if source == nil || invoker == nil {
		return nil, ErrAuthProviderFlowUnavailable
	}
	return &AuthProviderFlow{source: source, invoker: invoker, links: links}, nil
}

// Start 调用选定 auth 提供方的 start 操作。失败关闭，无 Core/其他提供方回退。
func (f *AuthProviderFlow) Start(ctx context.Context, input AuthProviderStartInput) (AuthProviderStartResult, error) {
	if f == nil || f.source == nil || f.invoker == nil {
		return AuthProviderStartResult{}, ErrAuthProviderFlowUnavailable
	}
	prepared, err := prepareAuthStartInput(input)
	if err != nil {
		return AuthProviderStartResult{}, err
	}
	provider, err := f.resolveAuthProvider(ctx, prepared.ProviderID, prepared.Operation)
	if err != nil {
		return AuthProviderStartResult{}, err
	}
	requestInput := map[string]any{
		"correlationId":     prepared.CorrelationID,
		"deviceFingerprint": prepared.DeviceFingerprint,
		"clientClass":       prepared.ClientClass,
	}
	if prepared.RedirectHint != "" {
		requestInput["redirectHint"] = prepared.RedirectHint
	}
	if prepared.ActorUserID > 0 {
		requestInput["actorUserId"] = prepared.ActorUserID
	}
	// Host 拥有的 OAuth 材料：state/PKCE/callbackUrl。插件用于构造外部 authorize URL。
	if prepared.State != "" {
		requestInput["state"] = prepared.State
	}
	if prepared.CodeChallenge != "" {
		requestInput["codeChallenge"] = prepared.CodeChallenge
	}
	if prepared.CallbackURL != "" {
		requestInput["callbackUrl"] = prepared.CallbackURL
	}

	var result AuthProviderStartResult
	err = f.invoker.InvokeExact(
		ctx, provider, prepared.Operation, prepared.ActorUserID, requestInput,
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
			result = AuthProviderStartResult{
				ProviderID:     provider.ID,
				Operation:      prepared.Operation,
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
		return AuthProviderStartResult{}, mapAuthProviderInvokeError(err)
	}
	if result.ProviderID == "" {
		return AuthProviderStartResult{}, ErrAuthProviderFlowUnavailable
	}
	return result, nil
}

// Complete 调用选定 auth 提供方的 complete 操作，仅返回外部主体断言。
//
// 绝不在此写入 external link、创建用户或签发会话。link/login/registration 的
// 业务效应由 ExternalAuthService + Host callback 在授权校验之后执行。
func (f *AuthProviderFlow) Complete(ctx context.Context, input AuthProviderCompleteInput) (AuthProviderCompleteResult, error) {
	if f == nil || f.source == nil || f.invoker == nil {
		return AuthProviderCompleteResult{}, ErrAuthProviderFlowUnavailable
	}
	prepared, err := prepareAuthCompleteInput(input)
	if err != nil {
		return AuthProviderCompleteResult{}, err
	}
	provider, err := f.resolveAuthProvider(ctx, prepared.ProviderID, prepared.Operation)
	if err != nil {
		return AuthProviderCompleteResult{}, err
	}
	requestInput := map[string]any{
		"correlationId":     prepared.CorrelationID,
		"completionToken":   prepared.CompletionToken,
		"deviceFingerprint": prepared.DeviceFingerprint,
		"clientClass":       prepared.ClientClass,
	}
	if prepared.ActorUserID > 0 {
		requestInput["actorUserId"] = prepared.ActorUserID
	}
	if prepared.TargetUserID > 0 {
		requestInput["targetUserId"] = prepared.TargetUserID
	}
	// Host 拥有的 PKCE verifier 与绝对 callback URL：与 start 时事务绑定一致。
	if prepared.CodeVerifier != "" {
		requestInput["codeVerifier"] = prepared.CodeVerifier
	}
	if prepared.CallbackURL != "" {
		requestInput["callbackUrl"] = prepared.CallbackURL
	}

	var result AuthProviderCompleteResult
	err = f.invoker.InvokeExact(
		ctx, provider, prepared.Operation, prepared.ActorUserID, requestInput,
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseAuthCompleteOutput(output)
			if parseErr != nil {
				return parseErr
			}
			// Core-HMAC：若插件返回 raw subject，由 Core 计算 keyed digest；
			// 否则使用兼容旧 fixture 的 plugin-computed digest。
			resolvedDigest := parsed.subjectDigest
			if parsed.rawSubject != "" {
				computed, computeErr := ComputeSubjectDigest(provider.ID, parsed.rawSubject)
				if computeErr != nil {
					return computeErr
				}
				resolvedDigest = computed
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			result = AuthProviderCompleteResult{
				ProviderID:              provider.ID,
				Operation:               prepared.Operation,
				ProviderSubject:         parsed.rawSubject,
				SubjectDigest:           resolvedDigest,
				ProviderContractVersion: provider.ContractVersion,
				UsernameHint:            parsed.usernameHint,
				DisplayName:             parsed.displayName,
				EmailHint:               parsed.emailHint,
				EmailVerified:           parsed.emailVerified,
				OwnerExtensionID:        provider.Artifact.ExtensionID,
				OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
				OwnerPackageDigest:      provider.Artifact.PackageDigest,
				ProviderOutput:          cloneAuthDocument(output),
			}
			return nil
		},
	)
	if err != nil {
		return AuthProviderCompleteResult{}, mapAuthProviderInvokeError(err)
	}
	if result.ProviderID == "" || result.SubjectDigest == "" {
		return AuthProviderCompleteResult{}, ErrAuthProviderFlowUnavailable
	}
	return result, nil
}

func (f *AuthProviderFlow) resolveAuthProvider(
	ctx context.Context,
	providerID, operation string,
) (identityregistry.ProviderContribution, error) {
	provider, err := f.source.ResolveAuthProvider(ctx, providerID)
	if err != nil {
		if errors.Is(err, identityregistry.ErrNotFound) {
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		}
		return identityregistry.ProviderContribution{}, ErrAuthProviderFlowUnavailable
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindAuth {
		return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
	}
	if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
		return identityregistry.ProviderContribution{}, ErrAuthProviderFlowUnavailable
	}
	if !authProviderHasOperation(provider, operation) {
		return identityregistry.ProviderContribution{}, ErrAuthProviderFlowUnavailable
	}
	return provider, nil
}

func prepareAuthStartInput(input AuthProviderStartInput) (AuthProviderStartInput, error) {
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	input.Operation = strings.ToLower(strings.TrimSpace(input.Operation))
	switch input.Operation {
	case AuthOperationRegistrationStart, AuthOperationLoginStart, AuthOperationLinkStart:
	default:
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	if input.ProviderID == "" {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	if input.ActorUserID < 0 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	// link.start 需要已登录主体。
	if input.Operation == AuthOperationLinkStart && input.ActorUserID <= 0 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	// registration/login start 必须 actorless。
	if input.Operation != AuthOperationLinkStart && input.ActorUserID != 0 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	if input.CorrelationID == "" || len(input.CorrelationID) > 200 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	input.RedirectHint = strings.TrimSpace(input.RedirectHint)
	if len(input.RedirectHint) > 2000 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	// Host 拥有的 OAuth 材料：trim 并限制长度。这些值由 Host 生成，但调用方仍需防御性校验。
	input.State = strings.TrimSpace(input.State)
	input.CodeChallenge = strings.TrimSpace(input.CodeChallenge)
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)
	if len(input.State) > 200 || len(input.CodeChallenge) > 200 || len(input.CallbackURL) > 2000 {
		return AuthProviderStartInput{}, ErrAuthProviderFlowInvalid
	}
	return input, nil
}

func prepareAuthCompleteInput(input AuthProviderCompleteInput) (AuthProviderCompleteInput, error) {
	input.ProviderID = strings.ToLower(strings.TrimSpace(input.ProviderID))
	input.Operation = strings.ToLower(strings.TrimSpace(input.Operation))
	switch input.Operation {
	case AuthOperationRegistrationComplete, AuthOperationLoginComplete, AuthOperationLinkComplete:
	default:
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	if input.ProviderID == "" {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	if input.ActorUserID < 0 || input.TargetUserID < 0 {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	switch input.Operation {
	case AuthOperationLinkComplete:
		if input.ActorUserID <= 0 || input.TargetUserID <= 0 || input.ActorUserID != input.TargetUserID {
			return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
		}
	case AuthOperationRegistrationComplete, AuthOperationLoginComplete:
		// 完成断言必须 actorless；注册链接由上层 LinkTx 合成。
		if input.ActorUserID != 0 || input.TargetUserID != 0 {
			return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
		}
	}
	input.CorrelationID = strings.TrimSpace(input.CorrelationID)
	input.CompletionToken = strings.TrimSpace(input.CompletionToken)
	if input.CorrelationID == "" || len(input.CorrelationID) > 200 {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	if input.CompletionToken == "" || len(input.CompletionToken) > 4000 {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if len(input.IdempotencyKey) > 200 {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	// Host 拥有的 OAuth 材料：trim 并限制长度。浏览器不可伪造；空值表示非 PKCE 路径。
	input.CodeVerifier = strings.TrimSpace(input.CodeVerifier)
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)
	if len(input.CodeVerifier) > 200 || len(input.CallbackURL) > 2000 {
		return AuthProviderCompleteInput{}, ErrAuthProviderFlowInvalid
	}
	return input, nil
}

func authProviderHasOperation(provider identityregistry.ProviderContribution, operation string) bool {
	for _, op := range provider.Operations {
		if strings.TrimSpace(op.Name) == operation {
			return true
		}
	}
	return false
}

type authStartParsed struct {
	status        string
	continueToken string
	redirectURL   string
	challengeKind string
}

func parseAuthStartOutput(output map[string]any) (authStartParsed, error) {
	if output == nil {
		return authStartParsed{}, ErrAuthProviderFlowUnavailable
	}
	status := strings.ToLower(strings.TrimSpace(stringFromAuthOutput(output, "status")))
	if status == "" {
		status = AuthStartStatusContinue
	}
	switch status {
	case AuthStartStatusContinue, AuthStartStatusRedirect, AuthStartStatusChallenge:
	default:
		return authStartParsed{}, ErrAuthProviderFlowUnavailable
	}
	parsed := authStartParsed{
		status:        status,
		continueToken: strings.TrimSpace(stringFromAuthOutput(output, "continueToken")),
		redirectURL:   strings.TrimSpace(stringFromAuthOutput(output, "redirectUrl")),
		challengeKind: strings.TrimSpace(stringFromAuthOutput(output, "challengeKind")),
	}
	if status == AuthStartStatusRedirect && parsed.redirectURL == "" {
		return authStartParsed{}, ErrAuthProviderFlowUnavailable
	}
	if status == AuthStartStatusChallenge && parsed.challengeKind == "" {
		return authStartParsed{}, ErrAuthProviderFlowUnavailable
	}
	if len(parsed.continueToken) > 4000 || len(parsed.redirectURL) > 2000 || len(parsed.challengeKind) > 120 {
		return authStartParsed{}, ErrAuthProviderFlowUnavailable
	}
	return parsed, nil
}

type authCompleteParsed struct {
	subjectDigest string // Core 可直接使用的 digest（兼容旧 fixture 路径）
	rawSubject    string // raw 外部 subject（Core-HMAC 模式）；非空时由调用方计算 digest
	usernameHint  string
	displayName   string
	emailHint     string
	emailVerified bool
}

// parseAuthCompleteOutput 支持两种契约（见 plans/2026-07-27 M0 freeze）：
//  1. Core-HMAC：插件返回 raw providerSubject；Core 在调用方计算 keyed digest。
//  2. 兼容旧 fixture：插件返回 providerSubjectDigest/subjectDigest（如 membership-reference）。
//
// 二者至少满足其一，否则 fail closed。
func parseAuthCompleteOutput(output map[string]any) (authCompleteParsed, error) {
	if output == nil {
		return authCompleteParsed{}, ErrAuthProviderFlowUnavailable
	}
	digest := strings.ToLower(strings.TrimSpace(stringFromAuthOutput(output, "providerSubjectDigest")))
	if digest == "" {
		digest = strings.ToLower(strings.TrimSpace(stringFromAuthOutput(output, "subjectDigest")))
	}
	rawSubject := strings.TrimSpace(stringFromAuthOutput(output, "providerSubject"))
	if rawSubject == "" {
		rawSubject = strings.TrimSpace(stringFromAuthOutput(output, "subject"))
	}
	// 至少要有 digest 或 raw subject；二者全空 → fail closed。
	if digest == "" && rawSubject == "" {
		return authCompleteParsed{}, ErrAuthProviderFlowUnavailable
	}
	// 既有 digest 又有 raw subject 时优先 Core-HMAC：丢弃插件 digest，由 Core 计算。
	if rawSubject != "" {
		digest = ""
	} else if !isAuthSubjectDigest(digest) {
		return authCompleteParsed{}, ErrAuthProviderFlowUnavailable
	}
	if len(rawSubject) > 320 {
		return authCompleteParsed{}, ErrAuthProviderFlowUnavailable
	}
	usernameHint := strings.TrimSpace(stringFromAuthOutput(output, "usernameHint"))
	displayName := strings.TrimSpace(stringFromAuthOutput(output, "displayName"))
	emailHint := strings.TrimSpace(strings.ToLower(stringFromAuthOutput(output, "emailHint")))
	emailVerified, _ := output["emailVerified"].(bool)
	if len(usernameHint) > 64 || len(displayName) > 200 || len(emailHint) > 320 {
		return authCompleteParsed{}, ErrAuthProviderFlowUnavailable
	}
	if emailHint == "" {
		emailVerified = false
	}
	return authCompleteParsed{
		subjectDigest: digest,
		rawSubject:    rawSubject,
		usernameHint:  usernameHint,
		displayName:   displayName,
		emailHint:     emailHint,
		emailVerified: emailVerified,
	}, nil
}

func isAuthSubjectDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func stringFromAuthOutput(output map[string]any, key string) string {
	raw, _ := output[key].(string)
	return raw
}

func cloneAuthDocument(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func mapAuthProviderInvokeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrAuthProviderFlowInvalid) ||
		errors.Is(err, ErrAuthProviderFlowUnavailable) ||
		errors.Is(err, ErrAuthProviderFlowDenied) ||
		errors.Is(err, ErrAuthProviderNotFound) ||
		errors.Is(err, ErrExternalIdentityLinkInvalid) ||
		errors.Is(err, ErrExternalIdentityLinkStateConflict) ||
		errors.Is(err, ErrExternalIdentityLinkIdempotencyConflict) ||
		errors.Is(err, ErrExternalIdentitySubjectConflict) ||
		errors.Is(err, ErrExternalIdentityProviderStale) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrAuthProviderFlowUnavailable, err)
}

// RegistryAuthProviderSource 从 Identity Registry 解析精确 auth 提供方。
type RegistryAuthProviderSource struct {
	Registry *identityregistry.Registry
}

func (s RegistryAuthProviderSource) ResolveAuthProvider(_ context.Context, providerID string) (identityregistry.ProviderContribution, error) {
	if s.Registry == nil {
		return identityregistry.ProviderContribution{}, ErrAuthProviderFlowUnavailable
	}
	provider, err := s.Registry.ResolveProvider(providerID)
	if err != nil {
		return identityregistry.ProviderContribution{}, err
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindAuth {
		return identityregistry.ProviderContribution{}, identityregistry.ErrNotFound
	}
	return provider, nil
}
