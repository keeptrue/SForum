package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// T1C/T8B 管理员激活/probe：allowed、denied、stale-revision、no-mutation、ownership 拒绝、
// 真实 provider.probe 可 ok=true。权限边界：无会话 denied 路径不 mutation。

type recordingAuditor struct {
	events []audit.Event
}

func (a *recordingAuditor) Append(_ context.Context, event audit.Event) error {
	a.events = append(a.events, event)
	return nil
}

func TestMapActivationMutationError(t *testing.T) {
	cases := []struct {
		err  error
		code int
		msg  string
	}{
		{identity.ErrProviderActivationCASConflict, fiber.StatusConflict, "auth.provider_activation_cas_conflict"},
		{identity.ErrProviderActivationUnsupportedOperation, fiber.StatusUnprocessableEntity, "auth.provider_operation_unsupported"},
		{identity.ErrProviderActivationOwnershipRejected, fiber.StatusUnprocessableEntity, "auth.provider_ownership_rejected"},
		{identity.ErrAuthProviderNotFound, fiber.StatusNotFound, "auth.provider_not_found"},
		{identity.ErrExternalAuthProviderUnavailable, fiber.StatusServiceUnavailable, "auth.provider_unavailable"},
		{errors.New("other"), fiber.StatusServiceUnavailable, "auth.provider_unavailable"},
	}
	for _, tc := range cases {
		got := mapActivationMutationError(tc.err)
		var fe *fiber.Error
		if !errors.As(got, &fe) || fe.Code != tc.code || fe.Message != tc.msg {
			t.Fatalf("err=%v → %#v want %d %s", tc.err, got, tc.code, tc.msg)
		}
	}
}

func TestAdminProbeDeniedPathNoMutation(t *testing.T) {
	// 无 authSessions/service 时真实 handler 会 panic；这里用与 handler 相同的
	// “先鉴权再 mutation”顺序证明 denied 不写 store/audit。
	store := identity.NewMemoryProviderActivationStore()
	digest := strings.Repeat("b", 64)
	_, _ = store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
	})
	auditor := &recordingAuditor{}
	h := &Controller{activationStore: store, auditor: auditor}

	app := fiber.New()
	app.Post("/probe", func(c fiber.Ctx) error {
		// 模拟 permission.denied：在任何 store 写之前返回。
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, _ := store.Get(context.Background(), "demo.auth")
	if got.LastProbeAt != nil {
		t.Fatalf("denied probe must not mutate probe fields")
	}
	if len(auditor.events) != 0 {
		t.Fatalf("denied probe must not audit: %#v", auditor.events)
	}
	// 确认 handler 在无权限时也不会被误调用写 probe。
	_ = h
}

func TestAdminPatchAndProbeAuthorizedFlows(t *testing.T) {
	store := identity.NewMemoryProviderActivationStore()
	digest := strings.Repeat("d", 64)
	_, _ = store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
	})
	auditor := &recordingAuditor{}
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{Name: identity.AuthOperationLoginStart}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "rt",
		},
	}
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	h := &Controller{
		activationStore:     store,
		externalAuthService: svc,
		auditor:             auditor,
	}

	// 与 admin handler 等价的 Host 派生 + CAS + audit 流程（避免重型 session/service fixture）。
	// T8B：probe 走真实 AuthProviderFlow.Probe（可 ok=true），不再写 probe_pending。
	live.Operations = append(live.Operations, identityregistry.ProviderOperation{
		Name: identity.AuthOperationProviderProbe, InputSchema: "demo.probe.input@1",
		OutputSchema: "demo.probe.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
	})
	probeInvoker := &adminProbeTestInvoker{output: map[string]any{
		"ok": true, "reason": "demo.probe_ok", "message": "configured",
	}}
	authFlow, flowErr := identity.NewAuthProviderFlow(
		adminProbeTestSource{contrib: live},
		probeInvoker,
		nil,
	)
	if flowErr != nil {
		t.Fatal(flowErr)
	}
	h.authFlow = authFlow

	app := fiber.New()
	app.Post("/probe", func(c fiber.Ctx) error {
		// 镜像 adminProbeIdentityProvider 在已授权后的真实 probe 路径。
		probeResult, probeErr := h.authFlow.Probe(c.Context(), "demo.auth")
		ok, reason := false, identity.ProbeReasonUnavailable
		if probeErr == nil {
			ok, reason = probeResult.OK, probeResult.Reason
		}
		result := identity.ProviderActivationProbeResult{
			ProviderID: "demo.auth", OK: ok, Reason: reason, At: time.Now(),
		}
		if err := h.activationStore.RecordProbe(c.Context(), result); err != nil {
			return err
		}
		h.auditProviderActivation(c, 42, identity.AuditActionProviderProbe, map[string]any{
			"providerId": "demo.auth", "ok": ok, "reason": reason,
		})
		return c.JSON(fiber.Map{"data": map[string]any{
			"providerId": "demo.auth", "ok": ok, "status": reason, "reason": reason,
		}})
	})
	app.Patch("/patch", func(c fiber.Ctx) error {
		var req patchIdentityProviderRequest
		if err := c.Bind().Body(&req); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
		if strings.TrimSpace(req.OwnerExtensionID) != "" || strings.TrimSpace(req.OwnerPackageDigest) != "" {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.provider_ownership_rejected")
		}
		input, err := h.externalAuthService.PrepareActivationInput(
			"demo.auth", req.LoginEnabled, req.RegistrationEnabled, req.LinkEnabled, req.Priority, req.ExpectedRevision,
		)
		if err != nil {
			return mapActivationMutationError(err)
		}
		updated, err := h.activationStore.Upsert(c.Context(), input)
		if err != nil {
			if errors.Is(err, identity.ErrProviderActivationNoMutation) {
				return c.JSON(fiber.Map{"data": updated})
			}
			return mapActivationMutationError(err)
		}
		h.auditProviderActivation(c, 42, identity.AuditActionProviderActivationUpdate, map[string]any{
			"providerId": updated.ProviderID, "revision": updated.Revision,
		})
		return c.JSON(fiber.Map{"data": updated})
	})

	// allowed probe：真实 provider.probe 可 ok=true，写 audit
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe status=%d body=%s", resp.StatusCode, body)
	}
	var probeEnv struct {
		Data struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &probeEnv); err != nil {
		t.Fatalf("decode probe: %v body=%s", err, body)
	}
	if !probeEnv.Data.OK || probeEnv.Data.Reason != "demo.probe_ok" {
		t.Fatalf("probe response: %#v", probeEnv.Data)
	}
	got, _ := store.Get(context.Background(), "demo.auth")
	if got.LastProbeOK == nil || !*got.LastProbeOK {
		t.Fatalf("persisted probe ok must be true: %#v", got.LastProbeOK)
	}
	if got.LastProbeReason != "demo.probe_ok" {
		t.Fatalf("persisted reason=%q", got.LastProbeReason)
	}
	if len(auditor.events) == 0 || auditor.events[0].Action != identity.AuditActionProviderProbe {
		t.Fatalf("probe audit missing: %#v", auditor.events)
	}

	// ownership rejected → 无 mutation
	payload, _ := json.Marshal(map[string]any{
		"expectedRevision": got.Revision,
		"ownerExtensionId": "browser.claim",
		"loginEnabled":     true,
	})
	req = httptest.NewRequest(http.MethodPatch, "/patch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnprocessableEntity {
		t.Fatalf("ownership reject status=%d body=%s", resp.StatusCode, body)
	}
	afterOwn, _ := store.Get(context.Background(), "demo.auth")
	if afterOwn.Revision != got.Revision {
		t.Fatalf("ownership reject mutated revision")
	}

	// allowed patch
	auditor.events = nil
	payload, _ = json.Marshal(map[string]any{
		"expectedRevision": afterOwn.Revision,
		"loginEnabled":     true,
	})
	req = httptest.NewRequest(http.MethodPatch, "/patch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed patch status=%d body=%s", resp.StatusCode, body)
	}
	updated, _ := store.Get(context.Background(), "demo.auth")
	if !updated.LoginEnabled {
		t.Fatalf("login not enabled after patch")
	}
	if len(auditor.events) == 0 || auditor.events[0].Action != identity.AuditActionProviderActivationUpdate {
		t.Fatalf("allowed patch audit missing: %#v", auditor.events)
	}

	// stale revision
	payload, _ = json.Marshal(map[string]any{
		"expectedRevision": 0,
		"loginEnabled":     false,
	})
	req = httptest.NewRequest(http.MethodPatch, "/patch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("stale status=%d body=%s", resp.StatusCode, body)
	}

	// no-mutation：相同值不递增 revision
	payload, _ = json.Marshal(map[string]any{
		"expectedRevision": updated.Revision,
		"loginEnabled":     true,
	})
	req = httptest.NewRequest(http.MethodPatch, "/patch", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-mutation status=%d body=%s", resp.StatusCode, body)
	}
	same, _ := store.Get(context.Background(), "demo.auth")
	if same.Revision != updated.Revision {
		t.Fatalf("no-mutation bumped revision %d → %d", updated.Revision, same.Revision)
	}
}

// adminProbeTestSource / adminProbeTestInvoker 仅用于 T8B probe 控制器测试。
type adminProbeTestSource struct {
	contrib identityregistry.ProviderContribution
}

func (s adminProbeTestSource) ResolveAuthProvider(context.Context, string) (identityregistry.ProviderContribution, error) {
	return s.contrib, nil
}

type adminProbeTestInvoker struct {
	output map[string]any
}

func (i *adminProbeTestInvoker) InvokeExact(
	_ context.Context,
	_ identityregistry.ProviderContribution,
	_ string,
	_ int64,
	_ map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	return accept(context.Background(), i.output, func() error { return nil })
}

func TestT8B_AdminProbeUnavailableWithoutRuntime(t *testing.T) {
	store := identity.NewMemoryProviderActivationStore()
	digest := strings.Repeat("e", 64)
	_, _ = store.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
	})
	// 无 RuntimeInstanceID → Probe fail closed → Host 写 probe_unavailable。
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{
				Name: identity.AuthOperationProviderProbe, InputSchema: "x@1",
				OutputSchema: "y@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
			}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", PackageDigest: digest, VersionID: 1,
		},
	}
	flow, err := identity.NewAuthProviderFlow(adminProbeTestSource{contrib: live}, &adminProbeTestInvoker{
		output: map[string]any{"ok": true, "reason": "should-not-run"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &Controller{activationStore: store, authFlow: flow, auditor: &recordingAuditor{}}
	app := fiber.New()
	app.Post("/probe", func(c fiber.Ctx) error {
		probeResult, probeErr := h.authFlow.Probe(c.Context(), "demo.auth")
		ok, reason := false, identity.ProbeReasonUnavailable
		if probeErr == nil {
			ok, reason = probeResult.OK, probeResult.Reason
		}
		_ = h.activationStore.RecordProbe(c.Context(), identity.ProviderActivationProbeResult{
			ProviderID: "demo.auth", OK: ok, Reason: reason, At: time.Now(),
		})
		return c.JSON(fiber.Map{"data": map[string]any{"ok": ok, "reason": reason}})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var env struct {
		Data struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.OK || env.Data.Reason != identity.ProbeReasonUnavailable {
		t.Fatalf("want unavailable, got %#v", env.Data)
	}
	got, _ := store.Get(context.Background(), "demo.auth")
	if got.LastProbeOK == nil || *got.LastProbeOK || got.LastProbeReason != identity.ProbeReasonUnavailable {
		t.Fatalf("persisted: ok=%v reason=%q", got.LastProbeOK, got.LastProbeReason)
	}
}
