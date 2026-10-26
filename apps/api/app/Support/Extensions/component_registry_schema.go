package extensionsruntime

import (
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func compileComponentContribution(
	registration componentRuntimeRegistration,
	declaration extensions.ManifestComponent,
) (ComponentContribution, error) {
	extension := registration.extension
	if err := validateComponentDeclaration(extension, declaration); err != nil {
		return ComponentContribution{}, err
	}
	propsValidator, propsDigest, err := compileComponentSchema(extension, declaration.PropsSchema)
	if err != nil {
		return ComponentContribution{}, fmt.Errorf(
			"%w: contribution %s props schema: %v", ErrComponentRegistryInvalid, declaration.ID, err,
		)
	}
	resultValidator, resultDigest, err := compileComponentSchema(extension, declaration.ResultSchema)
	if err != nil {
		return ComponentContribution{}, fmt.Errorf(
			"%w: contribution %s result schema: %v", ErrComponentRegistryInvalid, declaration.ID, err,
		)
	}
	return ComponentContribution{
		ID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Action: declaration.Action, TargetID: declaration.TargetID,
		TargetContractVersion: declaration.TargetContractVersion, Priority: declaration.Priority,
		Permission:  declaration.Permission,
		SSRTemplate: declaration.SSRTemplate, L2Component: declaration.L2Component,
		PropsSchema: declaration.PropsSchema, PropsSchemaDigest: propsDigest,
		ResultSchema: declaration.ResultSchema, ResultSchemaDigest: resultDigest,
		ThemeOverrideKey: declaration.ThemeOverrideKey,
		Artifact: HookArtifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, RuntimeInstanceID: registration.instanceID,
		},
		manifest: declaration, propsValidator: propsValidator, resultValidator: resultValidator,
	}, nil
}

func validateComponentDeclaration(extension extensions.Extension, declaration extensions.ManifestComponent) error {
	if !strings.HasPrefix(declaration.ID, extension.ID+".") || strings.TrimSpace(declaration.ContractVersion) == "" ||
		!validComponentRegistryAction(declaration.Action) ||
		extension.Type == extensions.TypeTheme && (declaration.Action == extensionmanifest.ComponentActionFilterProps ||
			declaration.Action == extensionmanifest.ComponentActionFilterResult) ||
		(declaration.TargetID == "") != (declaration.TargetContractVersion == "") ||
		declaration.Action != extensionmanifest.ComponentActionAdd && declaration.TargetID == "" ||
		declaration.Action != extensionmanifest.ComponentActionHide && strings.TrimSpace(declaration.PropsSchema) == "" ||
		componentActionNeedsResult(declaration.Action) && strings.TrimSpace(declaration.ResultSchema) == "" ||
		declaration.Action != extensionmanifest.ComponentActionHide && declaration.SSRTemplate == "" && declaration.L2Component == "" {
		return fmt.Errorf("%w: contribution %s", ErrComponentRegistryInvalid, declaration.ID)
	}
	if declaration.SSRTemplate != "" && !componentTemplateDeclared(extension, declaration.SSRTemplate) {
		return fmt.Errorf("%w: contribution %s template", ErrComponentRegistryInvalid, declaration.ID)
	}
	if declaration.L2Component != "" && !componentFrontendDeclared(extension, declaration.L2Component) {
		return fmt.Errorf("%w: contribution %s L2 component", ErrComponentRegistryInvalid, declaration.ID)
	}
	return nil
}

func componentActionNeedsResult(action string) bool {
	return action == extensionmanifest.ComponentActionWrap ||
		action == extensionmanifest.ComponentActionReplace ||
		action == extensionmanifest.ComponentActionFilterResult
}

func compileComponentSchema(
	extension extensions.Extension,
	reference string,
) (providerDocumentValidator, string, error) {
	if strings.TrimSpace(reference) == "" {
		return nil, "", nil
	}
	return compileExactProviderSchema(extension, reference)
}

func validateComponentUpgrade(previous, next extensions.Extension) error {
	oldComponents := make(map[string]extensions.ManifestComponent, len(previous.Manifest.Components))
	for _, component := range previous.Manifest.Components {
		oldComponents[component.ID] = component
	}
	for _, component := range next.Manifest.Components {
		old, found := oldComponents[component.ID]
		if !found || old.ContractVersion != component.ContractVersion {
			continue
		}
		if old != component || componentReferencedContentChanged(previous, next, old) {
			return fmt.Errorf(
				"%w: contribution %s changed without a contract version: bump %s.contractVersion in %s/sforum.extension.json whenever the contribution or the props schema / L2 artifact it references changes",
				ErrComponentRegistryConflict, component.ID, component.ID, next.ID,
			)
		}
	}
	return nil
}

func componentReferencedContentChanged(
	previous extensions.Extension,
	next extensions.Extension,
	component extensions.ManifestComponent,
) bool {
	for _, reference := range []string{component.PropsSchema, component.ResultSchema} {
		if reference == "" {
			continue
		}
		oldDigest, oldFound := providerSchemaDigest(previous, reference)
		newDigest, newFound := providerSchemaDigest(next, reference)
		if oldFound != newFound || oldFound && oldDigest != newDigest {
			return true
		}
	}
	if component.SSRTemplate != "" {
		oldTemplate, oldFound := componentTemplate(previous, component.SSRTemplate)
		newTemplate, newFound := componentTemplate(next, component.SSRTemplate)
		if oldFound != newFound || oldFound && oldTemplate != newTemplate {
			return true
		}
	}
	if component.L2Component != "" {
		oldFile, oldFound := componentPackageFile(previous, component.L2Component)
		newFile, newFound := componentPackageFile(next, component.L2Component)
		if oldFound != newFound || oldFound && oldFile != newFile {
			return true
		}
	}
	return false
}

func componentTemplateDeclared(extension extensions.Extension, id string) bool {
	_, found := componentTemplate(extension, id)
	return found
}

func componentTemplate(extension extensions.Extension, id string) (extensions.ManifestTemplate, bool) {
	for _, template := range extension.Manifest.Templates {
		if template.ID == id {
			return template, true
		}
	}
	return extensions.ManifestTemplate{}, false
}

func componentFrontendDeclared(extension extensions.Extension, id string) bool {
	file, found := componentPackageFile(extension, id)
	return found && file.Kind == "frontend"
}

func componentPackageFile(extension extensions.Extension, id string) (extensions.ManifestPackageFile, bool) {
	for _, file := range extension.Manifest.PackageFiles {
		if file.ID == id {
			return file, true
		}
	}
	return extensions.ManifestPackageFile{}, false
}
