package profilecontroller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 核心资料字段的校验失败必须带 data.fields（键 = 前端字段名），否则前端
// 「错误→tab」链路在核心 tab 上完全不可用。
func TestUpdateProfileCoreFieldsReturnFieldErrors(t *testing.T) {
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true}, profileTestOptions{})
	cookie := loginProfileUser(t, app, 7)

	resp := performProfileRequest(t, app, http.MethodPut, "/api/v1/profile",
		[]byte(`{"websiteUrl":"not a url"}`), cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("single invalid field = %d", resp.StatusCode)
	}
	var failure struct {
		Data struct {
			Reason string              `json:"reason"`
			Fields map[string][]string `json:"fields"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Data.Reason != "profile.invalid" {
		t.Fatalf("reason must stay profile.invalid, got %q", failure.Data.Reason)
	}
	if len(failure.Data.Fields["websiteUrl"]) == 0 {
		t.Fatalf("websiteUrl field error missing: %#v", failure.Data.Fields)
	}

	// 多字段同时非法：一次返回全部（前端按 tab 汇总要用）。
	body, _ := json.Marshal(map[string]any{
		"websiteUrl": "still not a url",
		"bio":        strings.Repeat("x", 501),
	})
	resp = performProfileRequest(t, app, http.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("multi invalid = %d", resp.StatusCode)
	}
	failure.Data.Fields = nil
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("decode multi: %v", err)
	}
	if len(failure.Data.Fields["websiteUrl"]) == 0 || len(failure.Data.Fields["bio"]) == 0 {
		t.Fatalf("all invalid fields must be reported at once, got %#v", failure.Data.Fields)
	}

	// 合法提交照常 200。
	resp = performProfileRequest(t, app, http.MethodPut, "/api/v1/profile",
		[]byte(`{"websiteUrl":"https://example.com","bio":"hello"}`), cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid update = %d", resp.StatusCode)
	}
}
