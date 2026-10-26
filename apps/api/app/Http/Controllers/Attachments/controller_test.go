package attachmentscontroller

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
)

type attachmentStreamReader struct {
	*bytes.Reader
	closed bool
}

func (r *attachmentStreamReader) Read(buffer []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	return r.Reader.Read(buffer)
}

func (r *attachmentStreamReader) Close() error {
	r.closed = true
	return nil
}

func TestSendAttachmentContentKeepsStreamOpenUntilResponseWrite(t *testing.T) {
	app := fiber.New()
	reader := &attachmentStreamReader{Reader: bytes.NewReader([]byte("image-body"))}
	app.Get("/image", func(c fiber.Ctx) error {
		return sendAttachmentContent(c, "image/png", "photo.png", reader)
	})

	response, err := app.Test(httptest.NewRequest("GET", "/image", nil))
	if err != nil {
		t.Fatalf("request attachment stream: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read attachment stream: %v", err)
	}
	if string(body) != "image-body" {
		t.Fatalf("attachment body = %q, want %q", body, "image-body")
	}
	if got := response.Header.Get("Content-Disposition"); got != `inline; filename="photo.png"` {
		t.Fatalf("content disposition = %q", got)
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q", got)
	}
}

func TestSendAttachmentDeliveryRedirectsWithoutBuffering(t *testing.T) {
	app := fiber.New()
	app.Get("/image", func(c fiber.Ctx) error {
		return sendAttachmentDelivery(c, attachments.ContentDelivery{
			RedirectURL: "https://objects.example.test/signed?token=abc",
		})
	})

	response, err := app.Test(httptest.NewRequest("GET", "/image", nil))
	if err != nil {
		t.Fatalf("request attachment redirect: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusFound {
		t.Fatalf("redirect status = %d, want %d", response.StatusCode, fiber.StatusFound)
	}
	if got := response.Header.Get(fiber.HeaderLocation); got != "https://objects.example.test/signed?token=abc" {
		t.Fatalf("redirect location = %q", got)
	}
	if got := response.Header.Get(fiber.HeaderCacheControl); got != "private, no-store" {
		t.Fatalf("redirect cache-control = %q", got)
	}
}

// TestAttachmentContentDispositionForcesDownloadOnActiveContent 验证 content 响应对
// 主动内容类型强制下载（attachment），对安全类型保持内联（inline）。
// 这是审计 P2 附件主动内容风险的响应层兜底防护。
func TestAttachmentContentDispositionForcesDownloadOnActiveContent(t *testing.T) {
	for _, mime := range []string{"text/html", "image/svg+xml", "application/javascript"} {
		got := attachmentContentDisposition(mime, "file.html")
		if want := `attachment; filename="file.html"`; got != want {
			t.Fatalf("active content %q: disposition = %q, want %q", mime, got, want)
		}
	}
}

// TestAttachmentContentDispositionInlinesSafeContent 验证安全 MIME 保持 inline，
// 不误伤正常图片/PDF/文本附件的浏览体验。
func TestAttachmentContentDispositionInlinesSafeContent(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "application/pdf", "text/plain"} {
		got := attachmentContentDisposition(mime, "photo.png")
		if want := `inline; filename="photo.png"`; got != want {
			t.Fatalf("safe content %q: disposition = %q, want %q", mime, got, want)
		}
	}
}

// TestAttachmentContentDispositionSanitizesFilename 验证文件名中的双引号被剔除，
// 防止通过文件名注入额外的 disposition 参数。
func TestAttachmentContentDispositionSanitizesFilename(t *testing.T) {
	got := attachmentContentDisposition("image/png", `evil".txt`)
	// 期望双引号被移除：filename 段内只保留闭合引号，无内嵌引号。
	want := `inline; filename="evil.txt"`
	if got != want {
		t.Fatalf("unsanitized filename, got %q, want %q", got, want)
	}
}

func TestAttachmentUploadErrorsUseSpecificHTTPReasons(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantReason string
	}{
		{err: &attachments.FileTooLargeError{ActualBytes: 2, MaxBytes: 1}, wantStatus: fiber.StatusRequestEntityTooLarge, wantReason: attachments.CodeFileTooLarge},
		{err: uploadpolicy.ErrInvalidPolicy, wantStatus: fiber.StatusUnprocessableEntity, wantReason: attachments.CodeUploadPolicyInvalid},
		{err: uploadpolicy.ErrProtectedActor, wantStatus: fiber.StatusUnprocessableEntity, wantReason: attachments.CodeUploadPolicyProtected},
	}
	for _, test := range tests {
		mapped := mapAttachmentError(test.err)
		var fiberError *fiber.Error
		if !errors.As(mapped, &fiberError) {
			t.Fatalf("expected Fiber error for %v, got %T", test.err, mapped)
		}
		if fiberError.Code != test.wantStatus || fiberError.Message != test.wantReason {
			t.Fatalf("mapped %v to %d/%q", test.err, fiberError.Code, fiberError.Message)
		}
	}
}
