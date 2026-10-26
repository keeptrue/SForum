package pages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

var (
	ErrThemeRuntimeInvalid  = errors.New("pages: invalid theme runtime snapshot")
	ErrThemeRuntimeConflict = errors.New("pages: theme runtime exact artifact conflict")
	ErrThemeRuntimeMissing  = errors.New("pages: theme runtime snapshot is missing")
)

type ThemeRuntimeBuildInput struct {
	Artifact      RuntimeArtifact
	PackageRoot   string
	Contributions []PageContribution
	Templates     []RuntimeTemplateDeclaration
	DataSchemas   []RuntimeDataSchemaDeclaration
	PackageKind   RuntimeTemplatePackageKind
	// RequireDeclaredTemplates is enabled for Manifest V3 packages. Legacy
	// theme.json packages keep their synthetic stable template identity until
	// the P13 compatibility path is removed.
	RequireDeclaredTemplates bool
	SiteName                 string
	Locales                  []string
}

type RuntimeTemplatePackageKind string

const (
	RuntimeTemplateTheme  RuntimeTemplatePackageKind = "theme"
	RuntimeTemplatePlugin RuntimeTemplatePackageKind = "plugin"
)

type RuntimeTemplateDeclaration struct {
	ID               string
	ContractVersion  string
	Action           string
	TargetID         string
	Path             string
	Digest           string
	ViewModelSchema  string
	ThemeOverrideKey string
}

type RuntimeDataSchemaDeclaration struct {
	ID      string
	Version string
	Path    string
	Digest  string
}

type ThemeRuntimeProviderBinding struct {
	PageID           string `json:"pageId"`
	ContributionID   string `json:"contributionId"`
	TemplateID       string `json:"templateId"`
	Template         string `json:"template"`
	ContractVersion  string `json:"contractVersion"`
	ViewModelID      string `json:"viewModelId"`
	ViewModelSchema  string `json:"viewModelSchema"`
	SchemaDigest     string `json:"schemaDigest,omitempty"`
	ThemeOverrideKey string `json:"themeOverrideKey,omitempty"`
}

type ThemeRenderAttempt struct {
	Source        string `json:"source"`
	ExtensionID   string `json:"extensionId,omitempty"`
	PackageDigest string `json:"packageDigest,omitempty"`
	Template      string `json:"template,omitempty"`
	Outcome       string `json:"outcome"`
	FailureCode   string `json:"failureCode,omitempty"`
}

type ThemeRenderedPage struct {
	HTMLSegments []string                         `json:"htmlSegments"`
	Islands      []themecompiler.IslandDescriptor `json:"islands,omitempty"`
	SEO          themecompiler.PageSEOView        `json:"seo"`
	Source       string                           `json:"source"`
	Fallback     bool                             `json:"fallback"`
	Attempts     []ThemeRenderAttempt             `json:"attempts,omitempty"`
	NodeRevision uint64                           `json:"nodeRevision"`
}

type ThemeRuntimeSnapshot struct {
	artifact            RuntimeArtifact
	compiled            *themecompiler.Snapshot
	providers           map[string]ThemeRuntimeProviderBinding
	assets              ActiveSkinPublic
	locales             []string
	contracts           map[string]string
	islandTags          map[string]string
	kind                RuntimeTemplatePackageKind
	overrides           map[string]ThemeRuntimeProviderBinding
	pluginContracts     map[string]*themecompiler.PluginPageViewModelContract
	plan                *themeRenderPlan
	navigationLocations map[string]string
	publicationRevision uint64
}

func BuildThemeRuntimeSnapshot(input ThemeRuntimeBuildInput) (*ThemeRuntimeSnapshot, error) {
	if !validThemeRuntimeArtifact(input.Artifact) || strings.TrimSpace(input.PackageRoot) == "" {
		return nil, ErrThemeRuntimeInvalid
	}
	root, err := filepath.Abs(strings.TrimSpace(input.PackageRoot))
	if err != nil {
		return nil, fmt.Errorf("%w: package root: %v", ErrThemeRuntimeInvalid, err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: package root: %v", ErrThemeRuntimeInvalid, err)
	}
	kind := input.PackageKind
	if kind == "" {
		kind = RuntimeTemplateTheme
	}
	if kind != RuntimeTemplateTheme && kind != RuntimeTemplatePlugin {
		return nil, ErrThemeRuntimeInvalid
	}
	themePackage, err := LoadThemePackage(realRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: load theme package: %v", ErrThemeRuntimeInvalid, err)
	}
	if kind == RuntimeTemplatePlugin && len(themePackage.NavigationLocations) > 0 {
		return nil, fmt.Errorf("%w: plugins cannot declare theme navigation locations", ErrThemeRuntimeConflict)
	}
	navigationLocations := cloneStringMap(themePackage.NavigationLocations)
	providers := make(map[string]ThemeRuntimeProviderBinding)
	providerContributions := make(map[string]PageContribution)
	pageBindings := make(map[string]themecompiler.PageTemplateBinding)
	selectedTemplates := make(map[string]struct{})
	contracts := make(map[string]string)
	for _, contribution := range input.Contributions {
		if strings.TrimSpace(contribution.Template) == "" {
			continue
		}
		var page PageDefinition
		switch contribution.Action {
		case ActionReplace:
			var ok bool
			page, ok = Find(contribution.Target)
			if !ok || page.ContractVersion != contribution.Contract {
				return nil, ErrThemeRuntimeConflict
			}
			if IsSystemErrorPage(page.ID) && kind != RuntimeTemplateTheme {
				return nil, fmt.Errorf("%w: system error pages are theme-only surfaces", ErrThemeRuntimeConflict)
			}
		case ActionAdd:
			if kind != RuntimeTemplatePlugin || strings.TrimSpace(contribution.ID) == "" || strings.TrimSpace(contribution.Contract) == "" {
				continue
			}
			page = PageDefinition{ID: contribution.ID, ContractVersion: contribution.Contract}
		default:
			continue
		}
		if contribution.ExtensionID != input.Artifact.ExtensionID ||
			contribution.Version != input.Artifact.ExtensionVersion || contribution.PackageDigest != input.Artifact.PackageDigest {
			return nil, ErrThemeRuntimeConflict
		}
		templateName := filepath.ToSlash(strings.TrimSpace(contribution.Template))
		if previous, exists := providers[page.ID]; exists && previous.ContributionID != contribution.ID {
			return nil, ErrThemeRuntimeConflict
		}
		if _, exists := pageBindings[templateName]; exists {
			return nil, ErrThemeRuntimeConflict
		}
		providers[page.ID] = ThemeRuntimeProviderBinding{
			PageID: page.ID, ContributionID: contribution.ID, Template: templateName,
			ContractVersion: page.ContractVersion, ViewModelID: page.ID, ViewModelSchema: page.ContractVersion,
		}
		providerContributions[page.ID] = contribution
		contracts[page.ID] = page.ContractVersion
	}
	declarations := make(map[string]RuntimeTemplateDeclaration, len(input.Templates))
	for _, declaration := range input.Templates {
		templateName := filepath.ToSlash(strings.TrimSpace(declaration.Path))
		if templateName == "" || declarations[templateName].ID != "" {
			return nil, ErrThemeRuntimeConflict
		}
		declaration.Path = templateName
		declarations[templateName] = declaration
	}
	pluginContracts := make(map[string]*themecompiler.PluginPageViewModelContract)
	for pageID, provider := range providers {
		contribution := providerContributions[pageID]
		declaration, declared := declarations[provider.Template]
		if input.RequireDeclaredTemplates && !declared {
			return nil, fmt.Errorf("%w: %s has no exact template declaration", ErrThemeRuntimeConflict, provider.Template)
		}
		if declared {
			if declaration.Action != "add" || declaration.ContractVersion == "" || declaration.ViewModelSchema == "" || declaration.Digest == "" {
				return nil, fmt.Errorf("%w: declaration contract for %s", ErrThemeRuntimeConflict, provider.Template)
			}
			provider.TemplateID = declaration.ID
			provider.ThemeOverrideKey = declaration.ThemeOverrideKey
			if pluginBusinessDataRequested(contribution) {
				if kind != RuntimeTemplatePlugin {
					return nil, fmt.Errorf("%w: themes cannot own plugin page data", ErrThemeRuntimeConflict)
				}
				contract, contractErr := compilePluginPageDataContract(realRoot, declaration, contribution, input.DataSchemas)
				if contractErr != nil {
					return nil, contractErr
				}
				descriptor := contract.Schema()
				provider.ViewModelID = descriptor.ViewModelID
				provider.ViewModelSchema = descriptor.SchemaVersion
				provider.SchemaDigest = descriptor.SchemaDigest
				pluginContracts[provider.TemplateID] = contract
			} else if contribution.Action == ActionAdd {
				// Static legacy add pages remain on the explicit compatibility path.
				delete(providers, pageID)
				delete(contracts, pageID)
				continue
			} else if declaration.ViewModelSchema != provider.ContractVersion {
				return nil, fmt.Errorf("%w: declaration contract for %s", ErrThemeRuntimeConflict, provider.Template)
			}
		} else {
			if contribution.Action != ActionReplace || pluginBusinessDataRequested(contribution) {
				return nil, fmt.Errorf("%w: plugin business templates require exact declarations", ErrThemeRuntimeConflict)
			}
			provider.TemplateID = input.Artifact.ExtensionID + ".template." + provider.ContributionID
		}
		if _, exists := pageBindings[provider.Template]; exists {
			return nil, ErrThemeRuntimeConflict
		}
		pageBindings[provider.Template] = themecompiler.PageTemplateBinding{
			PageID: provider.ViewModelID, SchemaVersion: provider.ViewModelSchema,
		}
		selectedTemplates[provider.Template] = struct{}{}
		providers[pageID] = provider
	}
	overrides := make(map[string]ThemeRuntimeProviderBinding)
	if kind == RuntimeTemplateTheme {
		for _, declaration := range input.Templates {
			if declaration.Action != "replace" || strings.TrimSpace(declaration.TargetID) == "" {
				continue
			}
			page, coreContract := pageForTemplateContract(declaration.ViewModelSchema)
			if declaration.ContractVersion == "" || declaration.Digest == "" ||
				!validPluginOverridePath(declaration.Path, declaration.TargetID) {
				return nil, fmt.Errorf("%w: invalid theme override %s", ErrThemeRuntimeConflict, declaration.ID)
			}
			viewModelID := declaration.TargetID
			pageID := ""
			if coreContract {
				viewModelID = page.ID
				pageID = page.ID
			} else if strings.TrimSpace(declaration.ThemeOverrideKey) == "" {
				return nil, fmt.Errorf("%w: plugin override %s has no exact override key", ErrThemeRuntimeConflict, declaration.ID)
			}
			if _, exists := overrides[declaration.TargetID]; exists {
				return nil, ErrThemeRuntimeConflict
			}
			binding := ThemeRuntimeProviderBinding{
				PageID: pageID, TemplateID: declaration.ID, Template: declaration.Path,
				ContractVersion: declaration.ViewModelSchema, ViewModelID: viewModelID,
				ViewModelSchema: declaration.ViewModelSchema, ThemeOverrideKey: declaration.ThemeOverrideKey,
			}
			overrides[declaration.TargetID] = binding
			pageBindings[declaration.Path] = themecompiler.PageTemplateBinding{
				PageID: viewModelID, SchemaVersion: declaration.ViewModelSchema,
			}
			selectedTemplates[declaration.Path] = struct{}{}
		}
	}
	if len(providers) == 0 {
		return nil, ErrThemeRuntimeMissing
	}
	for pageID, provider := range providers {
		if IsSystemErrorPage(pageID) {
			if err := rejectSystemErrorExecutableTemplate(realRoot, provider.Template); err != nil {
				return nil, err
			}
		}
	}
	assets, err := SkinFromPackage(
		input.Artifact.ExtensionID, input.Artifact.ExtensionVersion, input.Artifact.PackageDigest, realRoot,
	)
	if err != nil {
		return nil, err
	}
	routes := make(map[string]string)
	for _, page := range Catalog() {
		routes[page.ID] = page.PathPattern
	}
	islands := productionThemeIslandBindings()
	bindingRevision, err := themeBindingRevision(input.Artifact, kind, providers, overrides, assets, input.Locales, contracts, islands, navigationLocations)
	if err != nil {
		return nil, err
	}
	compiled, err := themecompiler.NewCompiler(themecompiler.Limits{}).CompileFS(
		selectedThemeFS{FS: os.DirFS(realRoot), selected: selectedTemplates},
		input.Artifact.PackageDigest,
		themecompiler.Bindings{
			BindingRevision: bindingRevision, SiteName: strings.TrimSpace(input.SiteName),
			Assets: themeAssetBindings(assets), Routes: routes, PageViewModels: pageBindings, Islands: islands,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: compile exact theme: %w", ErrThemeRuntimeInvalid, err)
	}
	infos := make(map[string]themecompiler.TemplateInfo)
	for _, info := range compiled.Templates() {
		infos[info.Name] = info
	}
	for name := range selectedTemplates {
		declaration, declared := declarations[name]
		if !declared {
			continue
		}
		if info, ok := infos[name]; !ok || info.Digest != declaration.Digest {
			return nil, fmt.Errorf("%w: exact digest for %s", ErrThemeRuntimeConflict, name)
		}
	}
	islandTags := make(map[string]string, len(islands))
	for tag, binding := range islands {
		islandTags[binding.ComponentID] = tag
	}
	return &ThemeRuntimeSnapshot{
		artifact: input.Artifact, compiled: compiled, providers: providers, assets: assets,
		locales: normalizedLocales(input.Locales), contracts: contracts, islandTags: islandTags,
		kind: kind, overrides: overrides, pluginContracts: pluginContracts,
		navigationLocations: navigationLocations,
	}, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func pluginBusinessDataRequested(contribution PageContribution) bool {
	return strings.TrimSpace(contribution.DataSource) != "" || strings.TrimSpace(contribution.DataRoute) != "" ||
		strings.TrimSpace(contribution.DataSchema) != ""
}

func rejectSystemErrorExecutableTemplate(packageRoot, templatePath string) error {
	full, err := ResolveThemeAsset(packageRoot, templatePath)
	if err != nil {
		return fmt.Errorf("%w: resolve system error template %s: %v", ErrThemeRuntimeInvalid, templatePath, err)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("%w: read system error template %s: %v", ErrThemeRuntimeInvalid, templatePath, err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "<sf-extension-widget") {
		return fmt.Errorf("%w: system error template %s cannot declare public L2 widgets", ErrThemeRuntimeConflict, templatePath)
	}
	return nil
}

func compilePluginPageDataContract(
	realRoot string,
	template RuntimeTemplateDeclaration,
	contribution PageContribution,
	schemas []RuntimeDataSchemaDeclaration,
) (*themecompiler.PluginPageViewModelContract, error) {
	if strings.TrimSpace(strings.ToLower(contribution.DataSource)) != "plugin" ||
		strings.TrimSpace(contribution.DataRoute) == "" || strings.TrimSpace(contribution.DataSchema) == "" {
		return nil, fmt.Errorf("%w: plugin data source, route, and schema are required", ErrThemeRuntimeConflict)
	}
	if err := ValidateDataRoute(contribution.DataRoute); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeRuntimeConflict, err)
	}
	reference := strings.TrimSpace(template.ViewModelSchema)
	separator := strings.LastIndex(reference, "@")
	if separator <= 0 || separator == len(reference)-1 {
		return nil, fmt.Errorf("%w: invalid plugin data schema reference", ErrThemeRuntimeConflict)
	}
	schemaID, schemaVersion := reference[:separator], reference[separator+1:]
	var declared *RuntimeDataSchemaDeclaration
	for index := range schemas {
		candidate := &schemas[index]
		if candidate.ID == schemaID && candidate.Version == schemaVersion {
			declared = candidate
			break
		}
	}
	if declared == nil || filepath.ToSlash(strings.TrimSpace(declared.Path)) != filepath.ToSlash(strings.TrimSpace(contribution.DataSchema)) {
		return nil, fmt.Errorf("%w: exact plugin data schema declaration is missing", ErrThemeRuntimeConflict)
	}
	relative := filepath.Clean(filepath.FromSlash(strings.TrimSpace(declared.Path)))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: plugin data schema escapes package", ErrThemeRuntimeConflict)
	}
	realPath, err := filepath.EvalSymlinks(filepath.Join(realRoot, relative))
	if err != nil || realPath == realRoot || !strings.HasPrefix(realPath, realRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: plugin data schema escapes package", ErrThemeRuntimeConflict)
	}
	body, err := os.ReadFile(realPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read plugin data schema: %v", ErrThemeRuntimeConflict, err)
	}
	contract, err := themecompiler.CompilePluginPageViewModelContract(
		template.ID, reference, declared.Digest, body,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrThemeRuntimeConflict, err)
	}
	return contract, nil
}

func (s *ThemeRuntimeSnapshot) Artifact() RuntimeArtifact {
	if s == nil {
		return RuntimeArtifact{}
	}
	return s.artifact
}

func (s *ThemeRuntimeSnapshot) RuntimeKey() string {
	if s == nil || s.compiled == nil {
		return ""
	}
	return s.compiled.RuntimeKey()
}

func (s *ThemeRuntimeSnapshot) Covers(pageID, contributionID string) bool {
	if s == nil {
		return false
	}
	binding, ok := s.providers[pageID]
	return ok && binding.ContributionID == contributionID
}

func (s *ThemeRuntimeSnapshot) PluginDataContract(
	contributionID string,
) (themecompiler.PluginPageViewModelSchema, bool) {
	if s == nil || s.plan == nil || s.plan.contributionID != contributionID || s.plan.pluginContract == nil {
		return themecompiler.PluginPageViewModelSchema{}, false
	}
	return s.plan.pluginContract.Schema(), true
}

func (s *ThemeRuntimeSnapshot) Render(
	ctx context.Context,
	request CorePageViewModelRequest,
	contributionID string,
) (ThemeRenderedPage, error) {
	if s == nil || s.compiled == nil || ctx == nil {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	if s.plan != nil {
		return s.renderPlan(ctx, request, contributionID)
	}
	binding, ok := s.providers[request.PageID]
	if !ok || binding.ContributionID != contributionID {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	value, err := BuildCorePageViewModel(request)
	if err != nil {
		return ThemeRenderedPage{}, err
	}
	bound, err := themecompiler.CorePageViewModelRegistry().Bind(
		binding.ViewModelID, binding.ViewModelSchema, s.artifact.PackageDigest, value,
	)
	if err != nil {
		return ThemeRenderedPage{}, err
	}
	output, err := s.compiled.Render(ctx, binding.Template, bound)
	if err != nil {
		return ThemeRenderedPage{}, err
	}
	segments := output.HTMLSegments()
	result := ThemeRenderedPage{
		HTMLSegments: make([]string, len(segments)), Islands: output.Islands(), SEO: output.SEO(),
		Source: ThemeRenderSourceActiveTheme, NodeRevision: s.publicationRevision,
	}
	for index := range segments {
		result.HTMLSegments[index] = segments[index].String()
	}
	return result, nil
}

// ApplyComposition prefaces optional Host composition HTML after a successful
// theme render. Fail-open for ordinary composition errors; SEO fence fail-closed.
func (r *ThemeRuntimeRegistry) ApplyComposition(
	ctx context.Context,
	page ThemeRenderedPage,
	pageID string,
) (ThemeRenderedPage, error) {
	if r == nil {
		return page, nil
	}
	return ApplyPageComposition(ctx, page, r.pageComposition(), pageID, map[string]any{"pageId": pageID})
}

func (s *ThemeRuntimeSnapshot) RenderPluginData(
	ctx context.Context,
	payload json.RawMessage,
	seo themecompiler.PageSEOView,
	contributionID string,
) (ThemeRenderedPage, error) {
	if s == nil || s.compiled == nil || ctx == nil {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	return s.renderPluginPlan(ctx, payload, seo, contributionID)
}

func (s *ThemeRuntimeSnapshot) LegacyHTML(output ThemeRenderedPage) string {
	if s == nil {
		return ""
	}
	value := strings.Join(output.HTMLSegments, "")
	for _, island := range output.Islands {
		tag := s.islandTags[island.ComponentID]
		if tag == "" {
			return ""
		}
		placeholder := `<template data-sforum-island="` + island.ID + `"></template>`
		value = strings.Replace(value, placeholder, "<"+tag+"></"+tag+">", 1)
	}
	return value
}

type ThemeRuntimeRegistry struct {
	mu              sync.RWMutex
	revision        uint64
	active          RuntimeArtifact
	defaultArtifact RuntimeArtifact
	snapshots       map[RuntimeArtifact]*ThemeRuntimeSnapshot
	renderers       map[string]*ThemeRuntimeSnapshot
	activationCheck func(RuntimeArtifact) error
	// composition 可选：主题 L1 渲染成功后 preface 插件组件 HTML，失败默认 fail-open。
	composition PageCompositionRenderer
}

func (r *ThemeRuntimeRegistry) WithActivationCheck(check func(RuntimeArtifact) error) *ThemeRuntimeRegistry {
	if r != nil {
		r.mu.Lock()
		r.activationCheck = check
		r.mu.Unlock()
	}
	return r
}

// WithPageComposition attaches optional Host component composition. Nil clears it.
func (r *ThemeRuntimeRegistry) WithPageComposition(composition PageCompositionRenderer) *ThemeRuntimeRegistry {
	if r != nil {
		r.mu.Lock()
		r.composition = composition
		r.mu.Unlock()
	}
	return r
}

func (r *ThemeRuntimeRegistry) pageComposition() PageCompositionRenderer {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.composition
}

func NewThemeRuntimeRegistry() *ThemeRuntimeRegistry {
	return &ThemeRuntimeRegistry{
		snapshots: make(map[RuntimeArtifact]*ThemeRuntimeSnapshot),
		renderers: make(map[string]*ThemeRuntimeSnapshot),
	}
}

func (r *ThemeRuntimeRegistry) Publish(snapshot *ThemeRuntimeSnapshot) uint64 {
	if r == nil || snapshot == nil || snapshot.kind != RuntimeTemplateTheme {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureSnapshots()
	r.snapshots[snapshot.artifact] = snapshot
	r.active = snapshot.artifact
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision
}

// Stage makes a target artifact resolvable before the Page Registry switches.
// It does not change which snapshot is reported as active.
func (r *ThemeRuntimeRegistry) Stage(snapshot *ThemeRuntimeSnapshot) (uint64, bool, error) {
	if r == nil || snapshot == nil {
		return 0, false, ErrThemeRuntimeMissing
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureSnapshots()
	if current, exists := r.snapshots[snapshot.artifact]; exists {
		if current.RuntimeKey() != snapshot.RuntimeKey() {
			return r.revision, false, ErrThemeRuntimeConflict
		}
		return r.revision, false, nil
	}
	r.snapshots[snapshot.artifact] = snapshot
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision, true, nil
}

func (r *ThemeRuntimeRegistry) ActivateExact(artifact RuntimeArtifact) (uint64, error) {
	if r == nil {
		return 0, ErrThemeRuntimeMissing
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activationCheck != nil {
		if err := r.activationCheck(artifact); err != nil {
			return r.revision, err
		}
	}
	snapshot, exists := r.snapshots[artifact]
	if !exists || snapshot.kind != RuntimeTemplateTheme {
		return r.revision, ErrThemeRuntimeMissing
	}
	if r.active == artifact {
		return r.revision, nil
	}
	r.active = artifact
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision, nil
}

// SetDefaultExact retains the protected default theme as the third-level
// fallback without changing the active public theme.
func (r *ThemeRuntimeRegistry) SetDefaultExact(artifact RuntimeArtifact) (uint64, error) {
	if r == nil {
		return 0, ErrThemeRuntimeMissing
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.snapshots[artifact]
	if snapshot == nil || snapshot.kind != RuntimeTemplateTheme {
		return r.revision, ErrThemeRuntimeMissing
	}
	if r.defaultArtifact == artifact {
		return r.revision, nil
	}
	r.defaultArtifact = artifact
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision, nil
}

func (r *ThemeRuntimeRegistry) RemoveExact(artifact RuntimeArtifact) (uint64, error) {
	if r == nil {
		return 0, ErrThemeRuntimeMissing
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, exists := r.snapshots[artifact]
	if !exists {
		if active := r.snapshots[r.active]; active != nil && active.artifact.ExtensionID == artifact.ExtensionID {
			return r.revision, ErrThemeRuntimeConflict
		}
		return r.revision, nil
	}
	delete(r.snapshots, snapshot.artifact)
	if r.active == artifact {
		r.active = RuntimeArtifact{}
	}
	if r.defaultArtifact == artifact {
		r.defaultArtifact = RuntimeArtifact{}
	}
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision, nil
}

func (r *ThemeRuntimeRegistry) ClearExtension(extensionID string) uint64 {
	if r == nil || strings.TrimSpace(extensionID) == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for artifact := range r.snapshots {
		if artifact.ExtensionID == extensionID {
			delete(r.snapshots, artifact)
			changed = true
		}
	}
	if r.active.ExtensionID == extensionID {
		r.active = RuntimeArtifact{}
		changed = true
	}
	if r.defaultArtifact.ExtensionID == extensionID {
		r.defaultArtifact = RuntimeArtifact{}
		changed = true
	}
	if !changed {
		return r.revision
	}
	r.rebuildRenderersLocked()
	r.revision++
	return r.revision
}

func (r *ThemeRuntimeRegistry) Resolve(artifact RuntimeArtifact, pageID, contributionID string) (*ThemeRuntimeSnapshot, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.renderers[runtimeRenderKey(artifact, pageID, contributionID)]
	if snapshot == nil {
		return nil, false
	}
	return snapshot, true
}

// Claims reports whether a compiled snapshot owns the provider identity even
// when the caller supplied a stale artifact. Controllers use it to fail closed
// instead of falling back to request-time package I/O.
func (r *ThemeRuntimeRegistry) Claims(extensionID, pageID, contributionID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for artifact := range r.snapshots {
		if artifact.ExtensionID == extensionID && r.renderers[runtimeRenderKey(artifact, pageID, contributionID)] != nil {
			return true
		}
	}
	return false
}

func (r *ThemeRuntimeRegistry) Active() (*ThemeRuntimeSnapshot, uint64, bool) {
	if r == nil {
		return nil, 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.snapshots[r.active]
	return snapshot, r.revision, snapshot != nil
}

func (r *ThemeRuntimeRegistry) ActiveSkin() (ActiveSkinPublic, bool) {
	if r == nil {
		return ActiveSkinPublic{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.snapshots[r.active]
	if snapshot == nil {
		return ActiveSkinPublic{}, false
	}
	assets := snapshot.assets
	assets.CSS = append([]string(nil), assets.CSS...)
	assets.NodeRevision = r.revision
	return assets, true
}

// SupportsNavigationLocation projects capabilities from the exact active
// immutable theme snapshot. With no active snapshot, Core fallback owns all
// locations and must remain usable.
func (r *ThemeRuntimeRegistry) SupportsNavigationLocation(location string) bool {
	if r == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.snapshots[r.active]
	if snapshot == nil {
		return true
	}
	return snapshot.navigationLocations[strings.TrimSpace(location)] != ""
}

func (r *ThemeRuntimeRegistry) ensureSnapshots() {
	if r.snapshots == nil {
		r.snapshots = make(map[RuntimeArtifact]*ThemeRuntimeSnapshot)
	}
	if r.renderers == nil {
		r.renderers = make(map[string]*ThemeRuntimeSnapshot)
	}
}

func validThemeRuntimeArtifact(artifact RuntimeArtifact) bool {
	return strings.TrimSpace(artifact.ExtensionID) != "" && strings.TrimSpace(artifact.ExtensionVersion) != "" &&
		len(artifact.PackageDigest) == 64 && artifact.PackageDigest == strings.ToLower(artifact.PackageDigest)
}

func themeBindingRevision(
	artifact RuntimeArtifact,
	kind RuntimeTemplatePackageKind,
	providers map[string]ThemeRuntimeProviderBinding,
	overrides map[string]ThemeRuntimeProviderBinding,
	assets ActiveSkinPublic,
	locales []string,
	contracts map[string]string,
	islands map[string]themecompiler.IslandBinding,
	navigationLocations map[string]string,
) (string, error) {
	document := struct {
		Schema              string                                 `json:"schema"`
		Artifact            RuntimeArtifact                        `json:"artifact"`
		Kind                RuntimeTemplatePackageKind             `json:"kind"`
		Providers           map[string]ThemeRuntimeProviderBinding `json:"providers"`
		Overrides           map[string]ThemeRuntimeProviderBinding `json:"overrides"`
		Assets              ActiveSkinPublic                       `json:"assets"`
		Locales             []string                               `json:"locales"`
		Contracts           map[string]string                      `json:"contracts"`
		Islands             map[string]themecompiler.IslandBinding `json:"islands"`
		NavigationLocations map[string]string                      `json:"navigationLocations,omitempty"`
	}{"sforum.theme-runtime-binding@2", artifact, kind, providers, overrides, assets, normalizedLocales(locales), contracts, islands, navigationLocations}
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func normalizedLocales(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, value := range input {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func themeAssetBindings(skin ActiveSkinPublic) map[string]string {
	result := make(map[string]string, len(skin.CSS)+1)
	for index, value := range skin.CSS {
		result[fmt.Sprintf("theme.css.%d", index+1)] = value
	}
	if skin.Tokens != "" {
		result["theme.tokens"] = skin.Tokens
	}
	return result
}

type selectedThemeFS struct {
	fs.FS
	selected map[string]struct{}
}

func (s selectedThemeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(s.FS, name)
	if err != nil {
		return nil, err
	}
	if name != "templates" && !strings.HasPrefix(name, "templates/") {
		return entries, nil
	}
	result := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		child := strings.TrimPrefix(filepath.ToSlash(filepath.Join(name, entry.Name())), "./")
		if _, selected := s.selected[child]; selected || entry.IsDir() && selectedThemePrefix(s.selected, child+"/") {
			result = append(result, entry)
		}
	}
	return result, nil
}

func selectedThemePrefix(selected map[string]struct{}, prefix string) bool {
	for name := range selected {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
