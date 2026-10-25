package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	assetregistry "github.com/zhuchunshu/sforum/apps/api/app/Support/AssetRegistry"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	stablefile "github.com/zhuchunshu/sforum/apps/api/app/Support/StableFile"
	platformversion "github.com/zhuchunshu/sforum/apps/api/version"
)

var ErrPublicFrontendUnavailable = errors.New("extensions: public frontend is unavailable")

const (
	publicL2EntrySuffix          = ".l2.entry"
	maxPublicL2Assets            = 256
	maxPublicPackageResourceSize = 8 * 1024 * 1024
)

var publicPackageAssetContentTypes = map[string]string{
	".js":    "application/javascript; charset=utf-8",
	".mjs":   "application/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

func (s *FrontendService) PublicComponent(ctx context.Context, extensionID, componentID string) (PublicFrontendComponent, error) {
	if err := s.publicRuntimeGates(ctx, extensionID); err != nil {
		return PublicFrontendComponent{}, err
	}
	extensionID = normalizeID(extensionID)
	componentID = normalizeID(componentID)
	entryHandle := componentID + publicL2EntrySuffix
	// 请求先捕获不可变 Registry 发布物与依赖计划，再读取扩展元组和 live grant。
	ownerPublication, found := s.publicAssets.SnapshotPublication(extensionID)
	if !found {
		return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
	}
	plan, err := s.publicAssets.Plan(assetregistry.PlanRequest{Handles: []string{entryHandle}})
	if err != nil {
		return PublicFrontendComponent{}, errors.Join(ErrPublicFrontendUnavailable, err)
	}
	extension, err := s.extensions.Get(ctx, extensionID)
	if err != nil {
		return PublicFrontendComponent{}, s.failClosedPublicIdentity(ownerPublication.Artifact, err)
	}
	if err := s.executableTrust.ValidatePublishedIdentity(ctx, extension, ownerPublication.Artifact); err != nil {
		return PublicFrontendComponent{}, s.failClosedPublicIdentity(ownerPublication.Artifact, err)
	}
	component, ok := publicManifestComponent(extension.Manifest, componentID)
	if !ok || publicL2EntryHandle(component) != entryHandle {
		return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
	}
	if s.publicComponents == nil || !s.publicComponents.AdmitPublicComponent(extension, component) {
		return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
	}

	assets := make([]PublicFrontendAssetReference, 0, len(plan))
	var entry PublicFrontendAssetReference
	cspSet := map[string]struct{}{}
	validatedOwners := map[string]assetregistry.Artifact{}
	validatedOwners[ownerPublication.Artifact.ExtensionID] = ownerPublication.Artifact
	for _, asset := range plan {
		if err := s.ensurePlanOwnerTrusted(ctx, asset.Artifact, validatedOwners); err != nil {
			return PublicFrontendComponent{}, err
		}
		reference := publicAssetReference(asset)
		for _, declaration := range reference.CSP {
			cspSet[declaration] = struct{}{}
		}
		if asset.Handle == entryHandle {
			if !publicAssetMatchesPublication(asset, ownerPublication.Artifact) {
				return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
			}
			entry = reference
			continue
		}
		assets = append(assets, reference)
	}
	for ownerID, artifact := range validatedOwners {
		publication, found := s.publicAssets.SnapshotPublication(ownerID)
		if !found || publication.Artifact != artifact {
			return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
		}
	}
	if entry.Handle == "" || entry.Type != "script" || !entry.Module {
		return PublicFrontendComponent{}, ErrPublicFrontendUnavailable
	}
	csp := make([]string, 0, len(cspSet))
	for declaration := range cspSet {
		csp = append(csp, declaration)
	}
	sort.Strings(csp)
	return PublicFrontendComponent{
		SchemaVersion: PublicFrontendSchemaV1, APIVersion: PublicFrontendAPIVersion,
		TrustNotice: PublicFrontendTrustNotice, ExtensionID: extension.ID,
		ExtensionVersion: extension.Version, CoreVersion: platformversion.CoreCompatibilityVersion(),
		PackageDigest: extension.PackageDigest,
		ImpactDigest:  ownerPublication.Artifact.ImpactDigest, ComponentID: component.ID,
		ContractVersion: component.ContractVersion, Action: component.Action,
		TargetID: component.TargetID, TargetContractVersion: component.TargetContractVersion,
		PropsSchema: component.PropsSchema, ResultSchema: component.ResultSchema,
		Entry: entry, Assets: assets, CSP: csp,
	}, nil
}

func (s *FrontendService) PublicAsset(
	ctx context.Context,
	extensionID, packageDigest, digest, handle string,
) (FrontendAsset, error) {
	if err := s.publicRuntimeGates(ctx, extensionID); err != nil {
		return FrontendAsset{}, err
	}
	extensionID = normalizeID(extensionID)
	packageDigest = normalizedPublicDigest(packageDigest)
	digest = normalizedPublicDigest(digest)
	handle = normalizeID(handle)
	if packageDigest == "" || handle == "" || digest == "" {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	// 请求路径只读共享不可变快照；缺失或陈旧制品直接失败关闭。
	asset, ok := s.publicAssets.Resolve(handle)
	if !ok || asset.Artifact.ExtensionID != extensionID ||
		asset.Artifact.PackageDigest != packageDigest || asset.Digest != digest {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	publication, found := s.publicAssets.SnapshotPublication(extensionID)
	if !found || publication.Artifact != asset.Artifact {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	extension, err := s.extensions.Get(ctx, extensionID)
	if err != nil {
		return FrontendAsset{}, s.failClosedPublicIdentity(asset.Artifact, err)
	}
	if err := s.executableTrust.ValidatePublishedIdentity(ctx, extension, asset.Artifact); err != nil {
		return FrontendAsset{}, s.failClosedPublicIdentity(asset.Artifact, err)
	}
	target, ok := installedFilePath(extension, asset.Path)
	if !ok {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	limit := int64(maxPrebuiltAdminModuleBytes)
	contentType := "application/javascript; charset=utf-8"
	if asset.Type == "style" {
		limit = maxPrebuiltAdminCSSBytes
		contentType = "text/css; charset=utf-8"
	}
	if !validPublicAssetExtension(asset.Type, target) {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	body, actualDigest, readErr := readExactExtensionDigestFile(extension, asset.Path, asset.Digest, limit, false)
	if readErr != nil {
		if errors.Is(readErr, ErrFrontendPackageChanged) {
			return FrontendAsset{}, s.failClosedPublicIdentity(
				asset.Artifact, errors.Join(ErrPublicFrontendUnavailable, ErrFrontendPackageChanged),
			)
		}
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	return FrontendAsset{
		Body: body, ContentType: contentType, ETag: `"` + actualDigest + `"`,
		Digest: actualDigest, Integrity: asset.Integrity, CSP: append([]string(nil), asset.CSP...),
	}, nil
}

// PublicPackageAsset serves package-local resources from their original URL
// directory. Native ESM relative imports, import.meta.url, CSS url(), and
// package-local @import therefore keep browser semantics without a runtime
// source rewrite. Only exact manifest declarations cross this boundary.
func (s *FrontendService) PublicPackageAsset(
	ctx context.Context,
	extensionID, packageDigest, packagePath string,
) (FrontendAsset, error) {
	if err := s.publicRuntimeGates(ctx, extensionID); err != nil {
		return FrontendAsset{}, err
	}
	extensionID = normalizeID(extensionID)
	packageDigest = normalizedPublicDigest(packageDigest)
	packagePath = strings.TrimPrefix(strings.TrimSpace(packagePath), "/")
	if packageDigest == "" || !safePublicPackagePath(packagePath) {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	publication, found := s.publicAssets.SnapshotPublication(extensionID)
	if !found || publication.Artifact.PackageDigest != packageDigest {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	extension, err := s.extensions.Get(ctx, extensionID)
	if err != nil {
		return FrontendAsset{}, s.failClosedPublicIdentity(publication.Artifact, err)
	}
	// asset-only owner 也可服务 exact 声明的包内文件，但必须先通过 live grant。
	if !hasPublicAssetPayload(extension.Manifest) {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	if err := s.executableTrust.ValidatePublishedIdentity(ctx, extension, publication.Artifact); err != nil {
		return FrontendAsset{}, s.failClosedPublicIdentity(publication.Artifact, err)
	}
	file, ok := publicPackageFileByPath(extension.Manifest, packagePath)
	if !ok {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	contentType, ok := publicPackageFileContentType(file)
	if !ok {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	return readExactPublicPackageFile(extension, file, contentType, func(cause error) error {
		return s.failClosedPublicIdentity(publication.Artifact, cause)
	})
}

// RestorePublicAssetPublications 由 Host 启动/Safe Mode 路径调用，发布完整确定性
// exact-artifact 集合。请求路径不得调用本方法。
func (s *FrontendService) RestorePublicAssetPublications(
	ctx context.Context,
	items []Extension,
	safeMode bool,
) error {
	if s == nil || s.publicAssets == nil {
		return ErrPublicFrontendUnavailable
	}
	publications, err := s.buildPublicAssetPublications(ctx, items, safeMode)
	if err != nil {
		return err
	}
	_, err = s.publicAssets.ReplaceAll(publications)
	return err
}

// RestorePublicAssetPublicationsIfRevision 为生命周期/多节点收敛提供调用方 fenced 的完整图替换。
// 本方法不读取当前 revision；陈旧调用方会收到 AssetRegistry.ErrRevisionConflict。
func (s *FrontendService) RestorePublicAssetPublicationsIfRevision(
	ctx context.Context,
	expectedRevision uint64,
	items []Extension,
	safeMode bool,
) error {
	if s == nil || s.publicAssets == nil {
		return ErrPublicFrontendUnavailable
	}
	publications, err := s.buildPublicAssetPublications(ctx, items, safeMode)
	if err != nil {
		return err
	}
	_, err = s.publicAssets.ReplaceAllIfRevision(expectedRevision, publications)
	return err
}

func (s *FrontendService) buildPublicAssetPublications(
	ctx context.Context,
	items []Extension,
	safeMode bool,
) ([]assetregistry.Publication, error) {
	if safeMode {
		return []assetregistry.Publication{}, nil
	}
	if ctx == nil || s == nil || s.executableTrust == nil {
		return nil, ErrPublicFrontendUnavailable
	}
	publications := make([]assetregistry.Publication, 0, len(items))
	for _, extension := range items {
		if extension.Status != StatusEnabled || !hasPublicAssetPayload(extension.Manifest) {
			continue
		}
		identity, err := s.executableTrust.RuntimeIdentity(ctx, extension)
		if errors.Is(err, ErrTrustGrantNotFound) || errors.Is(err, ErrFrontendPackageChanged) {
			// 未信任、已吊销或字节漂移的 owner 不进入快照；依赖方在整图校验时失败关闭。
			continue
		}
		if err != nil {
			return nil, err
		}
		publication, err := BuildPublicAssetPublication(extension, identity.ImpactDigest)
		if err != nil {
			return nil, err
		}
		publications = append(publications, publication)
	}
	return publications, nil
}

func (s *FrontendService) publicRuntimeGates(ctx context.Context, extensionID string) error {
	extensionID = normalizeID(extensionID)
	if s == nil || ctx == nil || s.safeMode || !s.v3TrustChallenges ||
		s.extensions == nil || s.executableTrust == nil || s.publicAssets == nil || extensionID == "" {
		return ErrPublicFrontendUnavailable
	}
	return nil
}

// ensurePlanOwnerTrusted 校验 plan 中每个 dependency owner 的 live 发布身份。
// 任一 owner 失效时隔离其闭包，避免吊销/未信任依赖的 CSP 残留在 descriptor 中。
func (s *FrontendService) ensurePlanOwnerTrusted(
	ctx context.Context,
	artifact assetregistry.Artifact,
	validated map[string]assetregistry.Artifact,
) error {
	if previous, ok := validated[artifact.ExtensionID]; ok {
		if previous != artifact {
			return ErrPublicFrontendUnavailable
		}
		return nil
	}
	publication, found := s.publicAssets.SnapshotPublication(artifact.ExtensionID)
	if !found || publication.Artifact != artifact {
		return ErrPublicFrontendUnavailable
	}
	if artifact.OwnerKind == assetregistry.OwnerKindCore {
		if err := s.executableTrust.ValidatePublishedIdentity(ctx, Extension{}, artifact); err != nil {
			return s.failClosedPublicIdentity(artifact, err)
		}
		validated[artifact.ExtensionID] = artifact
		return nil
	}
	extension, err := s.extensions.Get(ctx, artifact.ExtensionID)
	if err != nil {
		return s.failClosedPublicIdentity(artifact, err)
	}
	if err := s.executableTrust.ValidatePublishedIdentity(ctx, extension, artifact); err != nil {
		return s.failClosedPublicIdentity(artifact, err)
	}
	validated[artifact.ExtensionID] = artifact
	return nil
}

func (s *FrontendService) failClosedPublicIdentity(artifact assetregistry.Artifact, cause error) error {
	if cause != nil && !errors.Is(cause, ErrTrustGrantNotFound) &&
		!errors.Is(cause, ErrFrontendPackageChanged) &&
		!errors.Is(cause, ErrPublicFrontendUnavailable) &&
		!errors.Is(cause, ErrExtensionNotFound) {
		return errors.Join(ErrPublicFrontendUnavailable, cause)
	}
	if s == nil || s.publicAssets == nil {
		return errors.Join(ErrPublicFrontendUnavailable, cause)
	}
	_, _, quarantineErr := s.publicAssets.QuarantineExact(artifact)
	if quarantineErr != nil {
		return errors.Join(ErrPublicFrontendUnavailable, cause, quarantineErr)
	}
	if cause == nil {
		return ErrPublicFrontendUnavailable
	}
	return errors.Join(ErrPublicFrontendUnavailable, cause)
}

func publicManifestComponent(manifest Manifest, componentID string) (ManifestComponent, bool) {
	manifest = extensionmanifest.Normalize(manifest)
	for _, component := range manifest.Components {
		if component.ID == componentID && component.L2Component != "" && component.Action != extensionmanifest.ComponentActionHide {
			return component, true
		}
	}
	return ManifestComponent{}, false
}

// BuildPublicAssetPublication 构造 exact-artifact 资产发布物，供生命周期与启动恢复复用。
// 允许 asset-only provider（仅 script/style 声明、无 L2 component）和
// Editor Registry 已声明的 exact L2 module。
// OwnerKind 只由 Extension.Type 推导；Host core publication 不经此构造器。
func BuildPublicAssetPublication(
	extension Extension,
	impactDigest string,
) (assetregistry.Publication, error) {
	impactDigest = normalizedPublicDigest(impactDigest)
	if impactDigest == "" || !hasPublicAssetPayload(extension.Manifest) {
		return assetregistry.Publication{}, ErrPublicFrontendUnavailable
	}
	manifest := extensionmanifest.Normalize(extension.Manifest)
	packageFiles := make(map[string]ManifestPackageFile, len(manifest.PackageFiles))
	packagePaths := make(map[string]ManifestPackageFile, len(manifest.PackageFiles))
	for _, file := range manifest.PackageFiles {
		packageFiles[file.ID] = file
		packagePaths[file.Path] = file
	}
	declarations := make([]assetregistry.Declaration, 0, maxPublicL2Assets)
	for _, asset := range manifest.Assets {
		if asset.Type != "script" && asset.Type != "style" {
			continue
		}
		declaration := assetregistry.Declaration{
			Handle: asset.Handle, ContractVersion: asset.ContractVersion, Type: asset.Type,
			Path: asset.Path, Digest: asset.Digest, Dependencies: append([]string(nil), asset.Dependencies...),
			Scope: append([]string(nil), asset.Scope...), Module: asset.Module,
			Loading: asset.Loading, Integrity: asset.Integrity, CSP: append([]string(nil), asset.CSP...),
		}
		declarations = append(declarations, declaration)
	}
	for _, component := range manifest.Components {
		if component.L2Component == "" || component.Action == extensionmanifest.ComponentActionHide {
			continue
		}
		entryFile, ok := packageFiles[component.L2Component]
		if !ok || entryFile.Kind != "frontend" || !validPublicAssetExtension("script", entryFile.Path) {
			return assetregistry.Publication{}, ErrPublicFrontendUnavailable
		}
		scopes := []string{component.ID}
		if component.TargetID != "" {
			scopes = append(scopes, component.TargetID)
		}
		entryDependencies := make([]string, 0, len(manifest.Assets))
		for _, asset := range manifest.Assets {
			if len(asset.Scope) == 0 || stringSlicesIntersect(asset.Scope, scopes) {
				entryDependencies = append(entryDependencies, asset.Handle)
			}
		}
		declarations = append(declarations, assetregistry.Declaration{
			Handle: publicL2EntryHandle(component), ContractVersion: publicL2EntryContractVersion(component), Type: "script",
			Path: entryFile.Path, Digest: entryFile.Digest, Dependencies: entryDependencies,
			Scope: scopes, Module: true, Loading: "lazy",
		})
	}
	editorModules := make(map[string]extensionmanifest.ManifestEditor)
	for _, editor := range manifest.Editor {
		if editor.L2Module == "" {
			continue
		}
		current, exists := editorModules[editor.L2Module]
		if !exists || editor.ID < current.ID {
			editorModules[editor.L2Module] = editor
		}
	}
	editorModulePaths := make([]string, 0, len(editorModules))
	for modulePath := range editorModules {
		editorModulePaths = append(editorModulePaths, modulePath)
	}
	sort.Strings(editorModulePaths)
	for _, modulePath := range editorModulePaths {
		editor := editorModules[modulePath]
		moduleFile, ok := packagePaths[modulePath]
		if !ok || moduleFile.Kind != "frontend" || moduleFile.Digest != editor.L2Digest ||
			!validPublicAssetExtension("script", moduleFile.Path) {
			return assetregistry.Publication{}, ErrPublicFrontendUnavailable
		}
		declarations = append(declarations, assetregistry.Declaration{
			Handle: editor.ID, ContractVersion: editor.ContractVersion, Type: "script",
			Path: moduleFile.Path, Digest: moduleFile.Digest, Scope: []string{editor.ID},
			Module: true, Loading: "lazy",
		})
	}
	if len(declarations) == 0 || len(declarations) > maxPublicL2Assets {
		return assetregistry.Publication{}, ErrPublicFrontendUnavailable
	}
	ownerKind, ok := publicAssetOwnerKind(extension)
	if !ok || strings.HasPrefix(normalizeID(extension.ID), "core.") {
		return assetregistry.Publication{}, ErrPublicFrontendUnavailable
	}
	return assetregistry.Publication{Artifact: assetregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: impactDigest,
		OwnerKind: ownerKind,
	}, Assets: declarations}, nil
}

// publicAssetOwnerKind 将扩展类型映射为 Asset Registry OwnerKind。
func publicAssetOwnerKind(extension Extension) (string, bool) {
	switch extension.Type {
	case TypePlugin:
		return assetregistry.OwnerKindPlugin, true
	case TypeTheme:
		return assetregistry.OwnerKindTheme, true
	default:
		return "", false
	}
}

func publicL2EntryHandle(component ManifestComponent) string {
	return component.ID + publicL2EntrySuffix
}

func publicL2EntryContractVersion(component ManifestComponent) string {
	_, major, ok := strings.Cut(component.ContractVersion, "@")
	if !ok || major == "" {
		return ""
	}
	return publicL2EntryHandle(component) + "@" + major
}

func publicAssetReference(asset assetregistry.Asset) PublicFrontendAssetReference {
	assetPath := fmt.Sprintf(
		"/_sforum/assets/extensions/%s/%s/%s",
		url.PathEscape(asset.Artifact.ExtensionID), url.PathEscape(asset.Artifact.PackageDigest),
		escapePublicPackagePath(asset.Path),
	)
	return PublicFrontendAssetReference{
		Handle: asset.Handle, ContractVersion: asset.ContractVersion,
		ExtensionID: asset.Artifact.ExtensionID, PackageDigest: asset.Artifact.PackageDigest,
		ImpactDigest: asset.Artifact.ImpactDigest, Type: asset.Type, Digest: asset.Digest,
		Integrity: asset.Integrity, Dependencies: append([]string(nil), asset.Dependencies...),
		Scope: append([]string(nil), asset.Scope...), Module: asset.Module, Loading: asset.Loading,
		CSP: append([]string(nil), asset.CSP...), AssetPath: assetPath,
	}
}

func publicAssetMatchesPublication(asset assetregistry.Asset, artifact assetregistry.Artifact) bool {
	return asset.Artifact == artifact
}

func publicPackageFileByPath(manifest Manifest, packagePath string) (ManifestPackageFile, bool) {
	manifest = extensionmanifest.Normalize(manifest)
	for _, file := range manifest.PackageFiles {
		if file.Path == packagePath {
			return file, true
		}
	}
	return ManifestPackageFile{}, false
}

func publicPackageFileContentType(file ManifestPackageFile) (string, bool) {
	if file.Kind != "frontend" && file.Kind != "asset" {
		return "", false
	}
	extension := strings.ToLower(path.Ext(file.Path))
	contentType, ok := publicPackageAssetContentTypes[extension]
	if !ok {
		return "", false
	}
	if file.Kind == "frontend" && extension != ".js" && extension != ".mjs" {
		return "", false
	}
	return contentType, true
}

func readExactPublicPackageFile(
	extension Extension,
	file ManifestPackageFile,
	contentType string,
	onDrift func(error) error,
) (FrontendAsset, error) {
	if _, ok := installedFilePath(extension, file.Path); !ok {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	expectedDigest := normalizedPublicDigest(file.Digest)
	if expectedDigest == "" {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	body, actualDigest, err := readExactExtensionDigestFile(
		extension, file.Path, expectedDigest, maxPublicPackageResourceSize, true,
	)
	if err != nil {
		if errors.Is(err, ErrFrontendPackageChanged) && onDrift != nil {
			return FrontendAsset{}, onDrift(errors.Join(ErrPublicFrontendUnavailable, ErrFrontendPackageChanged))
		}
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	raw, decodeErr := hex.DecodeString(actualDigest)
	if decodeErr != nil {
		return FrontendAsset{}, ErrPublicFrontendUnavailable
	}
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(raw)
	return FrontendAsset{
		Body: body, ContentType: contentType, ETag: `"` + actualDigest + `"`,
		Digest: actualDigest, Integrity: integrity,
	}, nil
}

func readExactExtensionDigestFile(
	extension Extension,
	manifestPath, expectedDigest string,
	limit int64,
	requireNonEmpty bool,
) ([]byte, string, error) {
	expectedDigest = normalizedPublicDigest(expectedDigest)
	if expectedDigest == "" || limit <= 0 {
		return nil, "", ErrPublicFrontendUnavailable
	}
	body, err := readStableExtensionFile(extension, manifestPath, limit, requireNonEmpty)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	actualDigest := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actualDigest, expectedDigest) {
		return nil, "", ErrFrontendPackageChanged
	}
	return body, actualDigest, nil
}

// ReadExactExtensionDigestFile 暴露既有 exact-package 读取边界给 Host 子系统复用。
// 实现保持 OpenRoot、SameFile、O_NONBLOCK、LimitReader 与 digest 校验，不重写安全逻辑。
func ReadExactExtensionDigestFile(
	extension Extension,
	manifestPath, expectedDigest string,
	limit int64,
	requireNonEmpty bool,
) ([]byte, string, error) {
	return readExactExtensionDigestFile(extension, manifestPath, expectedDigest, limit, requireNonEmpty)
}

// readStableRegularFile 在打开前拒绝非常规文件，并用非阻塞 flag
// 关闭 Lstat/Open 之间被替换为 FIFO 的竞态。
func readStableRegularFile(target string, limit int64, requireNonEmpty bool) ([]byte, error) {
	pathBefore, err := os.Lstat(target)
	if err != nil || !regularPath(pathBefore) {
		return nil, ErrPublicFrontendUnavailable
	}
	file, err := stablefile.OpenReadFile(target)
	if err != nil {
		return nil, ErrPublicFrontendUnavailable
	}
	defer file.Close()
	return readOpenedStableRegularFileWithPath(
		file, pathBefore, func() (os.FileInfo, error) { return os.Lstat(target) }, limit, requireNonEmpty,
	)
}

func readOpenedStableRegularFile(file *os.File, target string, limit int64, requireNonEmpty bool) ([]byte, error) {
	if file == nil || target == "" {
		return nil, ErrPublicFrontendUnavailable
	}
	pathBefore, pathErr := os.Lstat(target)
	if pathErr != nil {
		return nil, ErrPublicFrontendUnavailable
	}
	return readOpenedStableRegularFileWithPath(
		file, pathBefore, func() (os.FileInfo, error) { return os.Lstat(target) }, limit, requireNonEmpty,
	)
}

func readStableExtensionFile(extension Extension, manifestPath string, limit int64, requireNonEmpty bool) ([]byte, error) {
	opened, err := openStableExtensionRegularFile(extension, manifestPath)
	if err != nil {
		return nil, err
	}
	defer opened.close()
	return readOpenedStableRegularFileWithPath(
		opened.file, opened.pathBefore, opened.lstat, limit, requireNonEmpty,
	)
}

type openedStableExtensionFile struct {
	root       *os.Root
	file       *os.File
	name       string
	pathBefore os.FileInfo
}

func openStableExtensionRegularFile(extension Extension, manifestPath string) (*openedStableExtensionFile, error) {
	name, ok := safeArchivePath(manifestPath)
	rootPath := PackageContentRoot(extension)
	if !ok || rootPath == "" {
		return nil, ErrPublicFrontendUnavailable
	}
	rootBefore, err := os.Lstat(rootPath)
	if err != nil || !rootBefore.IsDir() || rootBefore.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPublicFrontendUnavailable
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, ErrPublicFrontendUnavailable
	}
	openedRoot, err := root.Stat(".")
	if err != nil || !openedRoot.IsDir() || !os.SameFile(rootBefore, openedRoot) {
		_ = root.Close()
		return nil, ErrPublicFrontendUnavailable
	}
	pathBefore, err := root.Lstat(name)
	if err != nil || !regularPath(pathBefore) {
		_ = root.Close()
		return nil, ErrPublicFrontendUnavailable
	}
	file, err := stablefile.OpenRootReadFile(root, name)
	if err != nil {
		_ = root.Close()
		return nil, ErrPublicFrontendUnavailable
	}
	return &openedStableExtensionFile{root: root, file: file, name: name, pathBefore: pathBefore}, nil
}

func (f *openedStableExtensionFile) close() {
	if f == nil {
		return
	}
	if f.file != nil {
		_ = f.file.Close()
	}
	if f.root != nil {
		_ = f.root.Close()
	}
}

func (f *openedStableExtensionFile) lstat() (os.FileInfo, error) {
	if f == nil || f.root == nil {
		return nil, ErrPublicFrontendUnavailable
	}
	return f.root.Lstat(f.name)
}

func readOpenedStableRegularFileWithPath(
	file *os.File,
	pathBefore os.FileInfo,
	lstat func() (os.FileInfo, error),
	limit int64,
	requireNonEmpty bool,
) ([]byte, error) {
	fdBefore, readLimit, err := stableOpenedRegularFileBefore(file, pathBefore, lstat, limit, requireNonEmpty)
	if err != nil {
		return nil, ErrPublicFrontendUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(file, readLimit+1))
	if err != nil || int64(len(body)) > readLimit || int64(len(body)) != fdBefore.Size() {
		return nil, ErrPublicFrontendUnavailable
	}
	if err := stableOpenedRegularFileAfter(file, pathBefore, fdBefore, lstat); err != nil {
		return nil, err
	}
	return body, nil
}

func digestStableExtensionFile(extension Extension, manifestPath string, limit int64, requireNonEmpty bool) (string, error) {
	opened, err := openStableExtensionRegularFile(extension, manifestPath)
	if err != nil {
		return "", err
	}
	defer opened.close()
	fdBefore, readLimit, err := stableOpenedRegularFileBefore(
		opened.file, opened.pathBefore, opened.lstat, limit, requireNonEmpty,
	)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, err := io.CopyBuffer(
		hasher,
		io.LimitReader(opened.file, readLimit+1),
		make([]byte, 32*1024),
	)
	if err != nil || written > readLimit || written != fdBefore.Size() {
		return "", ErrPublicFrontendUnavailable
	}
	if err := stableOpenedRegularFileAfter(opened.file, opened.pathBefore, fdBefore, opened.lstat); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func stableOpenedRegularFileBefore(
	file *os.File,
	pathBefore os.FileInfo,
	lstat func() (os.FileInfo, error),
	limit int64,
	requireNonEmpty bool,
) (os.FileInfo, int64, error) {
	if file == nil || pathBefore == nil || lstat == nil {
		return nil, 0, ErrPublicFrontendUnavailable
	}
	fdBefore, fdErr := file.Stat()
	if fdErr != nil || !stableRegularFile(pathBefore, fdBefore) || !os.SameFile(pathBefore, fdBefore) {
		return nil, 0, ErrPublicFrontendUnavailable
	}
	if requireNonEmpty && fdBefore.Size() <= 0 {
		return nil, 0, ErrPublicFrontendUnavailable
	}
	if limit <= 0 {
		limit = fdBefore.Size()
	}
	if fdBefore.Size() > limit || limit == int64(^uint64(0)>>1) {
		return nil, 0, ErrPublicFrontendUnavailable
	}
	return fdBefore, limit, nil
}

func stableOpenedRegularFileAfter(
	file *os.File,
	pathBefore, fdBefore os.FileInfo,
	lstat func() (os.FileInfo, error),
) error {
	fdAfter, fdErr := file.Stat()
	pathAfter, pathErr := lstat()
	if fdErr != nil || pathErr != nil || !stableRegularFile(pathAfter, fdAfter) ||
		!os.SameFile(fdBefore, fdAfter) || !os.SameFile(pathBefore, pathAfter) ||
		!os.SameFile(fdAfter, pathAfter) || !sameStableFileMetadata(fdBefore, fdAfter) {
		return ErrPublicFrontendUnavailable
	}
	return nil
}

func regularPath(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func stableRegularFile(pathInfo, fdInfo os.FileInfo) bool {
	return pathInfo != nil && fdInfo != nil && pathInfo.Mode().IsRegular() &&
		pathInfo.Mode()&os.ModeSymlink == 0 && fdInfo.Mode().IsRegular() &&
		pathInfo.Size() == fdInfo.Size() && pathInfo.Mode() == fdInfo.Mode() &&
		pathInfo.ModTime().Equal(fdInfo.ModTime())
}

func sameStableFileMetadata(before, after os.FileInfo) bool {
	return before.Size() == after.Size() && before.Mode() == after.Mode() &&
		before.ModTime().Equal(after.ModTime())
}

func safePublicPackagePath(value string) bool {
	return value != "" && len(value) <= 512 && !strings.Contains(value, "\\") &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value &&
		value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func escapePublicPackagePath(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func stringSlicesIntersect(left, right []string) bool {
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[normalizeID(value)] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[normalizeID(value)]; ok {
			return true
		}
	}
	return false
}

func validPublicAssetExtension(assetType, name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	if assetType == "style" {
		return extension == ".css"
	}
	return extension == ".js" || extension == ".mjs"
}

func normalizedPublicDigest(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func hasPublicAssetPayload(manifest Manifest) bool {
	return hasL2Components(manifest) || hasAssetRegistryDeclarations(manifest) || hasEditorL2Modules(manifest)
}

func hasEditorL2Modules(manifest Manifest) bool {
	for _, editor := range manifest.Editor {
		if strings.TrimSpace(editor.L2Module) != "" && normalizedPublicDigest(editor.L2Digest) != "" {
			return true
		}
	}
	return false
}

func hasAssetRegistryDeclarations(manifest Manifest) bool {
	for _, asset := range manifest.Assets {
		typ := strings.ToLower(strings.TrimSpace(asset.Type))
		if typ == "script" || typ == "style" {
			return true
		}
	}
	return false
}
