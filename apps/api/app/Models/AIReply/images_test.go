package aireply

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

type fakeImageReader struct {
	// content 按 publicID 返回字节；err 按 publicID 返回读取失败。
	content map[string][]byte
	errs    map[string]error
}

func (f *fakeImageReader) ReadImage(_ context.Context, _ int64, publicID string, _ int64) (io.ReadCloser, error) {
	if err, ok := f.errs[publicID]; ok {
		return nil, err
	}
	data, ok := f.content[publicID]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestCollectImagesSkipsWhenDisabledOrEmpty(t *testing.T) {
	ctx := context.Background()
	reader := &fakeImageReader{content: map[string][]byte{"a": {1, 2, 3}}}
	attachments := []ImageAttachment{{PublicID: "a", ContentType: "image/png"}}

	if got := collectImages(ctx, nil, 1, attachments); got != nil {
		t.Fatalf("nil reader must yield nil, got %v", got)
	}
	if got := collectImages(ctx, reader, 1, nil); got != nil {
		t.Fatalf("no attachments must yield nil, got %v", got)
	}
}

func TestCollectImagesEncodesBase64AndMarksCount(t *testing.T) {
	ctx := context.Background()
	reader := &fakeImageReader{content: map[string][]byte{
		"a": []byte("png-bytes"),
	}}
	parts := collectImages(ctx, reader, 1, []ImageAttachment{
		{PublicID: "a", ContentType: "image/png"},
	})
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0].Type != supportai.PartImage || parts[0].Image == nil {
		t.Fatalf("expected image part, got %+v", parts[0])
	}
	if parts[0].Image.MediaType != "image/png" {
		t.Fatalf("unexpected media type: %s", parts[0].Image.MediaType)
	}
	if parts[0].Image.Data != base64.StdEncoding.EncodeToString([]byte("png-bytes")) {
		t.Fatalf("unexpected data: %s", parts[0].Image.Data)
	}
}

func TestCollectImagesSkipsFailuresAndInvalidTypes(t *testing.T) {
	ctx := context.Background()
	reader := &fakeImageReader{
		content: map[string][]byte{"ok": []byte("data"), "svg": []byte("<svg/>")},
		errs:    map[string]error{"broken": errors.New("storage down")},
	}
	attachments := []ImageAttachment{
		{PublicID: "broken", ContentType: "image/png"},
		{PublicID: "svg", ContentType: "image/svg+xml"},
		{PublicID: "ok", ContentType: "image/jpeg"},
	}
	parts := collectImages(ctx, reader, 1, attachments)
	if len(parts) != 1 || parts[0].Image.MediaType != "image/jpeg" {
		t.Fatalf("expected only the readable image, got %+v", parts)
	}
}

func TestCollectImagesEnforcesSizeLimit(t *testing.T) {
	ctx := context.Background()
	oversized := bytes.Repeat([]byte{1}, supportai.MaxImageBytes+1)
	reader := &fakeImageReader{content: map[string][]byte{"big": oversized, "ok": []byte("x")}}
	parts := collectImages(ctx, reader, 1, []ImageAttachment{
		{PublicID: "big", ContentType: "image/png"},
		{PublicID: "ok", ContentType: "image/png"},
	})
	if len(parts) != 1 {
		t.Fatalf("oversized image must be skipped, got %d parts", len(parts))
	}
}

func TestBuildUserPartsAnnotatesImageCount(t *testing.T) {
	parts := buildUserParts("你好", []supportai.Part{
		{Type: supportai.PartImage, Image: &supportai.ImageContent{MediaType: "image/png", Data: "aW1hZ2U="}},
	})
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[0].Type != supportai.PartText || !strings.Contains(parts[0].Text, "1 张图片") {
		t.Fatalf("text part must mention image count, got %+v", parts[0])
	}

	plain := buildUserParts("你好", nil)
	if len(plain) != 1 || strings.Contains(plain[0].Text, "图片") {
		t.Fatalf("no images must not annotate, got %+v", plain)
	}
}
