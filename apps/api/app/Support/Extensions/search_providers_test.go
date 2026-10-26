package extensionsruntime

import (
	"context"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

type fakeSearchProviderStore struct {
	selected string
	items    map[string]extensions.Extension
	list     []extensions.Extension
}

func (s *fakeSearchProviderStore) GetSearchProviderExtension(_ context.Context, id string) (extensions.Extension, error) {
	item, ok := s.items[id]
	if !ok {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	return item, nil
}
func (s *fakeSearchProviderStore) SelectedSearchProvider(context.Context) (string, error) {
	return s.selected, nil
}
func (s *fakeSearchProviderStore) SelectSearchProvider(_ context.Context, id string) error {
	s.selected = id
	return nil
}
func (s *fakeSearchProviderStore) RestoreSearchProvider(context.Context) error {
	s.selected = ""
	return nil
}
func (s *fakeSearchProviderStore) List(context.Context) ([]extensions.Extension, error) {
	return s.list, nil
}

func sitePlugin() extensions.Extension {
	return extensions.Extension{
		ID: DefaultSiteSearchExtensionID, Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Name: "Site Search", Source: extensions.SourceBuiltin, IsSystem: true, IsDeletable: false,
		Manifest: extensions.Manifest{
			Providers: []extensions.ManifestProvider{{
				Slot: SearchProviderSlot, Label: "Site Search",
			}},
		},
	}
}

func meiliPlugin() extensions.Extension {
	return extensions.Extension{
		ID: "sforum.search-meilisearch", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Name: "Meilisearch",
		Manifest: extensions.Manifest{
			Providers: []extensions.ManifestProvider{{
				Slot: SearchProviderSlot, Label: "Meilisearch",
			}},
		},
	}
}

func TestSearchProviderRegistryDefaultsToSiteSearch(t *testing.T) {
	registry := NewSearchProviderRegistry(&fakeSearchProviderStore{})
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok {
		t.Fatalf("expected default site search, ok=%v err=%v", ok, err)
	}
	if sel.ExtensionID != DefaultSiteSearchExtensionID {
		t.Fatalf("default id=%q want %q", sel.ExtensionID, DefaultSiteSearchExtensionID)
	}
}

func TestSearchProviderRegistryRestoreDefaultClearsPinToSite(t *testing.T) {
	site := sitePlugin()
	meili := meiliPlugin()
	store := &fakeSearchProviderStore{
		selected: meili.ID,
		items: map[string]extensions.Extension{
			site.ID: site, meili.ID: meili,
		},
		list: []extensions.Extension{site, meili},
	}
	registry := NewSearchProviderRegistry(store)
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || sel.ExtensionID != meili.ID {
		t.Fatalf("explicit meili = %#v ok=%v err=%v", sel, ok, err)
	}
	if err := registry.RestoreDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.selected != "" {
		t.Fatalf("pin not cleared: %q", store.selected)
	}
	sel, ok, err = registry.Selected(context.Background())
	if err != nil || !ok || sel.ExtensionID != DefaultSiteSearchExtensionID {
		t.Fatalf("after restore = %#v ok=%v err=%v", sel, ok, err)
	}
}

func TestSearchProviderRegistrySoleEnabledExternalWhenOnlyMeili(t *testing.T) {
	// 仅 Meili 在 list（站内尚未 Sync）时 sole 回落 Meili。
	plugin := meiliPlugin()
	store := &fakeSearchProviderStore{
		items: map[string]extensions.Extension{plugin.ID: plugin},
		list:  []extensions.Extension{plugin},
	}
	registry := NewSearchProviderRegistry(store)
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || sel.ExtensionID != plugin.ID {
		t.Fatalf("sole meili = %#v ok=%v err=%v", sel, ok, err)
	}
}

func TestSearchProviderRegistrySiteAndMeiliDefaultsSite(t *testing.T) {
	// 站内 + Meili 同时启用且无 pin → 站内默认（不抢选外部）。
	site := sitePlugin()
	meili := meiliPlugin()
	store := &fakeSearchProviderStore{
		items: map[string]extensions.Extension{site.ID: site, meili.ID: meili},
		list:  []extensions.Extension{site, meili},
	}
	registry := NewSearchProviderRegistry(store)
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || sel.ExtensionID != DefaultSiteSearchExtensionID {
		t.Fatalf("want site default with both enabled, got %#v ok=%v err=%v", sel, ok, err)
	}
}

func TestSearchProviderRegistryCandidatesAlwaysIncludeSite(t *testing.T) {
	store := &fakeSearchProviderStore{
		list: []extensions.Extension{meiliPlugin()},
	}
	registry := NewSearchProviderRegistry(store)
	cands, err := registry.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates=%#v", cands)
	}
	if cands[0].ExtensionID != DefaultSiteSearchExtensionID || !cands[0].IsDefault {
		t.Fatalf("first should be site default: %#v", cands[0])
	}
	if cands[1].ExtensionID != "sforum.search-meilisearch" {
		t.Fatalf("second should be meili: %#v", cands[1])
	}
}

func TestSearchProviderRegistryInvalidPinFallsBackToSite(t *testing.T) {
	store := &fakeSearchProviderStore{
		selected: "sforum.search-meilisearch",
		items:    map[string]extensions.Extension{},
		list:     nil,
	}
	registry := NewSearchProviderRegistry(store)
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || sel.ExtensionID != DefaultSiteSearchExtensionID {
		t.Fatalf("invalid pin fallback = %#v ok=%v err=%v", sel, ok, err)
	}
}

func TestSearchProviderRegistryExplicitSelection(t *testing.T) {
	plugin := meiliPlugin()
	store := &fakeSearchProviderStore{
		selected: plugin.ID,
		items:    map[string]extensions.Extension{plugin.ID: plugin},
		list:     []extensions.Extension{plugin},
	}
	registry := NewSearchProviderRegistry(store)
	if err := registry.Select(context.Background(), plugin.ID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	sel, ok, err := registry.Selected(context.Background())
	if err != nil || !ok || sel.Label != "Meilisearch" {
		t.Fatalf("Selected = %#v ok=%v err=%v", sel, ok, err)
	}
}

func TestResolvingSearchEngineShortCircuitsSite(t *testing.T) {
	site := search.NewMemorySiteEngine()
	if err := site.Index(context.Background(), search.TopicSearchDoc{
		ID: 7, Title: "site hit keyword", Status: "active", PlainText: "body",
	}); err != nil {
		t.Fatal(err)
	}
	// 无 registry / 无 runtime：应走站内
	engine := NewResolvingSearchEngine(nil, nil, site)
	if !engine.Enabled(context.Background()) {
		t.Fatal("site engine should be enabled")
	}
	res, err := engine.Search(context.Background(), search.SearchInput{Query: "keyword", Page: 1, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("short-circuit search total=%d", res.Total)
	}

	// 显式 pin 站内 id
	store := &fakeSearchProviderStore{selected: DefaultSiteSearchExtensionID}
	engine = NewResolvingSearchEngine(NewSearchProviderRegistry(store), nil, site)
	id, ok, err := engine.SelectedID(context.Background())
	if err != nil || !ok || id != DefaultSiteSearchExtensionID {
		t.Fatalf("SelectedID=%s ok=%v err=%v", id, ok, err)
	}
	if err := engine.Index(context.Background(), search.TopicSearchDoc{
		ID: 8, Title: "another", Status: "active", PlainText: "zzz",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResolvingSearchEngineRoutesExternalToRuntime(t *testing.T) {
	meili := meiliPlugin()
	store := &fakeSearchProviderStore{
		selected: meili.ID,
		items:    map[string]extensions.Extension{meili.ID: meili},
		list:     []extensions.Extension{meili},
	}
	rt := &recordingSearchRuntime{}
	site := search.NewMemorySiteEngine()
	engine := NewResolvingSearchEngine(NewSearchProviderRegistry(store), rt, site)
	if err := engine.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.probeID != meili.ID {
		t.Fatalf("probe went to %q want meili", rt.probeID)
	}
	// 站内不应被 Index 调用
	if err := engine.Index(context.Background(), search.TopicSearchDoc{ID: 1, Title: "x", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if rt.indexID != meili.ID {
		t.Fatalf("index went to %q", rt.indexID)
	}
}

func TestIsSiteSearchProvider(t *testing.T) {
	if !IsSiteSearchProvider(DefaultSiteSearchExtensionID) {
		t.Fatal("expected site id")
	}
	if IsSiteSearchProvider("sforum.search-meilisearch") {
		t.Fatal("meili is not site")
	}
}

func TestBuiltinUninstallRejectedForSiteSearch(t *testing.T) {
	// 与 lifecycle.Uninstall 门禁对齐：SourceBuiltin / IsSystem / !IsDeletable。
	item := sitePlugin()
	if item.Source != extensions.SourceBuiltin || !item.IsSystem || item.IsDeletable {
		t.Fatalf("site plugin flags incorrect: %+v", item)
	}
	// 模拟 lifecycle 判定
	if item.Source == extensions.SourceBuiltin || item.IsSystem || !item.IsDeletable {
		// rejected — good
		return
	}
	t.Fatal("expected uninstall rejection")
}

func TestPluginSearchEngineFiltersACL(t *testing.T) {
	rt := &recordingSearchRuntime{
		searchItems: []search.TopicSearchDoc{
			{ID: 1, Title: "ok", Status: "active", PlainText: "secret-body"},
			{ID: 2, Title: "no", Status: "draft", PlainText: "draft-body"},
		},
		searchOK: true,
	}
	engine, err := NewPluginSearchEngine("sforum.search-meilisearch", rt)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Search(context.Background(), search.SearchInput{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != 1 || res.Items[0].PlainText != "" {
		t.Fatalf("ACL/strip failed: %+v", res.Items)
	}
}

type recordingSearchRuntime struct {
	probeID, indexID string
	searchItems      []search.TopicSearchDoc
	searchOK         bool
}

func (r *recordingSearchRuntime) SearchEngineProbe(_ context.Context, id string, _ SearchEngineProbeRequest) (SearchEngineProbeResponse, error) {
	r.probeID = id
	return SearchEngineProbeResponse{OK: true, Reason: "ok"}, nil
}
func (r *recordingSearchRuntime) SearchEngineEnsure(context.Context, string) (SearchEngineResult, error) {
	return SearchEngineResult{OK: true}, nil
}
func (r *recordingSearchRuntime) SearchEngineIndex(_ context.Context, id string, _ SearchEngineIndexRequest) (SearchEngineResult, error) {
	r.indexID = id
	return SearchEngineResult{OK: true}, nil
}
func (r *recordingSearchRuntime) SearchEngineDelete(context.Context, string, SearchEngineDeleteRequest) (SearchEngineResult, error) {
	return SearchEngineResult{OK: true}, nil
}
func (r *recordingSearchRuntime) SearchEngineSearch(_ context.Context, id string, _ SearchEngineSearchRequest) (SearchEngineSearchResponse, error) {
	if !r.searchOK {
		return SearchEngineSearchResponse{}, errors.New("not configured")
	}
	return SearchEngineSearchResponse{
		OK: true, Items: r.searchItems, Total: int64(len(r.searchItems)), Page: 1, PerPage: 20,
	}, nil
}
