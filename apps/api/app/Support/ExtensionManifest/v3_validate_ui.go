package extensionmanifest

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
)

func (v *v3Validator) validateUIAndPackage() error {
	if len(v.manifest.Content) > ContentDeclarationsMaximum {
		return fmt.Errorf("%w: v3 validateUIAndPackage rule #1", ErrInvalidManifest)
	}
	packageFiles := map[string]ManifestPackageFile{}
	packagePaths := map[string]ManifestPackageFile{}
	targetedSchemas := map[string]string{}
	for _, provider := range v.manifest.Providers {
		if provider.TargetID == "" {
			continue
		}
		for _, reference := range []string{provider.RequestSchema, provider.ResponseSchema} {
			separator := strings.LastIndex(reference, "@")
			if separator > 0 && separator < len(reference)-1 {
				targetedSchemas[reference[:separator]] = reference[separator+1:]
			}
		}
	}
	for _, file := range v.manifest.PackageFiles {
		foreignTargetedSchema := file.Kind == "schema" && targetedSchemas[file.ID] == file.Version
		if !manifestIDPattern.MatchString(file.ID) || !strings.HasPrefix(file.ID, v.manifest.ID+".") && !foreignTargetedSchema {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #2", ErrInvalidManifest)
		}
		if _, duplicate := packageFiles[file.ID]; duplicate {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #3", ErrInvalidManifest)
		}
		if _, duplicate := packagePaths[file.Path]; duplicate {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #4", ErrInvalidManifest)
		}
		if file.Path == ManifestFileName || !validPackagePath(file.Path) || !validDigest(file.Digest) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #5", ErrInvalidManifest)
		}
		switch file.Kind {
		case "executable", "frontend", "locale", "schema", "migration", "template", "asset", "openapi", "database_operation":
		default:
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #6", ErrInvalidManifest)
		}
		if file.Kind == "locale" && file.Locale == "" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #7", ErrInvalidManifest)
		}
		packageFiles[file.ID] = file
		packagePaths[file.Path] = file
	}
	if backend := v.manifest.Backend; backend.Entry != "" && !matchingPackageFile(packagePaths, backend.Entry, "executable", backend.Digest) {
		return fmt.Errorf("%w: v3 validateUIAndPackage rule #8", ErrInvalidManifest)
	}
	for _, guard := range v.manifest.Guards {
		if !matchingPackageFile(packagePaths, guard.Entry, "executable", guard.Digest) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #9", ErrInvalidManifest)
		}
	}
	for _, query := range v.manifest.Queries {
		if query.Handler != "" && validContractVersion(query.ResultSchema) && !matchingVersionedSchemaFile(packageFiles, query.ResultSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #10", ErrInvalidManifest)
		}
	}
	for _, declaration := range v.manifest.NotificationTypes {
		if !matchingVersionedSchemaFile(packageFiles, declaration.PayloadSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #11", ErrInvalidManifest)
		}
	}
	for _, migration := range v.manifest.Migrations {
		if !matchingPackageFile(packagePaths, migration.Path, "migration", migration.Digest) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #12", ErrInvalidManifest)
		}
	}
	if database := v.manifest.Database; database != nil {
		for _, operation := range database.Operations {
			if !matchingPackageFile(packagePaths, operation.Path, "database_operation", operation.Digest) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #13", ErrInvalidManifest)
			}
		}
	}
	for _, provider := range v.manifest.Providers {
		if provider.RequestSchema == "" && provider.ResponseSchema == "" {
			continue
		}
		if !matchingVersionedSchemaFile(packageFiles, provider.RequestSchema) ||
			!matchingVersionedSchemaFile(packageFiles, provider.ResponseSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #14", ErrInvalidManifest)
		}
	}
	if identity := v.manifest.Identity; identity != nil {
		for _, provider := range identity.Providers {
			for _, operation := range provider.Operations {
				for _, reference := range []string{operation.InputSchema, operation.OutputSchema} {
					if validContractVersion(reference) && !matchingVersionedSchemaFile(packageFiles, reference) {
						return fmt.Errorf("%w: v3 validateUIAndPackage rule #15", ErrInvalidManifest)
					}
				}
			}
		}
	}

	// 模板按规范化 ID 唯一登记；后续组件 SSR 与 content renderer 都只解析同包声明。
	templates := map[string]ManifestTemplate{}
	for _, template := range v.manifest.Templates {
		if err := v.versionedID(template.ID, template.ContractVersion, "template"); err != nil {
			return err
		}
		if template.Action != "add" && template.Action != "replace" || template.Action == "replace" && template.TargetID == "" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #16", ErrInvalidManifest)
		}
		if !validPackagePath(template.Path) || !validDigest(template.Digest) || !validSchemaRef(template.ViewModelSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #17", ErrInvalidManifest)
		}
		file, declared := packagePaths[template.Path]
		if !declared || file.Kind != "template" || file.Digest != template.Digest {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #18", ErrInvalidManifest)
		}
		templates[template.ID] = template
	}

	for _, asset := range v.manifest.Assets {
		if err := v.versionedID(asset.Handle, asset.ContractVersion, "asset"); err != nil {
			return err
		}
		if asset.Type != "script" && asset.Type != "style" || !validPackagePath(asset.Path) || !validDigest(asset.Digest) ||
			asset.Type == "style" && asset.Module || !validPrebuiltAssetPath(asset.Type, asset.Path) ||
			!validAssetIntegrity(asset.Digest, asset.Integrity) || !validAssetCSP(asset.CSP) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #19", ErrInvalidManifest)
		}
		file, declared := packagePaths[asset.Path]
		if !declared || file.Kind != "asset" || file.Digest != asset.Digest {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #20", ErrInvalidManifest)
		}
		switch asset.Loading {
		case "", "blocking", "defer", "async", "preload", "lazy":
		default:
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #21", ErrInvalidManifest)
		}
		seenDependencies := map[string]struct{}{}
		for _, dependency := range asset.Dependencies {
			if !manifestIDPattern.MatchString(dependency) || dependency == asset.Handle {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #22", ErrInvalidManifest)
			}
			if _, duplicate := seenDependencies[dependency]; duplicate {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #23", ErrInvalidManifest)
			}
			seenDependencies[dependency] = struct{}{}
		}
		seenScopes := map[string]struct{}{}
		for _, scope := range asset.Scope {
			if !manifestIDPattern.MatchString(scope) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #24", ErrInvalidManifest)
			}
			if _, duplicate := seenScopes[scope]; duplicate {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #25", ErrInvalidManifest)
			}
			seenScopes[scope] = struct{}{}
		}
	}
	for _, asset := range v.manifest.Assets {
		for _, dependency := range asset.Dependencies {
			family, declared := v.ids[dependency]
			if declared && family != "asset" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #26", ErrInvalidManifest)
			}
			if !declared && !strings.HasPrefix(dependency, "core.asset.") &&
				!requiredExtensionOwnsAsset(v.manifest.Dependencies, dependency) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #27", ErrInvalidManifest)
			}
		}
	}

	for _, component := range v.manifest.Components {
		if err := v.versionedID(component.ID, component.ContractVersion, "component"); err != nil {
			return err
		}
		if !validComponentAction(component.Action) || component.Action != ComponentActionAdd && component.TargetID == "" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #28", ErrInvalidManifest)
		}
		if !validComponentTarget(component.TargetID, component.TargetContractVersion, v.manifest.Type) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #29", ErrInvalidManifest)
		}
		if component.Permission != "" && !manifestIDPattern.MatchString(component.Permission) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #30", ErrInvalidManifest)
		}
		if component.Action != ComponentActionHide && !validSchemaRef(component.PropsSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #31", ErrInvalidManifest)
		}
		if (component.Action == ComponentActionFilterResult || component.Action == ComponentActionWrap || component.Action == ComponentActionReplace) && !validSchemaRef(component.ResultSchema) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #32", ErrInvalidManifest)
		}
		// 组件 props/result schema 与 provider/query/notification 采用同一条精确声明
		// 规则：引用必须在本包的 packageFiles[] 中有 id+version 对齐的 kind=schema 条目。
		// 组件注册表编译（compileExactProviderSchema）按同一条规则拒绝，缺了这里，
		// CLI 会放行一个运行时激活必然失败的主题/插件包，作者只看到一句泛化文案。
		for _, reference := range []string{component.PropsSchema, component.ResultSchema} {
			if reference == "" {
				continue
			}
			if !matchingVersionedSchemaFile(packageFiles, reference) {
				return fmt.Errorf(
					"%w: component %s schema %s has no exact packageFiles schema declaration",
					ErrInvalidManifest, component.ID, reference,
				)
			}
		}
		if component.Action != ComponentActionHide && component.SSRTemplate == "" && component.L2Component == "" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #33", ErrInvalidManifest)
		}
		// 非空 ssrTemplate 必须解析到同包唯一模板，并与 props/override 契约精确一致。
		if component.SSRTemplate != "" {
			template, declared := templates[component.SSRTemplate]
			if !declared || !componentSSRTemplateConsistent(component, template) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #34", ErrInvalidManifest)
			}
		}
		if component.L2Component != "" {
			file, exists := packageFiles[component.L2Component]
			if !exists || file.Kind != "frontend" || !validPrebuiltAssetPath("script", file.Path) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #35", ErrInvalidManifest)
			}
		}
	}

	for _, content := range v.manifest.Content {
		if err := v.versionedID(content.ID, content.ContractVersion, "content"); err != nil {
			return err
		}
		switch content.Kind {
		case "block", "shortcode", "embed", "node", "mark", "render_filter", "sanitizer":
		default:
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #36", ErrInvalidManifest)
		}
		if !validSchemaRef(content.Schema) ||
			(content.Handler != "" && !validHandler(content.Handler)) ||
			(content.Handler != "" && v.manifest.Backend.Entry == "") ||
			content.Handler == "" && content.Renderer == "" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #37", ErrInvalidManifest)
		}
		if content.Renderer != "" {
			if _, declared := templates[content.Renderer]; !declared {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #38", ErrInvalidManifest)
			}
		}
		if content.Migration != "" && v.ids[content.Migration] != "migration" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #39", ErrInvalidManifest)
		}
	}

	// Tiptap Editor Registry 声明：node/mark 绑定 prebuilt L2；toolbar 仅引用同包 command。
	if len(v.manifest.Editor) > 256 {
		return fmt.Errorf("%w: v3 validateUIAndPackage rule #40", ErrInvalidManifest)
	}
	editorCommands := map[string]struct{}{}
	for _, editor := range v.manifest.Editor {
		if err := v.versionedID(editor.ID, editor.ContractVersion, "editor"); err != nil {
			return err
		}
		if editor.Permission != "" && !manifestIDPattern.MatchString(editor.Permission) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #41", ErrInvalidManifest)
		}
		switch editor.Kind {
		case "node", "mark":
			if !validSchemaRef(editor.Schema) || editor.ExtensionName == "" ||
				!validPackagePath(editor.L2Module) || !validDigest(editor.L2Digest) ||
				!validPrebuiltAssetPath("script", editor.L2Module) ||
				editor.CommandKey != "" || editor.CommandID != "" || editor.Label != "" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #42", ErrInvalidManifest)
			}
			file, declared := packagePaths[editor.L2Module]
			if !declared || file.Kind != "frontend" || file.Digest != editor.L2Digest {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #43", ErrInvalidManifest)
			}
		case "command":
			if editor.CommandKey == "" || editor.Schema != "" || editor.CommandID != "" || editor.Label != "" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #44", ErrInvalidManifest)
			}
			if editor.L2Module != "" || editor.L2Digest != "" {
				if !validPackagePath(editor.L2Module) || !validDigest(editor.L2Digest) ||
					!validPrebuiltAssetPath("script", editor.L2Module) {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #45", ErrInvalidManifest)
				}
				file, declared := packagePaths[editor.L2Module]
				if !declared || file.Kind != "frontend" || file.Digest != editor.L2Digest {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #46", ErrInvalidManifest)
				}
			}
			editorCommands[editor.ID] = struct{}{}
		case "toolbar":
			if editor.CommandID == "" || editor.Label == "" ||
				editor.Schema != "" || editor.ExtensionName != "" ||
				editor.L2Module != "" || editor.L2Digest != "" || editor.CommandKey != "" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #47", ErrInvalidManifest)
			}
			if !strings.HasPrefix(editor.CommandID, v.manifest.ID+".") {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #48", ErrInvalidManifest)
			}
		default:
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #49", ErrInvalidManifest)
		}
	}
	for _, editor := range v.manifest.Editor {
		if editor.Kind != "toolbar" {
			continue
		}
		if _, ok := editorCommands[editor.CommandID]; !ok {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #50", ErrInvalidManifest)
		}
	}

	// Entity/Taxonomy/Field Schema Registry：同包交叉引用；字段/分类必须绑定本包实体。
	if len(v.manifest.Entities) > 256 {
		return fmt.Errorf("%w: v3 validateUIAndPackage rule #51", ErrInvalidManifest)
	}
	entityIDs := map[string]struct{}{}
	taxonomyIDs := map[string]struct{}{}
	for _, entity := range v.manifest.Entities {
		if err := v.versionedID(entity.ID, entity.ContractVersion, "entity"); err != nil {
			return err
		}
		switch entity.Kind {
		case "entity":
			if entity.Label == "" || entity.StorageKey == "" ||
				!strings.HasPrefix(entity.StorageKey, v.manifest.ID+".") ||
				!manifestIDPattern.MatchString(entity.PermissionCreate) ||
				!manifestIDPattern.MatchString(entity.PermissionRead) ||
				!manifestIDPattern.MatchString(entity.PermissionUpdate) ||
				!manifestIDPattern.MatchString(entity.PermissionDelete) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #52", ErrInvalidManifest)
			}
			switch entity.ImportExportPolicy {
			case "allow", "deny", "export_only", "import_only":
			default:
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #53", ErrInvalidManifest)
			}
			switch entity.DeletionPolicy {
			case "soft", "hard", "retain":
			default:
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #54", ErrInvalidManifest)
			}
			if entity.ImportExportPolicy == "allow" || entity.ImportExportPolicy == "import_only" {
				if !manifestIDPattern.MatchString(entity.PermissionImport) {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #55", ErrInvalidManifest)
				}
			} else if entity.PermissionImport != "" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #56", ErrInvalidManifest)
			}
			if entity.ImportExportPolicy == "allow" || entity.ImportExportPolicy == "export_only" {
				if !manifestIDPattern.MatchString(entity.PermissionExport) {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #57", ErrInvalidManifest)
				}
			} else if entity.PermissionExport != "" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #58", ErrInvalidManifest)
			}
			if entity.EntityID != "" || entity.Schema != "" || entity.UIComponent != "" ||
				entity.UIModule != "" || entity.UIDigest != "" || entity.Validation != "" ||
				entity.PermissionManage != "" || entity.PermissionAssign != "" ||
				entity.PermissionFieldRead != "" || entity.PermissionFieldWrite != "" ||
				entity.IndexKind != "" || entity.Indexed || entity.Required || entity.Hierarchical ||
				len(entity.EntityIDs) > 0 {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #59", ErrInvalidManifest)
			}
			entityIDs[entity.ID] = struct{}{}
		case "taxonomy":
			if entity.Label == "" || entity.StorageKey == "" ||
				!strings.HasPrefix(entity.StorageKey, v.manifest.ID+".") ||
				!manifestIDPattern.MatchString(entity.PermissionManage) ||
				!manifestIDPattern.MatchString(entity.PermissionAssign) ||
				len(entity.EntityIDs) == 0 {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #60", ErrInvalidManifest)
			}
			for _, entityID := range entity.EntityIDs {
				if !strings.HasPrefix(entityID, v.manifest.ID+".") {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #61", ErrInvalidManifest)
				}
			}
			if entity.PermissionCreate != "" || entity.PermissionRead != "" ||
				entity.PermissionUpdate != "" || entity.PermissionDelete != "" ||
				entity.PermissionImport != "" || entity.PermissionExport != "" ||
				entity.ImportExportPolicy != "" || entity.DeletionPolicy != "" ||
				entity.EntityID != "" || entity.Schema != "" || entity.UIComponent != "" ||
				entity.UIModule != "" || entity.UIDigest != "" || entity.Validation != "" ||
				entity.PermissionFieldRead != "" || entity.PermissionFieldWrite != "" ||
				entity.IndexKind != "" || entity.Indexed || entity.Required ||
				len(entity.TaxonomyIDs) > 0 {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #62", ErrInvalidManifest)
			}
			taxonomyIDs[entity.ID] = struct{}{}
		case "field":
			// EntityID 可为本包实体，或 required 依赖包拥有的实体（plugin-extend-plugin）。
			if entity.EntityID == "" || !manifestIDPattern.MatchString(entity.EntityID) ||
				!validSchemaRef(entity.Schema) || entity.UIComponent == "" ||
				!manifestIDPattern.MatchString(entity.PermissionFieldRead) ||
				!manifestIDPattern.MatchString(entity.PermissionFieldWrite) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #63", ErrInvalidManifest)
			}
			indexKind := entity.IndexKind
			if indexKind == "" {
				if entity.Indexed {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #64", ErrInvalidManifest)
				}
				indexKind = "none"
			}
			switch indexKind {
			case "none", "keyword", "text", "numeric", "boolean":
			default:
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #65", ErrInvalidManifest)
			}
			if entity.Indexed && indexKind == "none" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #66", ErrInvalidManifest)
			}
			if !entity.Indexed && indexKind != "none" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #67", ErrInvalidManifest)
			}
			if entity.UIModule != "" || entity.UIDigest != "" {
				if !validPackagePath(entity.UIModule) || !validDigest(entity.UIDigest) {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #68", ErrInvalidManifest)
				}
				file, declared := packagePaths[entity.UIModule]
				if !declared || file.Kind != "frontend" || file.Digest != entity.UIDigest {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #69", ErrInvalidManifest)
				}
			}
			if entity.Validation != "" && !validSchemaRef(entity.Validation) {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #70", ErrInvalidManifest)
			}
			if entity.StorageKey != "" || entity.PermissionCreate != "" || entity.PermissionRead != "" ||
				entity.PermissionUpdate != "" || entity.PermissionDelete != "" ||
				entity.PermissionImport != "" || entity.PermissionExport != "" ||
				entity.ImportExportPolicy != "" || entity.DeletionPolicy != "" ||
				entity.PermissionManage != "" || entity.PermissionAssign != "" ||
				entity.Hierarchical || len(entity.EntityIDs) > 0 || len(entity.TaxonomyIDs) > 0 {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #71", ErrInvalidManifest)
			}
		default:
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #72", ErrInvalidManifest)
		}
	}
	for _, entity := range v.manifest.Entities {
		switch entity.Kind {
		case "field":
			if strings.HasPrefix(entity.EntityID, v.manifest.ID+".") {
				if _, ok := entityIDs[entity.EntityID]; !ok {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #73", ErrInvalidManifest)
				}
			} else if !manifestHasRequiredEntityOwnerDependency(v.manifest, entity.EntityID) {
				// 跨包字段扩展必须声明 required 依赖，版本约束由 Package Graph 校验。
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #74", ErrInvalidManifest)
			}
		case "taxonomy":
			for _, entityID := range entity.EntityIDs {
				if strings.HasPrefix(entityID, v.manifest.ID+".") {
					if _, ok := entityIDs[entityID]; !ok {
						return fmt.Errorf("%w: v3 validateUIAndPackage rule #75", ErrInvalidManifest)
					}
				} else if !manifestHasRequiredEntityOwnerDependency(v.manifest, entityID) {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #76", ErrInvalidManifest)
				}
			}
		case "entity":
			for _, taxonomyID := range entity.TaxonomyIDs {
				if _, ok := taxonomyIDs[taxonomyID]; !ok {
					return fmt.Errorf("%w: v3 validateUIAndPackage rule #77", ErrInvalidManifest)
				}
			}
		}
	}

	for _, fragment := range v.manifest.OpenAPI {
		if err := v.versionedID(fragment.ID, fragment.ContractVersion, "openapi"); err != nil {
			return err
		}
		if !validPackagePath(fragment.Path) || !validDigest(fragment.Digest) || !manifestIDPattern.MatchString(fragment.Namespace) {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #78", ErrInvalidManifest)
		}
		file, declared := packagePaths[fragment.Path]
		if !declared || file.Kind != "openapi" || file.Digest != fragment.Digest {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #79", ErrInvalidManifest)
		}
	}

	for _, binding := range DeclaredAdminComponents(v.manifest) {
		component := binding.Component
		entry, declared := packagePaths[component.Entry]
		if !declared || entry.Kind != "frontend" {
			return fmt.Errorf("%w: v3 validateUIAndPackage rule #80", ErrInvalidManifest)
		}
		if component.CSS != "" {
			css, declared := packagePaths[component.CSS]
			if !declared || css.Kind != "asset" {
				return fmt.Errorf("%w: v3 validateUIAndPackage rule #81", ErrInvalidManifest)
			}
		}
	}
	if !v.allLocalSchemasDeclared(packagePaths) {
		return fmt.Errorf("%w: v3 validateUIAndPackage rule #82", ErrInvalidManifest)
	}
	return nil
}

func requiredExtensionOwnsAsset(dependencies []ManifestDependency, handle string) bool {
	for _, dependency := range dependencies {
		if dependency.Kind == "required" && dependency.ID != "" &&
			strings.HasPrefix(handle, dependency.ID+".") {
			return true
		}
	}
	return false
}

// manifestHasRequiredEntityOwnerDependency 要求跨包 entity/field/taxonomy 绑定
// 声明 required 依赖，且目标实体 ID 以依赖扩展 ID 为前缀（与 Query result filter 一致）。
func manifestHasRequiredEntityOwnerDependency(manifest Manifest, entityID string) bool {
	entityID = strings.ToLower(strings.TrimSpace(entityID))
	if entityID == "" {
		return false
	}
	for _, dependency := range manifest.Dependencies {
		if dependency.Kind != "required" || dependency.ID == "" {
			continue
		}
		owner := strings.ToLower(strings.TrimSpace(dependency.ID))
		if owner != "" && strings.HasPrefix(entityID, owner+".") &&
			validSemverConstraint(dependency.Version) {
			return true
		}
	}
	return false
}

func validPrebuiltAssetPath(assetType, value string) bool {
	extension := strings.ToLower(filepath.Ext(value))
	if assetType == "style" {
		return extension == ".css"
	}
	return extension == ".js" || extension == ".mjs"
}

func validAssetIntegrity(digest, integrity string) bool {
	if integrity == "" {
		return true
	}
	raw, err := hex.DecodeString(strings.TrimSpace(digest))
	if err != nil || len(raw) != 32 {
		return false
	}
	return integrity == "sha256-"+base64.StdEncoding.EncodeToString(raw)
}

func validAssetCSP(declarations []string) bool {
	for _, declaration := range declarations {
		if declaration == "" || len(declaration) > 512 || strings.ContainsAny(declaration, ";\r\n\x00") {
			return false
		}
		fields := strings.Fields(declaration)
		if len(fields) < 2 {
			return false
		}
		switch fields[0] {
		case "connect-src", "font-src", "img-src", "media-src", "script-src", "style-src", "worker-src":
		default:
			return false
		}
		for _, source := range fields[1:] {
			if len(source) > 240 || strings.ContainsAny(source, "\"<>\\") {
				return false
			}
		}
	}
	return true
}

func matchingVersionedSchemaFile(files map[string]ManifestPackageFile, reference string) bool {
	separator := strings.LastIndex(reference, "@")
	if separator <= 0 || separator == len(reference)-1 {
		return false
	}
	file, ok := files[reference[:separator]]
	return ok && file.Kind == "schema" && file.Version == reference[separator+1:]
}

func matchingPackageFile(files map[string]ManifestPackageFile, path string, kind string, digest string) bool {
	file, exists := files[path]
	return exists && file.Kind == kind && file.Digest == digest
}

func (v *v3Validator) allLocalSchemasDeclared(files map[string]ManifestPackageFile) bool {
	refs := make([]string, 0)
	for _, route := range v.manifest.Routes {
		refs = append(refs, route.RequestSchema, route.ResponseSchema)
	}
	for _, hook := range v.manifest.Hooks {
		refs = append(refs, hook.InputSchema, hook.ResultSchema)
	}
	for _, event := range v.manifest.Events {
		refs = append(refs, event.InputSchema, event.ResultSchema)
	}
	for _, job := range v.manifest.Jobs {
		refs = append(refs, job.PayloadSchema)
	}
	for _, provider := range v.manifest.Providers {
		refs = append(refs, provider.RequestSchema, provider.ResponseSchema)
	}
	for _, component := range v.manifest.Components {
		refs = append(refs, component.PropsSchema, component.ResultSchema)
	}
	for _, template := range v.manifest.Templates {
		refs = append(refs, template.ViewModelSchema)
	}
	for _, content := range v.manifest.Content {
		refs = append(refs, content.Schema)
	}
	for _, service := range v.manifest.Services {
		refs = append(refs, service.RequestSchema, service.ResponseSchema)
	}
	for _, command := range v.manifest.Commands {
		refs = append(refs, command.InputSchema, command.ResultSchema)
	}
	for _, surface := range v.manifest.AdminSurfaces {
		refs = append(refs, surface.Schema)
	}
	for _, query := range v.manifest.Queries {
		refs = append(refs, query.ResultSchema)
	}
	if v.manifest.Database != nil {
		for _, operation := range v.manifest.Database.Operations {
			refs = append(refs, operation.ResultSchema)
			for _, parameter := range operation.Parameters {
				refs = append(refs, parameter.Schema)
			}
		}
	}
	if v.manifest.Identity != nil {
		for _, field := range v.manifest.Identity.UserFields {
			refs = append(refs, field.Schema)
		}
		for _, provider := range v.manifest.Identity.Providers {
			for _, operation := range provider.Operations {
				refs = append(refs, operation.InputSchema, operation.OutputSchema)
			}
		}
	}
	if v.manifest.Lifecycle != nil {
		for _, operation := range []*ManifestLifecycleOperation{v.manifest.Lifecycle.Install, v.manifest.Lifecycle.Enable, v.manifest.Lifecycle.Disable, v.manifest.Lifecycle.Upgrade, v.manifest.Lifecycle.Rollback, v.manifest.Lifecycle.Uninstall} {
			if operation != nil {
				refs = append(refs, operation.ProgressSchema, operation.CheckpointSchema)
			}
		}
	}
	for _, ref := range refs {
		if ref == "" || validContractVersion(ref) {
			continue
		}
		if file, exists := files[ref]; !exists || file.Kind != "schema" {
			return false
		}
	}
	return true
}

func validComponentAction(value string) bool {
	switch value {
	case ComponentActionAdd, ComponentActionBefore, ComponentActionAfter, ComponentActionWrap, ComponentActionReplace, ComponentActionHide, ComponentActionFilterProps, ComponentActionFilterResult:
		return true
	default:
		return false
	}
}

// componentSSRTemplateConsistent 校验组件与其 SSR 模板的生产准入契约。
// 仅使用已冻结字段：不发明 fragment 输入/wrap/filter 语义，也不合成 theme override target version。
func componentSSRTemplateConsistent(component ManifestComponent, template ManifestTemplate) bool {
	// 组件 SSR 片段只绑定同包 action=add 且无 target 的模板；replace/target 留给主题覆盖声明。
	if template.Action != "add" || template.TargetID != "" {
		return false
	}
	// PropsSchema 与 ViewModelSchema 必须是规范化后的精确同一引用，禁止隐式拓宽或缺失。
	if component.PropsSchema != template.ViewModelSchema {
		return false
	}
	// 双方皆空：明确表示无 theme override 绑定；任一方非空时必须逐字一致。
	if component.ThemeOverrideKey != template.ThemeOverrideKey {
		return false
	}
	return true
}

func validComponentTarget(targetID string, targetContractVersion string, manifestType string) bool {
	if targetID == "" {
		return targetContractVersion == ""
	}
	if !manifestIDPattern.MatchString(targetID) || !validContractVersion(targetContractVersion) {
		return false
	}
	if !strings.HasPrefix(targetID, "core.") {
		return true
	}
	if !strings.HasPrefix(targetID, "core.component.") {
		return false
	}
	target, found := componentcatalog.FindCoreComponent(targetID)
	return found && target.ContractVersion == targetContractVersion && (manifestType != TypeTheme || target.OwnedBy(componentcatalog.OwnerPublic))
}

func validAdminSurfacePlacement(targetID string, targetContractVersion string) bool {
	if !manifestIDPattern.MatchString(targetID) || !validContractVersion(targetContractVersion) {
		return false
	}
	target, found := componentcatalog.FindCoreComponent(targetID)
	return found && target.ContractVersion == targetContractVersion && target.Kind == componentcatalog.KindPage && target.OwnedBy(componentcatalog.OwnerAdmin)
}
