package extensionscontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensiondownload "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionDownload"
)

const (
	CodeRemoteURLInvalid         = "extension.remote_url_invalid"
	CodeRemoteFetchFailed        = "extension.remote_fetch_failed"
	CodeRemoteArchiveTooLarge    = "extension.remote_archive_too_large"
	CodeRemoteDigestMismatch     = "extension.remote_digest_mismatch"
	CodeRemoteInstallUnavailable = "extension.remote_install_unavailable"
)

// ArchiveFetcher downloads an extension ZIP from a URL. The concrete
// implementation is extensiondownload.Downloader; tests inject fakes.
type ArchiveFetcher interface {
	Fetch(context.Context, string) (extensiondownload.Archive, error)
}

// archiveInstaller is the subset of the extension service used by URL install.
// The controller holds the real service in production and a fake in tests.
type archiveInstaller interface {
	InstallOrUpgradeArchive(context.Context, identity.Actor, extensions.ArchiveInput) (extensions.InstallResult, error)
}

type remoteInstallInput struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// 可选期望身份：声明后宿主在校验清单时必须逐项对上，否则中止。
	ExpectedExtensionID string `json:"expectedExtensionId"`
	ExpectedVersion     string `json:"expectedVersion"`
	ExpectedType        string `json:"expectedType"`
	// Source 是审计来源标签（例如 admin-page:sforum.store）。
	Source string `json:"source"`
}

// expectedTypeAllowed maps a declared package type to nothing; the value is
// validated here and re-checked against the manifest by the service layer.
func expectedTypeAllowed(value string) bool {
	return value == extensions.TypePlugin || value == extensions.TypeTheme
}

// installFromURL downloads a public HTTPS ZIP and installs it as an inert
// package. Authorization is checked before the outbound request so an actor
// without install authority cannot use this route as a network probe.
func (h *Controller) installFromURL(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !extensions.CanManageAnyArchive(actor) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.remoteArchives == nil || h.installer == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, CodeRemoteInstallUnavailable)
	}

	var input remoteInstallInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	expectedDigest := strings.ToLower(strings.TrimSpace(input.SHA256))
	if expectedDigest != "" && !isSHA256Hex(expectedDigest) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	expected, err := normalizeInstallExpectations(input)
	if err != nil {
		return err
	}

	archive, err := h.remoteArchives.Fetch(c.Context(), strings.TrimSpace(input.URL))
	if err != nil {
		return mapExtensionError(err)
	}
	if expectedDigest != "" && !strings.EqualFold(expectedDigest, archive.Digest) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeRemoteDigestMismatch)
	}

	result, err := h.installer.InstallOrUpgradeArchive(c.Context(), actor, extensions.ArchiveInput{
		FileName:            archive.FileName,
		Data:                archive.Data,
		SourceURL:           archive.SourceURL,
		SourceDigest:        archive.Digest,
		ExpectedExtensionID: expected.extensionID,
		ExpectedVersion:     expected.version,
		ExpectedType:        expected.packageType,
		SourceLabel:         expected.sourceLabel,
	})
	if err != nil {
		if mismatch := mapArchiveExpectationError(err); mismatch != nil {
			return mismatch
		}
		return mapExtensionError(err)
	}

	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return apphttp.Created(c, result)
}

// mapRemoteInstallError maps download-boundary errors to stable API reasons.
// It returns nil for unrelated errors so mapExtensionError can continue.
func mapRemoteInstallError(err error) error {
	switch {
	case errors.Is(err, extensiondownload.ErrInvalidURL):
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeRemoteURLInvalid)
	case errors.Is(err, extensiondownload.ErrTooLarge):
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, CodeRemoteArchiveTooLarge)
	case errors.Is(err, extensiondownload.ErrNotArchive):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeInvalidArchive)
	case errors.Is(err, extensiondownload.ErrFetchFailed):
		return fiber.NewError(fiber.StatusBadGateway, CodeRemoteFetchFailed)
	default:
		return nil
	}
}

type installExpectations struct {
	extensionID string
	version     string
	packageType string
	sourceLabel string
}

// normalizeInstallExpectations validates caller-declared identity before the
// outbound request. Empty values keep the manual URL install path working; the
// admin-page bridge always sends at least the id and the type.
func normalizeInstallExpectations(input remoteInstallInput) (installExpectations, error) {
	var expected installExpectations
	expected.extensionID = strings.ToLower(strings.TrimSpace(input.ExpectedExtensionID))
	if expected.extensionID != "" && !extensions.ValidExtensionID(expected.extensionID) {
		return installExpectations{}, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	expected.version = strings.TrimSpace(input.ExpectedVersion)
	if expected.version != "" && !isExpectedVersion(expected.version) {
		return installExpectations{}, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	expected.packageType = strings.ToLower(strings.TrimSpace(input.ExpectedType))
	if expected.packageType != "" && !expectedTypeAllowed(expected.packageType) {
		return installExpectations{}, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if rawSource := input.Source; rawSource != "" {
		if !extensions.ValidSourceLabel(rawSource) {
			return installExpectations{}, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
		expected.sourceLabel = rawSource
	}
	return expected, nil
}

// isExpectedVersion bounds a caller-declared version to the characters a
// manifest version may use, so junk cannot reach the audit log.
func isExpectedVersion(value string) bool {
	if len(value) > 64 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= '0' && character <= '9':
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character == '.' || character == '-' || character == '+' || character == '_':
		default:
			return false
		}
	}
	return true
}

// mapArchiveExpectationError answers a declared-identity mismatch with the
// actual manifest values, so the page can show what the package really is
// instead of a bare code.
func mapArchiveExpectationError(err error) error {
	var mismatch *extensions.ArchiveExpectationMismatchError
	if !errors.As(err, &mismatch) {
		return nil
	}
	return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, extensions.CodeExpectedMismatch, map[string][]string{
		"fields":          mismatch.Fields,
		"expectedId":      {mismatch.ExpectedExtensionID},
		"expectedVersion": {mismatch.ExpectedVersion},
		"expectedType":    {mismatch.ExpectedType},
		"manifestId":      {mismatch.ActualExtensionID},
		"manifestVersion": {mismatch.ActualVersion},
		"manifestType":    {mismatch.ActualType},
	})
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// archiveSourceDigest records the exact ZIP bytes for uploads so both install
// paths carry the same audit provenance. An empty body has no digest.
func archiveSourceDigest(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
