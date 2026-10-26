package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

const (
	ExternalDiagnosticRootUnavailable = "external.root_unavailable"
	ExternalDiagnosticPackageInvalid  = "external.package_invalid"
	ExternalDiagnosticIDConflict      = "external.id_conflict"
	ExternalDiagnosticBuiltinConflict = "external.builtin_conflict"
	ExternalDiagnosticSnapshotFailed  = "external.snapshot_failed"
)

// ExternalSourceDiagnostic 描述单个外部根或包的静态扫描失败。
// 诊断不会让 Host 执行不可信代码，也不会删除此前已安装的不可变制品。
type ExternalSourceDiagnostic struct {
	Code        string
	Root        string
	PackagePath string
	ExtensionID string
	Message     string
}

type ExternalSyncResult struct {
	Items       []Extension
	Diagnostics []ExternalSourceDiagnostic
}

type externalSourceCandidate struct {
	root          string
	packagePath   string
	extensionType string
	manifest      Manifest
}

// SyncExternalSources 扫描显式配置的第三方源码集合。
// 新包以 uploaded/installed 保存；已有活动包的不同摘要只进入 staged。
func (s *CatalogService) SyncExternalSources(ctx context.Context) (ExternalSyncResult, error) {
	var result ExternalSyncResult
	if s == nil || len(s.externalRoots) == 0 {
		return result, nil
	}
	if s.store == nil {
		return result, fmt.Errorf("sync external extensions: store is required")
	}

	candidates := make([]externalSourceCandidate, 0)
	byID := make(map[string][]int)
	groups := []struct {
		dir           string
		extensionType string
	}{
		{dir: "plugins", extensionType: TypePlugin},
		{dir: "themes", extensionType: TypeTheme},
	}

	for _, root := range s.externalRoots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			message := "not a directory"
			if err != nil {
				message = err.Error()
			}
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticRootUnavailable, Root: root, Message: message,
			})
			continue
		}
		for _, group := range groups {
			groupRoot := filepath.Join(root, group.dir)
			entries, err := os.ReadDir(groupRoot)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
					Code: ExternalDiagnosticRootUnavailable, Root: root,
					PackagePath: groupRoot, Message: err.Error(),
				})
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				packagePath := filepath.Join(groupRoot, entry.Name())
				manifest, err := extensionmanifest.LoadPackage(packagePath)
				if err != nil || validateManifest(manifest) != nil || manifest.Type != group.extensionType || manifest.ID == DefaultThemeID {
					if err == nil {
						err = ErrInvalidManifest
					}
					result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
						Code: ExternalDiagnosticPackageInvalid, Root: root,
						PackagePath: packagePath, ExtensionID: manifest.ID, Message: err.Error(),
					})
					continue
				}
				index := len(candidates)
				candidates = append(candidates, externalSourceCandidate{
					root: root, packagePath: packagePath,
					extensionType: group.extensionType, manifest: manifest,
				})
				byID[manifest.ID] = append(byID[manifest.ID], index)
			}
		}
	}

	conflicted := make(map[int]bool)
	for extensionID, indexes := range byID {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			conflicted[index] = true
			candidate := candidates[index]
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticIDConflict, Root: candidate.root,
				PackagePath: candidate.packagePath, ExtensionID: extensionID,
				Message: "extension id is declared by multiple external source packages",
			})
		}
	}

	for index, candidate := range candidates {
		if conflicted[index] {
			continue
		}
		existing, err := s.store.Get(ctx, candidate.manifest.ID)
		existingFound := err == nil
		switch {
		case existingFound && (existing.Source == SourceBuiltin || existing.IsSystem):
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticBuiltinConflict, Root: candidate.root,
				PackagePath: candidate.packagePath, ExtensionID: candidate.manifest.ID,
				Message: "external package cannot replace a built-in extension",
			})
			continue
		case err != nil && !errors.Is(err, ErrExtensionNotFound):
			return result, fmt.Errorf("load external extension %s: %w", candidate.manifest.ID, err)
		}

		owned, err := extensionpackage.SnapshotExternalSourceOwned(candidate.packagePath, s.extensionRoot)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticSnapshotFailed, Root: candidate.root,
				PackagePath: candidate.packagePath, ExtensionID: candidate.manifest.ID, Message: err.Error(),
			})
			continue
		}
		snapshot := owned.Snapshot
		var manifest Manifest
		if err := json.Unmarshal([]byte(snapshot.Manifest), &manifest); err != nil ||
			manifest.ID != candidate.manifest.ID || manifest.Type != candidate.extensionType {
			if err == nil {
				err = errors.New("package identity changed while scanning")
			}
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticPackageInvalid, Root: candidate.root,
				PackagePath: candidate.packagePath, ExtensionID: candidate.manifest.ID, Message: err.Error(),
			})
			_ = s.discardUnreferencedUploadedSnapshot(ctx, owned)
			_ = owned.Release()
			continue
		}
		if manifest.Type == TypeTheme {
			if err := themecompiler.NewCompiler(themecompiler.Limits{}).PreflightFS(os.DirFS(snapshot.Root)); err != nil {
				result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
					Code: ExternalDiagnosticPackageInvalid, Root: candidate.root,
					PackagePath: candidate.packagePath, ExtensionID: manifest.ID, Message: err.Error(),
				})
				_ = s.discardUnreferencedUploadedSnapshot(ctx, owned)
				_ = owned.Release()
				continue
			}
		}
		adminFrontendDigest, err := ComputeAdminFrontendDigest(manifest, snapshot.Root)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
				Code: ExternalDiagnosticPackageInvalid, Root: candidate.root,
				PackagePath: candidate.packagePath, ExtensionID: manifest.ID, Message: err.Error(),
			})
			_ = s.discardUnreferencedUploadedSnapshot(ctx, owned)
			_ = owned.Release()
			continue
		}

		sameExactArtifact := existingFound && externalArtifactRecorded(existing, manifest.Version, snapshot.Digest)
		reusable := existingFound && externalArtifactReusable(existing, manifest.Version, snapshot.Digest, snapshot.Root)
		if reusable {
			result.Items = append(result.Items, existing)
			_ = owned.Release()
			continue
		}
		installed, saveErr := s.store.SaveInstalled(ctx, SaveInstalledInput{
			Manifest: manifest, PackagePath: snapshot.Root, PackageDigest: snapshot.Digest,
			AdminFrontendDigest: adminFrontendDigest,
		})
		if saveErr != nil {
			_ = s.discardUnreferencedUploadedSnapshot(ctx, owned)
			_ = owned.Release()
			if errors.Is(saveErr, ErrNotDeletable) || errors.Is(saveErr, ErrInvalidManifest) {
				result.Diagnostics = append(result.Diagnostics, ExternalSourceDiagnostic{
					Code: ExternalDiagnosticBuiltinConflict, Root: candidate.root,
					PackagePath: candidate.packagePath, ExtensionID: manifest.ID, Message: saveErr.Error(),
				})
				continue
			}
			return result, fmt.Errorf("save external extension %s: %w", manifest.ID, saveErr)
		}
		_ = owned.Release()
		message := "External source package discovered and installed as an inert snapshot."
		action := EventInstalled
		switch {
		case sameExactArtifact:
			// 版本+摘要仍是同一件制品，只是记录路径不在本次 DigestTree 校验过的
			// 快照根下（制品目录被删、EXTENSION_ROOT 变化、手工改库）。这次保存
			// 会把 package_path 修回内容寻址根，是自愈而不是新内容。
			message = "External source artifact was missing or relocated on disk; re-published the verified immutable snapshot."
			action = EventVerified
		case existingFound:
			message = "External source package changed; immutable candidate staged for operator review."
			action = EventUpgraded
		}
		_, _ = s.store.CreateEvent(ctx, EventInput{
			ExtensionID: installed.ID, Action: action, Message: message,
		})
		result.Items = append(result.Items, installed)
	}

	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		left := strings.Join([]string{result.Diagnostics[i].Root, result.Diagnostics[i].PackagePath, result.Diagnostics[i].Code}, "\x00")
		right := strings.Join([]string{result.Diagnostics[j].Root, result.Diagnostics[j].PackagePath, result.Diagnostics[j].Code}, "\x00")
		return left < right
	})
	return result, nil
}

// externalArtifactRecorded 判断 DB 是否已经记录了这个精确制品（版本 + 摘要）。
func externalArtifactRecorded(existing Extension, version, digest string) bool {
	if existing.ID == "" {
		return false
	}
	if externalArtifactRecordMatches(existing.Version, existing.PackageDigest, version, digest) {
		return true
	}
	staged := existing.StagedVersion
	return staged != nil && externalArtifactRecordMatches(staged.Version, staged.PackageDigest, version, digest)
}

// externalArtifactReusable 判定既有记录能否直接复用本次的不可变快照。
//
// 版本 + 摘要相同并不证明制品还在盘上：快照发布已经用 DigestTree 校验过
// snapshot.Root，所以「记录路径等于 snapshot.Root」才是磁盘存在的证据。少了
// 这一条，制品目录被删或 EXTENSION_ROOT 变化后行会永久停在 missing，只能靠
// 换摘要或手工改库恢复（见 knowledge/sessions/2026-10-19-dev-artifact-self-heal.md）。
func externalArtifactReusable(existing Extension, version, digest, snapshotRoot string) bool {
	if existing.ID == "" {
		return false
	}
	reusable := false
	if externalArtifactRecordMatches(existing.Version, existing.PackageDigest, version, digest) {
		if !externalSnapshotPathMatches(existing.PackagePath, snapshotRoot) {
			return false
		}
		reusable = true
	}
	if staged := existing.StagedVersion; staged != nil &&
		externalArtifactRecordMatches(staged.Version, staged.PackageDigest, version, digest) {
		if !externalSnapshotPathMatches(staged.PackagePath, snapshotRoot) {
			return false
		}
		reusable = true
	}
	return reusable
}

func externalArtifactRecordMatches(recordVersion, recordDigest, version, digest string) bool {
	return recordVersion == version && strings.EqualFold(recordDigest, digest)
}

func externalSnapshotPathMatches(recorded, snapshotRoot string) bool {
	recorded = strings.TrimSpace(recorded)
	if recorded == "" || strings.TrimSpace(snapshotRoot) == "" {
		return false
	}
	return filepath.Clean(recorded) == filepath.Clean(snapshotRoot)
}
