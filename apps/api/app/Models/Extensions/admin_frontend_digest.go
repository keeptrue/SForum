package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	maxPrebuiltAdminModuleBytes = 2 * 1024 * 1024
	maxPrebuiltAdminCSSBytes    = 1 * 1024 * 1024
)

type adminFrontendDigestContract struct {
	Surface     string         `json:"surface"`
	PagePath    string         `json:"pagePath,omitempty"`
	Component   AdminComponent `json:"component"`
	EntryDigest string         `json:"entryDigest"`
	CSSDigest   string         `json:"cssDigest,omitempty"`
}

type adminFrontendComponentMaterial struct {
	Component AdminComponent
	Entry     []byte
	CSS       []byte
}

type adminFrontendMaterial struct {
	Digest     string
	Components map[string]adminFrontendComponentMaterial
}

type adminComponentSurface struct {
	component AdminComponent
	page      *ManifestAdminPage
	settings  bool
}

func declaredAdminComponentSurfaces(manifest Manifest) []adminComponentSurface {
	bindings := extensionmanifest.DeclaredAdminComponents(manifest)
	result := make([]adminComponentSurface, 0, len(bindings))
	for _, binding := range bindings {
		surface := adminComponentSurface{component: binding.Component, settings: binding.Surface == "settings"}
		if binding.PagePath != "" {
			for _, page := range extensionmanifest.EffectiveAdminPages(manifest) {
				if page.Path == binding.PagePath && page.Component != nil && page.Component.ID == binding.Component.ID {
					copy := page
					surface.page = &copy
					break
				}
			}
		}
		result = append(result, surface)
	}
	return result
}

func primaryAdminComponent(extension Extension) *AdminComponent {
	if component := prebuiltSettingsComponent(extension); component != nil {
		copy := AdminComponent(*component)
		return &copy
	}
	surfaces := declaredAdminComponentSurfaces(extension.Manifest)
	if len(surfaces) == 0 {
		return nil
	}
	copy := surfaces[0].component
	return &copy
}

func adminComponentIDs(extension Extension) []string {
	surfaces := declaredAdminComponentSurfaces(extension.Manifest)
	result := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		result = append(result, surface.component.ID)
	}
	return result
}

func findAdminComponentSurface(extension Extension, componentID string) (adminComponentSurface, bool) {
	componentID = strings.TrimSpace(componentID)
	for _, surface := range declaredAdminComponentSurfaces(extension.Manifest) {
		if surface.component.ID == componentID {
			return surface, true
		}
	}
	return adminComponentSurface{}, false
}

func canAccessAdminComponent(actor identity.Actor, extension Extension, surface adminComponentSurface) bool {
	if surface.settings {
		return canManageExtensionSettings(actor, extension)
	}
	if surface.page == nil || extension.Status != StatusEnabled || !canViewExtensions(actor) {
		return false
	}
	permission := strings.TrimSpace(surface.page.Permission)
	return permission == "" || actor.Can(permission)
}

// ComputeAdminFrontendDigest covers every author-prebuilt admin component and
// the exact surface contract that admits it.
func ComputeAdminFrontendDigest(manifest Manifest, packageRoot string) (string, error) {
	material, err := readAdminFrontendMaterial(manifest, packageRoot)
	return material.Digest, err
}

// readAdminFrontendMaterial 从同一批稳定、根目录限域的文件句柄构造授权摘要和
// 实际返回字节。调用方不得在验证 Digest 后二次读盘。
func readAdminFrontendMaterial(manifest Manifest, packageRoot string) (adminFrontendMaterial, error) {
	contracts := make([]adminFrontendDigestContract, 0)
	material := adminFrontendMaterial{Components: map[string]adminFrontendComponentMaterial{}}
	for _, binding := range extensionmanifest.DeclaredAdminComponents(manifest) {
		component := binding.Component
		entry, err := readAdminAsset(packageRoot, component.Entry, maxPrebuiltAdminModuleBytes)
		if err != nil {
			return adminFrontendMaterial{}, err
		}
		current := adminFrontendComponentMaterial{Component: component, Entry: entry}
		contract := adminFrontendDigestContract{
			Surface: binding.Surface, PagePath: binding.PagePath, Component: component,
			EntryDigest: digestAdminAssetBytes(entry),
		}
		if component.CSS != "" {
			current.CSS, err = readAdminAsset(packageRoot, component.CSS, maxPrebuiltAdminCSSBytes)
			if err != nil {
				return adminFrontendMaterial{}, err
			}
			contract.CSSDigest = digestAdminAssetBytes(current.CSS)
		}
		material.Components[component.ID] = current
		contracts = append(contracts, contract)
	}
	if len(contracts) == 0 {
		return material, nil
	}
	body, err := json.Marshal(contracts)
	if err != nil {
		return adminFrontendMaterial{}, err
	}
	digest := sha256.Sum256(body)
	material.Digest = hex.EncodeToString(digest[:])
	return material, nil
}

func readAdminAsset(packageRoot, relative string, limit int) ([]byte, error) {
	body, err := readStableExtensionFile(
		Extension{PackagePath: packageRoot}, relative, int64(limit), false,
	)
	if err != nil {
		return nil, fmt.Errorf("read admin asset %s: %w", relative, err)
	}
	return body, nil
}

func digestAdminAssetBytes(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
