package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	extID = "sforum.membership-reference"

	authProviderID     = extID + ".auth"
	profileProviderID  = extID + ".profile"
	recoveryProviderID = extID + ".recovery"
	sessionProviderID  = extID + ".session"
	riskProviderID     = extID + ".risk"

	handler = extID + ".identity"
)

// 进程内资料状态，仅供参考 fixture 联调。
var (
	profileMu   sync.Mutex
	profileData = map[int64]map[string]any{}
	accountData = map[int64]map[string]any{}
)

func main() {
	registry, err := pluginv2.NewIdentityProviderRegistry(
		authProviderDefinition(),
		profileProviderDefinition(),
		recoveryProviderDefinition(),
		sessionProviderDefinition(),
		riskProviderDefinition(),
	)
	if err != nil {
		panic(err)
	}
	pluginv2.Serve(pluginv2.NewServer().
		WithFeatures(pluginv2.IdentityRuntimeProtocolFeature()).
		WithIdentityProviderRegistry(registry),
	)
}

func authProviderDefinition() pluginv2.IdentityProviderDefinition {
	return pluginv2.IdentityProviderDefinition{
		ID: authProviderID, ContractVersion: authProviderID + "@1",
		Kind: "auth", Handler: handler, Priority: 100,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("registration.start", "start.input@1", "start.output@1", "fail_closed"),
			op("registration.complete", "complete.input@1", "auth.complete.output@1", "fail_closed"),
			op("login.start", "start.input@1", "start.output@1", "fail_closed"),
			op("login.complete", "complete.input@1", "auth.complete.output@1", "fail_closed"),
			op("link.start", "start.input@1", "start.output@1", "fail_closed"),
			op("link.complete", "complete.input@1", "auth.complete.output@1", "fail_closed"),
		},
		Execute: handleIdentity,
	}
}

func profileProviderDefinition() pluginv2.IdentityProviderDefinition {
	return pluginv2.IdentityProviderDefinition{
		ID: profileProviderID, ContractVersion: profileProviderID + "@1",
		Kind: "profile", Handler: handler, Priority: 100,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("sections.list", "profile.list.input@1", "profile.list.output@1", "omit"),
			op("section.read", "profile.section.input@1", "profile.section.output@1", "omit"),
			op("section.update", "profile.section.input@1", "profile.section.output@1", "fail_closed"),
			op("account.read", "profile.account.input@1", "profile.account.output@1", "fail_closed"),
			op("account.update", "profile.account.input@1", "profile.account.output@1", "fail_closed"),
		},
		Execute: handleIdentity,
	}
}

func recoveryProviderDefinition() pluginv2.IdentityProviderDefinition {
	return pluginv2.IdentityProviderDefinition{
		ID: recoveryProviderID, ContractVersion: recoveryProviderID + "@1",
		Kind: "recovery", Handler: handler, Priority: 50,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("recovery.start", "start.input@1", "start.output@1", "fail_closed"),
			op("recovery.complete", "complete.input@1", "recovery.complete.output@1", "fail_closed"),
		},
		Execute: handleIdentity,
	}
}

func sessionProviderDefinition() pluginv2.IdentityProviderDefinition {
	return pluginv2.IdentityProviderDefinition{
		ID: sessionProviderID, ContractVersion: sessionProviderID + "@1",
		Kind: "session", Handler: handler, Priority: 100,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("session.evaluate", "session.evaluate.input@1", "session.evaluate.output@1", "fail_closed"),
		},
		Execute: handleIdentity,
	}
}

func riskProviderDefinition() pluginv2.IdentityProviderDefinition {
	return pluginv2.IdentityProviderDefinition{
		ID: riskProviderID, ContractVersion: riskProviderID + "@1",
		Kind: "risk", Handler: handler, Priority: 100,
		Operations: []pluginv2.IdentityProviderOperationDefinition{
			op("risk.evaluate", "risk.evaluate.input@1", "risk.evaluate.output@1", "fail_closed"),
		},
		Execute: handleIdentity,
	}
}

func op(name, input, output, policy string) pluginv2.IdentityProviderOperationDefinition {
	return pluginv2.IdentityProviderOperationDefinition{
		Name: name,
		InputSchema:  schemaID(input),
		OutputSchema: schemaID(output),
		TimeoutMS:    1000,
		FailurePolicy: policy,
	}
}

// schemaID 将短名映射为 packageFiles id@version，与 Manifest 对齐。
func schemaID(short string) string {
	// short 形如 "start.input@1" → "sforum.membership-reference.schema.start.input@1"
	parts := strings.Split(short, "@")
	if len(parts) != 2 {
		return extID + ".schema." + short
	}
	return extID + ".schema." + parts[0] + "@" + parts[1]
}

func handleIdentity(ctx context.Context, call *pluginv2.IdentityProviderCall) (*protocolwire.TypedDocument, error) {
	if call == nil {
		return nil, errors.New("missing identity call")
	}
	if err := refuseUnsafeIdentityContext(call); err != nil {
		return nil, err
	}
	if call.Handler != handler {
		return nil, errors.New("identity handler drifted")
	}
	input := documentValues(call.Input)
	switch call.Operation {
	case "registration.start", "login.start", "link.start", "recovery.start":
		return startResult(call, input)
	case "registration.complete", "login.complete", "link.complete":
		return authCompleteResult(call, input)
	case "recovery.complete":
		return recoveryCompleteResult(call, input)
	case "sections.list":
		return sectionsListResult(call, input)
	case "section.read":
		return sectionReadResult(call, input)
	case "section.update":
		return sectionUpdateResult(call, input)
	case "account.read":
		return accountReadResult(call, input)
	case "account.update":
		return accountUpdateResult(call, input)
	case "session.evaluate":
		return dispositionResult(call, sessionDisposition(input), "session.evaluate.output@1")
	case "risk.evaluate":
		return dispositionResult(call, riskDisposition(input), "risk.evaluate.output@1")
	default:
		return nil, errors.New("unknown identity operation")
	}
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

func startResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	correlation, _ := input["correlationId"].(string)
	if correlation == "" {
		return nil, errors.New("correlationId required")
	}
	// 测试钩子：correlation 含 fail → 失败关闭。
	if strings.Contains(correlation, "fail") {
		return nil, errors.New("reference identity start failure")
	}
	status := "continue"
	out := map[string]any{"status": status, "continueToken": "continue:" + correlation}
	if strings.Contains(correlation, "redirect") {
		out["status"] = "redirect"
		out["redirectUrl"] = "https://idp.example/membership/" + correlation
	}
	if strings.Contains(correlation, "challenge") {
		out["status"] = "challenge"
		out["challengeKind"] = "membership_code"
	}
	return pluginv2.NewTypedDocument(schemaID("start.output@1"), out)
}

func authCompleteResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	token, _ := input["completionToken"].(string)
	if token == "" {
		return nil, errors.New("completionToken required")
	}
	if strings.Contains(token, "fail") {
		return nil, errors.New("reference identity complete failure")
	}
	subject := "member-subject-default"
	if strings.HasPrefix(token, "subject:") {
		subject = strings.TrimPrefix(token, "subject:")
	}
	digest := subjectDigest(subject)
	return pluginv2.NewTypedDocument(schemaID("auth.complete.output@1"), map[string]any{
		"providerSubjectDigest": digest,
		"displayName":           "Membership Member",
		"emailHint":             "member@example.com",
	})
}

func recoveryCompleteResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	token, _ := input["completionToken"].(string)
	if token == "" {
		return nil, errors.New("completionToken required")
	}
	if strings.Contains(token, "fail") {
		return nil, errors.New("reference recovery complete failure")
	}
	out := map[string]any{
		"providerSubjectDigest": subjectDigest("member-subject-default"),
		"userHintId":            float64(42),
	}
	return pluginv2.NewTypedDocument(schemaID("recovery.complete.output@1"), out)
}

func sectionsListResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	target := int64From(input["targetUserId"])
	if target <= 0 {
		return nil, errors.New("targetUserId required")
	}
	if fingerprint, _ := input["deviceFingerprint"].(string); strings.Contains(fingerprint, "omit-list") {
		return nil, errors.New("reference profile list omit")
	}
	fields := profileFields(target)
	return pluginv2.NewTypedDocument(schemaID("profile.list.output@1"), map[string]any{
		"sections": []any{
			map[string]any{
				"sectionId": "membership",
				"title":     "Membership",
				"fields":    fields,
			},
		},
	})
}

func sectionReadResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	sectionID, _ := input["sectionId"].(string)
	target := int64From(input["targetUserId"])
	if sectionID == "" || target <= 0 {
		return nil, errors.New("sectionId and targetUserId required")
	}
	if sectionID != "membership" {
		return nil, errors.New("unknown section")
	}
	return pluginv2.NewTypedDocument(schemaID("profile.section.output@1"), map[string]any{
		"sectionId": "membership",
		"title":     "Membership",
		"fields":    profileFields(target),
	})
}

func sectionUpdateResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	sectionID, _ := input["sectionId"].(string)
	target := int64From(input["targetUserId"])
	actor := int64From(input["actorUserId"])
	if sectionID != "membership" || target <= 0 || actor != target {
		return nil, errors.New("section update denied")
	}
	fields, _ := input["fields"].(map[string]any)
	if fields == nil {
		return nil, errors.New("fields required")
	}
	profileMu.Lock()
	profileData[target] = cloneMap(fields)
	profileMu.Unlock()
	return pluginv2.NewTypedDocument(schemaID("profile.section.output@1"), map[string]any{
		"sectionId": "membership",
		"title":     "Membership",
		"fields":    cloneMap(fields),
	})
}

func accountReadResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	target := int64From(input["targetUserId"])
	if target <= 0 {
		return nil, errors.New("targetUserId required")
	}
	return pluginv2.NewTypedDocument(schemaID("profile.account.output@1"), map[string]any{
		"fields": accountFields(target),
	})
}

func accountUpdateResult(call *pluginv2.IdentityProviderCall, input map[string]any) (*protocolwire.TypedDocument, error) {
	target := int64From(input["targetUserId"])
	actor := int64From(input["actorUserId"])
	if target <= 0 || actor != target {
		return nil, errors.New("account update denied")
	}
	fields, _ := input["fields"].(map[string]any)
	if fields == nil {
		return nil, errors.New("fields required")
	}
	profileMu.Lock()
	accountData[target] = cloneMap(fields)
	profileMu.Unlock()
	return pluginv2.NewTypedDocument(schemaID("profile.account.output@1"), map[string]any{
		"fields": cloneMap(fields),
	})
}

func sessionDisposition(input map[string]any) string {
	if fp, _ := input["deviceFingerprint"].(string); strings.Contains(fp, "deny") {
		return "deny"
	}
	if fp, _ := input["deviceFingerprint"].(string); strings.Contains(fp, "step_up") {
		return "step_up"
	}
	return "allow"
}

func riskDisposition(input map[string]any) string {
	if fp, _ := input["deviceFingerprint"].(string); strings.Contains(fp, "risk-deny") {
		return "deny"
	}
	if fp, _ := input["deviceFingerprint"].(string); strings.Contains(fp, "risk-step") {
		return "step_up"
	}
	return "allow"
}

func dispositionResult(call *pluginv2.IdentityProviderCall, disposition, shortSchema string) (*protocolwire.TypedDocument, error) {
	return pluginv2.NewTypedDocument(schemaID(shortSchema), map[string]any{"disposition": disposition})
}

func profileFields(userID int64) map[string]any {
	profileMu.Lock()
	defer profileMu.Unlock()
	if fields, ok := profileData[userID]; ok {
		return cloneMap(fields)
	}
	return map[string]any{"tierLabel": "standard"}
}

func accountFields(userID int64) map[string]any {
	profileMu.Lock()
	defer profileMu.Unlock()
	if fields, ok := accountData[userID]; ok {
		return cloneMap(fields)
	}
	return map[string]any{"tier": "standard"}
}

func subjectDigest(subject string) string {
	sum := sha256.Sum256([]byte("sforum.membership-reference:" + subject))
	return hex.EncodeToString(sum[:])
}

func documentValues(doc *protocolwire.TypedDocument) map[string]any {
	if doc == nil || doc.GetValue() == nil {
		return map[string]any{}
	}
	return doc.GetValue().AsMap()
}

func int64From(raw any) int64 {
	switch value := raw.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case int32:
		return int64(value)
	default:
		return 0
	}
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
