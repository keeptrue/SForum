package sitechrome

import (
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

const NavigationViewModelSchemaVersion = "sforum.site-chrome-navigation@1"

type NavigationCompositionInput struct {
	Actor             identity.Actor
	Locale            string
	HiddenIDs         []string
	DisabledProviders []navigationregistry.ProviderRef
}

// ChromeNodeViewModel is the public, artifact-redacted projection shared by
// navigation and region surfaces. Exact package/runtime attribution remains in
// the permissioned NavigationRegistry Inspector rather than public chrome.
type ChromeNodeViewModel struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Href  string `json:"href,omitempty"`
	// Content is plain text. Rendering it as HTML would cross the trusted L2
	// boundary and is deliberately outside this view model.
	Content    string                `json:"content,omitempty"`
	Multiple   bool                  `json:"multiple,omitempty"`
	Attributes map[string]string     `json:"attributes,omitempty"`
	Wrappers   []ChromeNodeViewModel `json:"wrappers,omitempty"`
	Children   []ChromeNodeViewModel `json:"children,omitempty"`
}

type RegionViewModels struct {
	Menus    []ChromeNodeViewModel `json:"menus"`
	Headers  []ChromeNodeViewModel `json:"headers"`
	Footers  []ChromeNodeViewModel `json:"footers"`
	Sidebars []ChromeNodeViewModel `json:"sidebars"`
	Widgets  []ChromeNodeViewModel `json:"widgets"`
	Content  []ChromeNodeViewModel `json:"content"`
}

type NavigationRegionViewModel struct {
	SchemaVersion string                `json:"schemaVersion"`
	Revision      uint64                `json:"revision"`
	Digest        string                `json:"digest"`
	SafeMode      bool                  `json:"safeMode,omitempty"`
	Locale        string                `json:"locale"`
	CacheKey      string                `json:"cacheKey"`
	Menus         []ChromeNodeViewModel `json:"menus"`
	Breadcrumbs   []ChromeNodeViewModel `json:"breadcrumbs"`
	Headers       []ChromeNodeViewModel `json:"headers"`
	Footers       []ChromeNodeViewModel `json:"footers"`
	Sidebars      []ChromeNodeViewModel `json:"sidebars"`
	Regions       RegionViewModels      `json:"regions"`
}
