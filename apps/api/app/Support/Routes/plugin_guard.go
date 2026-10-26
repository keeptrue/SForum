package routes

import (
	"fmt"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// PluginGuardBinding freezes the executable guard declaration selected from the
// same exact package as the route. Runtime code must never resolve it from a
// mutable manifest after the Route Registry snapshot has been published.
type PluginGuardBinding struct {
	ID              string   `json:"id"`
	ContractVersion string   `json:"contractVersion"`
	Kind            string   `json:"kind"`
	Entry           string   `json:"entry"`
	Digest          string   `json:"digest"`
	Permissions     []string `json:"permissions,omitempty"`
}

func preparePluginGuardBindings(
	artifact PluginArtifact,
	declarations []extensionmanifest.ManifestGuard,
) (map[string]PluginGuardBinding, error) {
	bindings := make(map[string]PluginGuardBinding, len(declarations))
	for _, declaration := range declarations {
		if !routeIDPattern.MatchString(declaration.ID) || !strings.HasPrefix(declaration.ID, artifact.ExtensionID+".") ||
			!contractPattern.MatchString(declaration.ContractVersion) || !validRouteHandler(declaration.Entry) ||
			!packageDigestPattern.MatchString(declaration.Digest) ||
			(declaration.Kind != "custom" && declaration.Kind != "raw_request") {
			return nil, fmt.Errorf("%w: invalid plugin guard declaration", ErrInvalidRoute)
		}
		if _, duplicate := bindings[declaration.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate plugin guard %q", ErrInvalidRoute, declaration.ID)
		}
		permissions := make([]string, 0, len(declaration.Permissions))
		seen := make(map[string]struct{}, len(declaration.Permissions))
		for _, permission := range declaration.Permissions {
			if !routeIDPattern.MatchString(permission) {
				return nil, fmt.Errorf("%w: invalid plugin guard permission", ErrInvalidRoute)
			}
			if _, duplicate := seen[permission]; duplicate {
				return nil, fmt.Errorf("%w: duplicate plugin guard permission", ErrInvalidRoute)
			}
			seen[permission] = struct{}{}
			permissions = append(permissions, permission)
		}
		bindings[declaration.ID] = PluginGuardBinding{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion, Kind: declaration.Kind,
			Entry: declaration.Entry, Digest: declaration.Digest, Permissions: permissions,
		}
	}
	return bindings, nil
}

func pluginGuardBindingForRoute(
	extensionID string,
	guard string,
	bindings map[string]PluginGuardBinding,
) (PluginGuardBinding, error) {
	if !strings.HasPrefix(guard, extensionID+".") {
		return PluginGuardBinding{}, nil
	}
	binding, ok := bindings[guard]
	if !ok || binding.ID != guard {
		return PluginGuardBinding{}, fmt.Errorf("%w: custom guard %q is not declared by the exact package", ErrInvalidRoute, guard)
	}
	return clonePluginGuardBinding(binding), nil
}

func clonePluginGuardBinding(value PluginGuardBinding) PluginGuardBinding {
	value.Permissions = append([]string(nil), value.Permissions...)
	return value
}

func equalPluginGuardBinding(left, right PluginGuardBinding) bool {
	if left.ID != right.ID || left.ContractVersion != right.ContractVersion || left.Kind != right.Kind ||
		left.Entry != right.Entry || left.Digest != right.Digest || len(left.Permissions) != len(right.Permissions) {
		return false
	}
	for index := range left.Permissions {
		if left.Permissions[index] != right.Permissions[index] {
			return false
		}
	}
	return true
}
