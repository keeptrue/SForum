package main

import (
	"context"
	"errors"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	extID          = "sforum.auth-github"
	authProviderID = extID + ".auth"
	handlerName    = extID + ".identity"

	// 操作超时：令牌交换 + 用户/邮箱拉取，给网络余量且不超过 Manifest 上限 5000ms。
	authOpTimeoutMS = 4000
)

// newGitHubIdentityRegistry 注册 auth 提供方的 start/complete 与有界 provider.probe。
func newGitHubIdentityRegistry(oauth *GitHubOAuth) (*pluginv2.IdentityProviderRegistry, error) {
	def := pluginv2.IdentityProviderDefinition{
		ID:              authProviderID,
		ContractVersion: authProviderID + "@1",
		Kind:            "auth",
		Handler:         handlerName,
		Priority:        100,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("registration.start", "start.input@1", "start.output@1", authOpTimeoutMS),
			op("registration.complete", "complete.input@1", "auth.complete.output@1", authOpTimeoutMS),
			op("login.start", "start.input@1", "start.output@1", authOpTimeoutMS),
			op("login.complete", "complete.input@1", "auth.complete.output@1", authOpTimeoutMS),
			op("link.start", "start.input@1", "start.output@1", authOpTimeoutMS),
			op("link.complete", "complete.input@1", "auth.complete.output@1", authOpTimeoutMS),
			// T8B：有界探测；deadline 短于业务 complete，避免管理端卡住。
			op("provider.probe", "probe.input@1", "probe.output@1", probeOpTimeoutMS),
		},
		Execute: func(ctx context.Context, call *pluginv2.IdentityProviderCall) (*protocolwire.TypedDocument, error) {
			return handleAuthOperation(ctx, oauth, call)
		},
	}
	return pluginv2.NewIdentityProviderRegistry(def)
}

const probeOpTimeoutMS = 3000

func op(name, input, output string, timeoutMS int) pluginv2.IdentityProviderOperationDefinition {
	if timeoutMS <= 0 {
		timeoutMS = authOpTimeoutMS
	}
	return pluginv2.IdentityProviderOperationDefinition{
		Name:          name,
		InputSchema:   schemaID(input),
		OutputSchema:  schemaID(output),
		TimeoutMS:     timeoutMS,
		FailurePolicy: "fail_closed",
	}
}

// schemaID 将短名映射为 packageFiles id@version，与 Manifest 对齐。
func schemaID(short string) string {
	parts := strings.Split(short, "@")
	if len(parts) != 2 {
		return extID + ".schema." + short
	}
	return extID + ".schema." + parts[0] + "@" + parts[1]
}

func handleAuthOperation(
	ctx context.Context,
	oauth *GitHubOAuth,
	call *pluginv2.IdentityProviderCall,
) (*protocolwire.TypedDocument, error) {
	if call == nil {
		return nil, errors.New("missing identity call")
	}
	if err := refuseUnsafeIdentityContext(call); err != nil {
		return nil, err
	}
	if call.Handler != handlerName {
		return nil, errors.New("identity handler drifted")
	}
	if call.ID != authProviderID {
		return nil, errors.New("identity provider id drifted")
	}
	input := pluginv2.TypedDocumentValues(call.Input)

	switch call.Operation {
	case "registration.start", "login.start", "link.start":
		return handleStart(oauth, call, input)
	case "registration.complete", "login.complete", "link.complete":
		return handleComplete(ctx, oauth, call, input)
	case "provider.probe":
		return handleProbe(ctx, oauth)
	default:
		return nil, errors.New("unknown identity operation")
	}
}

// handleProbe 调用有界 GitHubOAuth.Probe；不交换令牌、不返回密钥或 raw subject。
func handleProbe(ctx context.Context, oauth *GitHubOAuth) (*protocolwire.TypedDocument, error) {
	result := oauth.Probe(ctx)
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		if result.OK {
			reason = "github.probe_ok"
		} else {
			reason = "github.probe_failed"
		}
	}
	// reason 必须是稳定短码；message 已在 Probe 内脱敏。
	out := map[string]any{
		"ok":     result.OK,
		"reason": reason,
	}
	if msg := strings.TrimSpace(RedactSensitive(result.Message)); msg != "" {
		if len(msg) > 500 {
			msg = msg[:500]
		}
		out["message"] = msg
	}
	return pluginv2.NewTypedDocument(schemaID("probe.output@1"), out)
}

func handleStart(
	oauth *GitHubOAuth,
	call *pluginv2.IdentityProviderCall,
	input map[string]any,
) (*protocolwire.TypedDocument, error) {
	correlation, _ := input["correlationId"].(string)
	if strings.TrimSpace(correlation) == "" {
		return nil, publicErr("github.start_input_invalid", "correlationId required")
	}
	state, _ := input["state"].(string)
	challenge, _ := input["codeChallenge"].(string)
	callbackURL, _ := input["callbackUrl"].(string)
	// codeChallengeMethod 若出现必须为 S256；Host 当前可能不传，插件默认 S256。
	if method, _ := input["codeChallengeMethod"].(string); method != "" && method != "S256" {
		return nil, publicErr("github.pkce_method_unsupported", "only S256 PKCE is supported")
	}

	redirectURL, err := oauth.BuildAuthorizeURL(state, challenge, callbackURL)
	if err != nil {
		return nil, err
	}
	return pluginv2.NewTypedDocument(schemaID("start.output@1"), map[string]any{
		"status":      "redirect",
		"redirectUrl": redirectURL,
	})
}

func handleComplete(
	ctx context.Context,
	oauth *GitHubOAuth,
	call *pluginv2.IdentityProviderCall,
	input map[string]any,
) (*protocolwire.TypedDocument, error) {
	correlation, _ := input["correlationId"].(string)
	code, _ := input["completionToken"].(string)
	if strings.TrimSpace(correlation) == "" || strings.TrimSpace(code) == "" {
		return nil, publicErr("github.complete_input_invalid", "correlationId and completionToken required")
	}
	verifier, _ := input["codeVerifier"].(string)
	callbackURL, _ := input["callbackUrl"].(string)

	assertion, err := oauth.CompleteWithCode(ctx, code, verifier, callbackURL)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"providerSubject": assertion.ProviderSubject,
	}
	if assertion.UsernameHint != "" {
		out["usernameHint"] = assertion.UsernameHint
	}
	if assertion.DisplayName != "" {
		out["displayName"] = assertion.DisplayName
	}
	if assertion.EmailHint != "" {
		out["emailHint"] = assertion.EmailHint
		out["emailVerified"] = assertion.EmailVerified
	}
	// 绝不输出 providerSubjectDigest：Host 用 IDENTITY_SUBJECT_HMAC_SECRET 计算。
	return pluginv2.NewTypedDocument(schemaID("auth.complete.output@1"), out)
}

func refuseUnsafeIdentityContext(call *pluginv2.IdentityProviderCall) error {
	if call.Context == nil {
		return errors.New("missing request context")
	}
	if len(call.Context.GetGrantedAuthority()) != 0 ||
		call.Context.GetIdempotencyKey() != "" ||
		len(call.Context.GetHostCommandDelegations()) != 0 ||
		len(call.Context.GetHostQueryDelegations()) != 0 {
		return errors.New("authority projection leaked into identity runtime")
	}
	actor := call.Context.GetActor()
	if actor != nil && (actor.GetSessionId() != "" || actor.GetClientIp() != "" ||
		actor.GetUserAgent() != "" || len(actor.GetRoleIds()) != 0 || len(actor.GetPermissionKeys()) != 0) {
		return errors.New("unsafe actor projection leaked into identity runtime")
	}
	return nil
}
