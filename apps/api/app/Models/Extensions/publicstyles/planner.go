package publicstyles

import (
	"errors"
	"slices"
	"sort"
	"strings"

	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
)

var ErrInvalid = errors.New("public content styles: invalid asset plan")

// Plan selects one immutable dependency-first CSS plan for a declared public
// surface. Executable dependencies and external CSP are rejected because this
// catalog is loaded as passive document presentation, not browser code.
func Plan(snapshot assetregistry.Snapshot, scope string) ([]assetregistry.Asset, error) {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(snapshot.Digest) == "" {
		return nil, ErrInvalid
	}
	assets := make(map[string]assetregistry.Asset, len(snapshot.Assets))
	roots := make([]string, 0)
	for _, asset := range snapshot.Assets {
		if asset.Handle == "" {
			return nil, ErrInvalid
		}
		if _, duplicate := assets[asset.Handle]; duplicate {
			return nil, ErrInvalid
		}
		assets[asset.Handle] = asset
		if asset.Type == "style" && asset.Loading == "blocking" && slices.Contains(asset.Scope, scope) {
			roots = append(roots, asset.Handle)
		}
	}
	sort.Strings(roots)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	result := make([]assetregistry.Asset, 0, len(roots))
	var visit func(string) error
	visit = func(handle string) error {
		if visited[handle] {
			return nil
		}
		if visiting[handle] {
			return ErrInvalid
		}
		asset, found := assets[handle]
		if !found || asset.Type != "style" || asset.Module || !sameOriginStyleCSP(asset.CSP) {
			return ErrInvalid
		}
		visiting[handle] = true
		for _, dependency := range asset.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, handle)
		visited[handle] = true
		result = append(result, asset)
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func sameOriginStyleCSP(declarations []string) bool {
	for _, declaration := range declarations {
		if declaration != "style-src 'self'" {
			return false
		}
	}
	return true
}
