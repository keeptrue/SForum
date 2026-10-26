package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"path"
	"strings"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

const (
	ThemeRenderSourceActiveOverride = "active_theme_override"
	ThemeRenderSourcePlugin         = "plugin_template"
	ThemeRenderSourceActiveTheme    = "active_theme"
	ThemeRenderSourceDefaultTheme   = "default_theme"
	ThemeRenderSourceEmergency      = "host_emergency"
)

type themeRenderCandidate struct {
	source   string
	snapshot *ThemeRuntimeSnapshot
	binding  ThemeRuntimeProviderBinding
}

type themeRenderPlan struct {
	pageID          string
	contributionID  string
	viewModelID     string
	viewModelSchema string
	pluginContract  *themecompiler.PluginPageViewModelContract
	candidates      []themeRenderCandidate
}

func (s *ThemeRuntimeSnapshot) renderPlan(
	ctx context.Context,
	request CorePageViewModelRequest,
	contributionID string,
) (ThemeRenderedPage, error) {
	if s.plan == nil || request.PageID != s.plan.pageID || contributionID != s.plan.contributionID {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	if s.plan.pluginContract != nil {
		return ThemeRenderedPage{}, themecompiler.ErrViewModelSchema
	}
	value, err := BuildCorePageViewModel(request)
	if err != nil {
		return ThemeRenderedPage{}, err
	}
	return s.renderBoundPlan(ctx, contributionID, request.SEO, func(candidate themeRenderCandidate) (themecompiler.BoundPageViewModel, error) {
		return themecompiler.CorePageViewModelRegistry().Bind(
			candidate.binding.ViewModelID, candidate.binding.ViewModelSchema,
			candidate.snapshot.artifact.PackageDigest, value,
		)
	})
}

func (s *ThemeRuntimeSnapshot) renderPluginPlan(
	ctx context.Context,
	payload json.RawMessage,
	seo themecompiler.PageSEOView,
	contributionID string,
) (ThemeRenderedPage, error) {
	if s.plan == nil || s.plan.pluginContract == nil || contributionID != s.plan.contributionID {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	sealed, sealErr := s.plan.pluginContract.Bind(s.artifact.PackageDigest, payload, seo)
	return s.renderBoundPlan(ctx, contributionID, seo, func(candidate themeRenderCandidate) (themecompiler.BoundPageViewModel, error) {
		if sealErr != nil {
			return themecompiler.BoundPageViewModel{}, sealErr
		}
		return s.plan.pluginContract.Rebind(sealed, candidate.snapshot.artifact.PackageDigest)
	})
}

func (s *ThemeRuntimeSnapshot) renderBoundPlan(
	ctx context.Context,
	contributionID string,
	seo themecompiler.PageSEOView,
	bind func(themeRenderCandidate) (themecompiler.BoundPageViewModel, error),
) (ThemeRenderedPage, error) {
	if s.plan == nil || contributionID != s.plan.contributionID {
		return ThemeRenderedPage{}, ErrThemeRuntimeMissing
	}
	attempts := make([]ThemeRenderAttempt, 0, len(s.plan.candidates)+1)
	failed := false
	for _, candidate := range s.plan.candidates {
		if candidate.snapshot == nil || candidate.snapshot.compiled == nil {
			continue
		}
		attempt := ThemeRenderAttempt{
			Source: candidate.source, ExtensionID: candidate.snapshot.artifact.ExtensionID,
			PackageDigest: candidate.snapshot.artifact.PackageDigest, Template: candidate.binding.Template,
		}
		bound, bindErr := bind(candidate)
		if bindErr != nil {
			attempt.Outcome = "failed"
			attempt.FailureCode = themeRenderFailureCode(bindErr)
			attempts = append(attempts, attempt)
			failed = true
			continue
		}
		output, renderErr := candidate.snapshot.compiled.Render(ctx, candidate.binding.Template, bound)
		if renderErr != nil {
			if errors.Is(renderErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return ThemeRenderedPage{}, context.Canceled
			}
			attempt.Outcome = "failed"
			attempt.FailureCode = themeRenderFailureCode(renderErr)
			attempts = append(attempts, attempt)
			failed = true
			continue
		}
		attempt.Outcome = "rendered"
		attempts = append(attempts, attempt)
		segments := output.HTMLSegments()
		result := ThemeRenderedPage{
			HTMLSegments: make([]string, len(segments)), Islands: output.Islands(), SEO: output.SEO(),
			Source: candidate.source, Fallback: failed, Attempts: attempts, NodeRevision: s.publicationRevision,
		}
		for index := range segments {
			result.HTMLSegments[index] = segments[index].String()
		}
		return result, nil
	}
	return emergencyThemeOutput(CorePageViewModelRequest{
		PageID: s.plan.pageID, SEO: seo,
	}, attempts, s.publicationRevision), nil
}

func emergencyThemeOutput(request CorePageViewModelRequest, attempts []ThemeRenderAttempt, revision uint64) ThemeRenderedPage {
	title := strings.TrimSpace(request.SEO.Title)
	if title == "" {
		title = request.PageID
	}
	attempts = append(attempts, ThemeRenderAttempt{Source: ThemeRenderSourceEmergency, Outcome: "rendered"})
	return ThemeRenderedPage{
		HTMLSegments: []string{`<main data-sforum-emergency="true"><h1>` + html.EscapeString(title) + `</h1></main>`},
		SEO:          request.SEO, Source: ThemeRenderSourceEmergency, Fallback: true, Attempts: attempts, NodeRevision: revision,
	}
}

func themeRenderFailureCode(err error) string {
	switch {
	case errors.Is(err, themecompiler.ErrRenderTimeout):
		return "render_timeout"
	case errors.Is(err, themecompiler.ErrOutputLimit):
		return "output_limit"
	case errors.Is(err, themecompiler.ErrMissingValue):
		return "missing_value"
	case errors.Is(err, themecompiler.ErrViewModelSchema):
		return "view_model_contract"
	case errors.Is(err, themecompiler.ErrRequiredIsland), errors.Is(err, themecompiler.ErrInvalidIsland):
		return "island_contract"
	case errors.Is(err, themecompiler.ErrExecution):
		return "execution"
	default:
		return "render_failed"
	}
}

func pageForTemplateContract(contract string) (PageDefinition, bool) {
	contract = strings.TrimSpace(contract)
	for _, page := range Catalog() {
		if page.ContractVersion == contract {
			return page, true
		}
	}
	return PageDefinition{}, false
}

func validPluginOverridePath(name, targetID string) bool {
	name = path.Clean(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "templates/plugins/") || !strings.HasSuffix(name, ".html") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 4 || parts[2] == "" || strings.Contains(parts[2], "..") {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(targetID), parts[2]+".")
}

func runtimeRenderKey(artifact RuntimeArtifact, pageID, contributionID string) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s", artifact.ExtensionID, artifact.ExtensionVersion,
		artifact.PackageDigest, artifact.RuntimeInstanceID, pageID, contributionID)
}

func (r *ThemeRuntimeRegistry) rebuildRenderersLocked() {
	r.ensureSnapshots()
	next := make(map[string]*ThemeRuntimeSnapshot)
	active := r.snapshots[r.active]
	defaultTheme := r.snapshots[r.defaultArtifact]
	for artifact, owner := range r.snapshots {
		if owner == nil {
			continue
		}
		for pageID, provider := range owner.providers {
			plan := &themeRenderPlan{
				pageID: pageID, contributionID: provider.ContributionID,
				viewModelID: provider.ViewModelID, viewModelSchema: provider.ViewModelSchema,
				pluginContract: owner.pluginContracts[provider.TemplateID],
			}
			if owner.kind == RuntimeTemplatePlugin {
				if active != nil && provider.TemplateID != "" {
					if override, ok := active.overrides[provider.TemplateID]; ok {
						if compatibleThemeOverride(provider, override) {
							override.PageID = pageID
							plan.addCandidate(ThemeRenderSourceActiveOverride, active, override)
						}
					}
				}
				plan.addCandidate(ThemeRenderSourcePlugin, owner, provider)
				if activeProvider, ok := providerForViewModel(active, pageID, provider); ok {
					plan.addCandidate(ThemeRenderSourceActiveTheme, active, activeProvider)
				}
				if defaultProvider, ok := providerForViewModel(defaultTheme, pageID, provider); ok {
					plan.addCandidate(ThemeRenderSourceDefaultTheme, defaultTheme, defaultProvider)
				}
			} else {
				source := ThemeRenderSourceActiveTheme
				if artifact == r.defaultArtifact && artifact != r.active {
					source = ThemeRenderSourceDefaultTheme
				}
				plan.addCandidate(source, owner, provider)
				if defaultProvider, ok := providerForViewModel(defaultTheme, pageID, provider); ok {
					plan.addCandidate(ThemeRenderSourceDefaultTheme, defaultTheme, defaultProvider)
				}
			}
			next[runtimeRenderKey(artifact, pageID, provider.ContributionID)] = runtimeSnapshotWithPlan(owner, plan, active, r.revision+1)
		}
	}
	r.renderers = next
}

func (p *themeRenderPlan) addCandidate(source string, snapshot *ThemeRuntimeSnapshot, binding ThemeRuntimeProviderBinding) {
	if p == nil || snapshot == nil || binding.PageID != p.pageID ||
		binding.ViewModelID != p.viewModelID || binding.ViewModelSchema != p.viewModelSchema {
		return
	}
	for _, current := range p.candidates {
		if current.snapshot.artifact == snapshot.artifact && current.binding.Template == binding.Template {
			return
		}
	}
	p.candidates = append(p.candidates, themeRenderCandidate{source: source, snapshot: snapshot, binding: binding})
}

func providerForViewModel(
	snapshot *ThemeRuntimeSnapshot,
	pageID string,
	want ThemeRuntimeProviderBinding,
) (ThemeRuntimeProviderBinding, bool) {
	if snapshot == nil {
		return ThemeRuntimeProviderBinding{}, false
	}
	binding, ok := snapshot.providers[pageID]
	return binding, ok && binding.ViewModelID == want.ViewModelID && binding.ViewModelSchema == want.ViewModelSchema
}

func compatibleThemeOverride(plugin, override ThemeRuntimeProviderBinding) bool {
	if plugin.TemplateID == "" || override.ViewModelID != plugin.ViewModelID ||
		override.ViewModelSchema != plugin.ViewModelSchema {
		return false
	}
	if plugin.SchemaDigest == "" {
		return true
	}
	return plugin.ThemeOverrideKey != "" && plugin.ThemeOverrideKey == override.ThemeOverrideKey
}

func runtimeSnapshotWithPlan(owner *ThemeRuntimeSnapshot, plan *themeRenderPlan, active *ThemeRuntimeSnapshot, revision uint64) *ThemeRuntimeSnapshot {
	islandTags := owner.islandTags
	if active != nil {
		islandTags = active.islandTags
	}
	return &ThemeRuntimeSnapshot{
		artifact: owner.artifact, compiled: owner.compiled, providers: owner.providers,
		assets: owner.assets, locales: owner.locales, contracts: owner.contracts,
		islandTags: islandTags, kind: owner.kind, overrides: owner.overrides,
		pluginContracts: owner.pluginContracts, plan: plan,
		publicationRevision: revision,
	}
}
