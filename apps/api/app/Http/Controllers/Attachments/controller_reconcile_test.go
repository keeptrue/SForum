package attachmentscontroller

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func newReconcileDecodeApp() *fiber.App {
	app := fiber.New()
	app.Post("/reconcile", func(c fiber.Ctx) error {
		dryRun, err := decodeReconcileRequest(c)
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"dryRun": dryRun})
	})
	return app
}

// 契约里 requestBody 是可选的，而 fiber 的 Bind().Body() 对「空 body + 无
// Content-Type」直接返回 422。这条用例钉住 handler 的缺省语义：空 body 必须
// 落成 dry-run（只读），同时非空但非法/类型不符仍然是 400。
func TestDecodeReconcileRequestDefaultsToDryRun(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantDryRun  bool
	}{
		{name: "empty body without content type", body: "", contentType: "", wantStatus: 200, wantDryRun: true},
		{name: "empty json object", body: "{}", contentType: "application/json", wantStatus: 200, wantDryRun: true},
		{name: "explicit dry run", body: `{"dryRun":true}`, contentType: "application/json", wantStatus: 200, wantDryRun: true},
		{name: "explicit apply", body: `{"dryRun":false}`, contentType: "application/json", wantStatus: 200, wantDryRun: false},
		{name: "malformed json", body: `{"dryRun":`, contentType: "application/json", wantStatus: 400},
		{name: "body without content type", body: `{"dryRun":false}`, contentType: "", wantStatus: 400},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/reconcile", strings.NewReader(tc.body))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			response, err := newReconcileDecodeApp().Test(request)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.wantStatus {
				payload, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d (body %q)", response.StatusCode, tc.wantStatus, payload)
			}
			if tc.wantStatus != 200 {
				return
			}
			var decoded struct {
				DryRun bool `json:"dryRun"`
			}
			if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if decoded.DryRun != tc.wantDryRun {
				t.Fatalf("dryRun = %t, want %t", decoded.DryRun, tc.wantDryRun)
			}
		})
	}
}
