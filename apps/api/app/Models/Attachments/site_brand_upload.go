package attachments

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"math"
	"path/filepath"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	siteBrandSVGMaxBytes     = 2 * 1024 * 1024
	siteBrandSVGMaxDimension = 1024
	siteBrandSVGMinDimension = 256
)

// UploadSiteBrandImage 复用附件存储链路，但由站点设置权限授权并强制公开图片。
func (s *Service) UploadSiteBrandImage(ctx context.Context, actor identity.Actor, input UploadInput) (Attachment, error) {
	if !actor.Can(identity.PermissionSettingsSiteManage) {
		return Attachment{}, identity.ErrPermissionDenied
	}
	settings, err := s.runtimeSettings(ctx)
	if err != nil {
		return Attachment{}, err
	}
	if !settings.UploadEnabled {
		return Attachment{}, ErrUploadDisabled
	}
	if input.File == nil || input.SizeBytes <= 0 || input.SizeBytes > int64(settings.MaxFileSizeMB)*1024*1024 {
		return Attachment{}, ErrInvalidAttachment
	}
	var prepared preparedUpload
	if strings.EqualFold(filepath.Ext(input.OriginalName), ".svg") {
		prepared, err = prepareSiteBrandSVG(input, settings)
	} else {
		var metadata uploadMetadata
		metadata, err = inspectUpload(input, settings)
		if err == nil && !strings.HasPrefix(metadata.ContentType, "image/") {
			err = ErrInvalidAttachment
		}
		prepared = preparedUpload{
			Reader: input.File, SizeBytes: input.SizeBytes, Metadata: metadata, Visibility: VisibilityPublic,
		}
	}
	if err != nil {
		return Attachment{}, ErrInvalidAttachment
	}
	if err := s.applyMediaRegistryMIME("site-brand", prepared.Metadata); err != nil {
		return Attachment{}, err
	}
	return s.storePreparedUpload(ctx, actor, settings, prepared)
}

func prepareSiteBrandSVG(input UploadInput, settings AttachmentSettings) (preparedUpload, error) {
	if input.SizeBytes > siteBrandSVGMaxBytes {
		return preparedUpload{}, ErrInvalidAttachment
	}
	if _, err := input.File.Seek(0, io.SeekStart); err != nil {
		return preparedUpload{}, ErrInvalidAttachment
	}
	source, err := io.ReadAll(io.LimitReader(input.File, siteBrandSVGMaxBytes+1))
	if err != nil || len(source) == 0 || len(source) > siteBrandSVGMaxBytes {
		return preparedUpload{}, ErrInvalidAttachment
	}
	encoded, width, height, err := rasterizeSiteBrandSVG(source)
	if err != nil || int64(len(encoded)) > int64(settings.MaxFileSizeMB)*1024*1024 {
		return preparedUpload{}, ErrInvalidAttachment
	}
	name := strings.TrimSuffix(strings.TrimSpace(filepath.Base(input.OriginalName)), filepath.Ext(input.OriginalName))
	if name == "" || name == "." {
		name = "brand"
	}
	return preparedUpload{
		Reader:     &memoryReadSeekCloser{Reader: bytes.NewReader(encoded)},
		SizeBytes:  int64(len(encoded)),
		Visibility: VisibilityPublic,
		Metadata: uploadMetadata{
			OriginalName: name + ".png",
			ContentType:  "image/png",
			Extension:    ".png",
			ImageWidth:   &width,
			ImageHeight:  &height,
		},
	}, nil
}

func rasterizeSiteBrandSVG(source []byte) (encoded []byte, width, height int, err error) {
	defer func() {
		if recover() != nil {
			encoded, width, height, err = nil, 0, 0, ErrInvalidAttachment
		}
	}()
	icon, err := oksvg.ReadIconStream(bytes.NewReader(source), oksvg.IgnoreErrorMode)
	if err != nil || icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 || len(icon.SVGPaths) == 0 ||
		math.IsNaN(icon.ViewBox.W) || math.IsNaN(icon.ViewBox.H) || math.IsInf(icon.ViewBox.W, 0) || math.IsInf(icon.ViewBox.H, 0) {
		return nil, 0, 0, ErrInvalidAttachment
	}
	longest := math.Max(icon.ViewBox.W, icon.ViewBox.H)
	targetLongest := math.Max(siteBrandSVGMinDimension, math.Min(siteBrandSVGMaxDimension, longest))
	width = max(1, int(math.Round(icon.ViewBox.W/longest*targetLongest)))
	height = max(1, int(math.Round(icon.ViewBox.H/longest*targetLongest)))

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	icon.SetTarget(0, 0, float64(width), float64(height))
	icon.Draw(rasterx.NewDasher(width, height, rasterx.NewScannerGV(width, height, canvas, canvas.Bounds())), 1)
	if !hasVisiblePixels(canvas) {
		return nil, 0, 0, ErrInvalidAttachment
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, 0, 0, ErrInvalidAttachment
	}
	return output.Bytes(), width, height, nil
}

func hasVisiblePixels(canvas *image.RGBA) bool {
	for offset := 3; offset < len(canvas.Pix); offset += 4 {
		if canvas.Pix[offset] != 0 {
			return true
		}
	}
	return false
}

type memoryReadSeekCloser struct {
	*bytes.Reader
}

func (*memoryReadSeekCloser) Close() error { return nil }
