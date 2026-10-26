package extensionsruntime

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// SearchProviderSlot 与 search.ProviderSlot 一致。
const SearchProviderSlot = search.ProviderSlot

// DefaultSiteSearchExtensionID 与 search 包默认站内扩展 id 一致。
const DefaultSiteSearchExtensionID = search.DefaultSiteSearchExtensionID

var ErrSearchProviderUnavailable = errors.New("search provider is unavailable")

type SearchProviderSelection struct {
	ExtensionID string
	Label       string
}

type SearchProviderStore interface {
	GetSearchProviderExtension(context.Context, string) (extensions.Extension, error)
	SelectedSearchProvider(context.Context) (string, error)
	SelectSearchProvider(context.Context, string) error
	RestoreSearchProvider(context.Context) error
	// List 用于在无显式选择时回退到唯一已启用的 search.provider 插件。
	List(context.Context) ([]extensions.Extension, error)
}

// SearchProviderRegistry 管理 search.provider 槽位的运营选择。
// 默认：受保护内置站内搜索 sforum.search-site（无显式 pin 时始终可用）。
type SearchProviderRegistry struct{ store SearchProviderStore }

func NewSearchProviderRegistry(store SearchProviderStore) *SearchProviderRegistry {
	return &SearchProviderRegistry{store: store}
}

func (r *SearchProviderRegistry) Select(ctx context.Context, extensionID string) error {
	if r == nil || r.store == nil {
		return ErrSearchProviderUnavailable
	}
	item, err := r.store.GetSearchProviderExtension(ctx, extensionID)
	if err != nil || item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled {
		return ErrSearchProviderUnavailable
	}
	for _, provider := range item.Manifest.Providers {
		if provider.Slot == SearchProviderSlot {
			return r.store.SelectSearchProvider(ctx, extensionID)
		}
	}
	// 站内默认 id 允许在扩展尚未 SyncBuiltins 完成时仍被 pin（Host 短路）。
	if extensionID == DefaultSiteSearchExtensionID {
		return r.store.SelectSearchProvider(ctx, extensionID)
	}
	return ErrSearchProviderUnavailable
}

func (r *SearchProviderRegistry) Selected(ctx context.Context) (SearchProviderSelection, bool, error) {
	if r == nil || r.store == nil {
		return siteSearchSelection(), true, nil
	}
	id, err := r.store.SelectedSearchProvider(ctx)
	if err != nil {
		return SearchProviderSelection{}, false, err
	}
	if id != "" {
		// 显式 pin 站内搜索：即使扩展行尚未加载也可用（Host 短路）。
		if id == DefaultSiteSearchExtensionID {
			item, getErr := r.store.GetSearchProviderExtension(ctx, id)
			if getErr == nil {
				if label, ok := searchProviderLabel(item); ok {
					return SearchProviderSelection{ExtensionID: id, Label: label}, true, nil
				}
			}
			return siteSearchSelection(), true, nil
		}
		item, getErr := r.store.GetSearchProviderExtension(ctx, id)
		if getErr != nil || item.Status != extensions.StatusEnabled {
			// pin 失效：回落站内默认，而不是 503。
			return siteSearchSelection(), true, nil
		}
		if label, ok := searchProviderLabel(item); ok {
			return SearchProviderSelection{ExtensionID: id, Label: label}, true, nil
		}
		return siteSearchSelection(), true, nil
	}
	// 无显式选择：若恰好只有一个已启用的 search.provider 且不是站内默认，自动使用（便于可选插件上手）。
	// 若存在站内默认 + 其它插件，不自动抢选外部引擎。
	if sole, ok, err := r.soleEnabled(ctx); err != nil {
		return SearchProviderSelection{}, false, err
	} else if ok && sole.ExtensionID != DefaultSiteSearchExtensionID {
		// 仅当唯一候选且不是站内时才自动选中（兼容仅装了 Meili 的场景）。
		// 若站内也在 list 中，soleEnabled 会 count>1 返回 false → 走站内默认。
		return sole, true, nil
	}
	return siteSearchSelection(), true, nil
}

func siteSearchSelection() SearchProviderSelection {
	return SearchProviderSelection{
		ExtensionID: DefaultSiteSearchExtensionID,
		Label:       "Site Search",
	}
}

func (r *SearchProviderRegistry) soleEnabled(ctx context.Context) (SearchProviderSelection, bool, error) {
	items, err := r.store.List(ctx)
	if err != nil {
		return SearchProviderSelection{}, false, err
	}
	var found SearchProviderSelection
	count := 0
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled {
			continue
		}
		label, ok := searchProviderLabel(item)
		if !ok {
			continue
		}
		count++
		found = SearchProviderSelection{ExtensionID: item.ID, Label: label}
		if count > 1 {
			return SearchProviderSelection{}, false, nil
		}
	}
	if count == 1 {
		return found, true, nil
	}
	return SearchProviderSelection{}, false, nil
}

func searchProviderLabel(item extensions.Extension) (string, bool) {
	for _, provider := range item.Manifest.Providers {
		if provider.Slot == SearchProviderSlot {
			label := provider.Label
			if label == "" {
				label = item.Name
			}
			if label == "" {
				label = item.ID
			}
			return label, true
		}
	}
	return "", false
}

// RestoreDefault 清除显式 pin；Selected 随后解析为站内搜索。
func (r *SearchProviderRegistry) RestoreDefault(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.RestoreSearchProvider(ctx)
}

// IsPinned 是否存在运营显式 pin（与解析后的 Selected 不同：无 pin 时仍可能解析到站内默认）。
func (r *SearchProviderRegistry) IsPinned(ctx context.Context) (bool, error) {
	if r == nil || r.store == nil {
		return false, nil
	}
	id, err := r.store.SelectedSearchProvider(ctx)
	if err != nil {
		return false, err
	}
	return id != "", nil
}

// SearchProviderCandidate 运营可选的 search.provider 候选。
type SearchProviderCandidate struct {
	ExtensionID string
	Label       string
	Healthy     bool
	IsDefault   bool
}

// Candidates 列出已启用的 search.provider 候选；始终包含站内默认。
func (r *SearchProviderRegistry) Candidates(ctx context.Context) ([]SearchProviderCandidate, error) {
	out := []SearchProviderCandidate{{
		ExtensionID: DefaultSiteSearchExtensionID,
		Label:       "Site Search",
		Healthy:     true,
		IsDefault:   true,
	}}
	if r == nil || r.store == nil {
		return out, nil
	}
	items, err := r.store.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{DefaultSiteSearchExtensionID: true}
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled {
			continue
		}
		label, ok := searchProviderLabel(item)
		if !ok {
			continue
		}
		if seen[item.ID] {
			// 站内扩展行存在时用清单 label 覆盖默认文案。
			if item.ID == DefaultSiteSearchExtensionID {
				out[0].Label = label
				out[0].Healthy = item.Runtime == nil || item.Runtime.State == extensions.RuntimeRunning
			}
			continue
		}
		seen[item.ID] = true
		out = append(out, SearchProviderCandidate{
			ExtensionID: item.ID,
			Label:       label,
			Healthy:     item.Runtime == nil || item.Runtime.State == extensions.RuntimeRunning,
			IsDefault:   item.ID == DefaultSiteSearchExtensionID,
		})
	}
	return out, nil
}

// IsSiteSearchProvider 判断扩展 id 是否为 Host 短路的站内搜索。
func IsSiteSearchProvider(extensionID string) bool {
	return extensionID == DefaultSiteSearchExtensionID
}
