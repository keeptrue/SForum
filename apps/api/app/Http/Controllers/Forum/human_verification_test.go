package forumcontroller

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"testing"

	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// postRiskTestProvider 只实现 humanverify.Provider 的「验证通过」分支。
// 需要「验证失败」的用例走 Service 的缺 token 早退，不依赖 provider 造错。
type postRiskTestProvider struct{}

func (postRiskTestProvider) Challenge(_ context.Context, purpose humanverify.Purpose, _ humanverify.Subject) (humanverify.Challenge, error) {
	return humanverify.Challenge{Provider: humanverify.ProviderAltcha, Purpose: purpose, Payload: map[string]any{}}, nil
}

func (postRiskTestProvider) Verify(_ context.Context, _ humanverify.VerifyRequest) (humanverify.VerifyResult, error) {
	return humanverify.VerifyResult{Verified: true, Code: humanverify.CodeOK}, nil
}

// postRiskVerifier 构造与 bootstrap 同形状的 verifier：provider 恒为 altcha，
// 只通过 EnabledPurposes 表达「运营是否打开了发帖人机验证」。
func postRiskVerifier(enabled bool) humanverify.Verifier {
	return humanverify.NewService(
		humanverify.ServiceConfig{
			Enabled:         true,
			EnabledPurposes: map[humanverify.Purpose]bool{humanverify.PurposePostRisk: enabled},
		},
		postRiskTestProvider{},
		humanverify.NewMemoryStore(),
	)
}

const postRiskTopicBody = `{"categorySlug":"general","title":"post risk topic","content":{"rawContent":"ordinary topic body","sourceFormat":"markdown","editorType":"markdown"}}`

const postRiskCommentBody = `{"content":{"rawContent":"ordinary reply body","sourceFormat":"markdown","editorType":"markdown"}}`

type postRiskErrorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Reason string              `json:"reason"`
		Fields map[string][]string `json:"fields"`
	} `json:"data"`
}

func decodePostRiskError(t *testing.T, resp *nethttp.Response) postRiskErrorEnvelope {
	t.Helper()
	defer resp.Body.Close()
	var envelope postRiskErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return envelope
}

func assertPostRiskRequired(t *testing.T, resp *nethttp.Response, operation string) {
	t.Helper()
	envelope := decodePostRiskError(t, resp)
	if resp.StatusCode != nethttp.StatusUnprocessableEntity || envelope.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("%s without token: status=%d code=%d want 422", operation, resp.StatusCode, envelope.Code)
	}
	if envelope.Data.Reason != humanverify.CodeRequired {
		t.Fatalf("%s without token: reason=%q want %q", operation, envelope.Data.Reason, humanverify.CodeRequired)
	}
	// 字段名与登录侧一致：前端按 data.fields.humanVerification 定位提示。
	// 字段值经 LocalizeFields 本地化成人话，稳定契约是 data.reason；这里只断言
	// 键存在且带一条非空提示，避免把翻译文案写进测试。
	fields := envelope.Data.Fields[humanverifyFieldHumanVerification]
	if len(fields) != 1 || fields[0] == "" {
		t.Fatalf("%s without token: fields=%#v want one humanVerification message", operation, envelope.Data.Fields)
	}
}

// humanverifyFieldHumanVerification 与 identity.FieldHumanVerification 同值。
// 这里写字面量是为了让 wire 契约的断言不依赖被测代码的常量。
const humanverifyFieldHumanVerification = "humanVerification"

func TestCreateTopicRejectsWhenPostRiskVerificationEnabledWithoutToken(t *testing.T) {
	app, _, store := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(true))
	})
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", []byte(postRiskTopicBody), cookie)
	assertPostRiskRequired(t, resp, "create topic")
	if store.createdTopic.Title != "" {
		t.Fatalf("rejected create reached store: %#v", store.createdTopic)
	}
}

func TestCreateTopicPassesWhenPostRiskVerificationEnabledWithToken(t *testing.T) {
	app, _, store := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(true))
	})
	cookie := loginForumUser(t, app, 1)

	body := []byte(`{"categorySlug":"general","title":"verified topic","content":{"rawContent":"ordinary topic body","sourceFormat":"markdown","editorType":"markdown"},"humanVerification":{"provider":"altcha","token":"solved-payload"}}`)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", body, cookie)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create topic with token: status=%d body=%s", resp.StatusCode, raw)
	}
	if store.createdTopic.Title != "verified topic" {
		t.Fatalf("accepted create did not reach store: %#v", store.createdTopic)
	}
}

func TestCreateTopicSkipsWhenPostRiskVerificationDisabled(t *testing.T) {
	app, _, store := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(false))
	})
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", []byte(postRiskTopicBody), cookie)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create topic with scenario disabled: status=%d body=%s", resp.StatusCode, raw)
	}
	if store.createdTopic.Title != "post risk topic" {
		t.Fatalf("disabled scenario blocked the write: %#v", store.createdTopic)
	}
}

func TestCreateCommentRejectsWhenPostRiskVerificationEnabledWithoutToken(t *testing.T) {
	app, _, _ := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(true))
	})
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/comments", []byte(postRiskCommentBody), cookie)
	assertPostRiskRequired(t, resp, "create comment")
}

func TestCreateCommentPassesWhenPostRiskVerificationEnabledWithToken(t *testing.T) {
	app, _, _ := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(true))
	})
	cookie := loginForumUser(t, app, 1)

	body := []byte(`{"content":{"rawContent":"ordinary reply body","sourceFormat":"markdown","editorType":"markdown"},"humanVerification":{"provider":"altcha","token":"solved-payload"}}`)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/comments", body, cookie)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create comment with token: status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestCreateCommentSkipsWhenPostRiskVerificationDisabled(t *testing.T) {
	app, _, _ := newForumTestApp(func(controller *Controller) {
		controller.WithHumanVerification(postRiskVerifier(false))
	})
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/comments", []byte(postRiskCommentBody), cookie)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create comment with scenario disabled: status=%d body=%s", resp.StatusCode, raw)
	}
}

// TestCreateTopicAndCommentSkipWhenVerifierNotWired 守住「未接线 = 零校验」：
// 无人机验证依赖的装配（含既有测试）不能被新开关意外变成 fail-closed。
func TestCreateTopicAndCommentSkipWhenVerifierNotWired(t *testing.T) {
	app, _, _ := newForumTestApp()
	cookie := loginForumUser(t, app, 1)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", []byte(postRiskTopicBody), cookie)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create topic without verifier: status=%d", resp.StatusCode)
	}

	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/comments", []byte(postRiskCommentBody), cookie)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("create comment without verifier: status=%d", resp.StatusCode)
	}
}
