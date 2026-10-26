package extensionsruntime

import (
	"encoding/hex"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// ValidateThemeTransition builds the exact post-switch component graph without
// publishing it. Theme activation calls this before its database CAS.
func (r *ComponentRegistry) ValidateThemeTransition(target, source *extensions.Extension) error {
	return r.transitionTheme(target, source, 0, false, false)
}

// PublishThemeTransition atomically replaces the exact active theme component
// publication. The durable theme publication revision makes two transitions
// from the same source deterministic even when they arrive out of order.
func (r *ComponentRegistry) PublishThemeTransition(
	target,
	source *extensions.Extension,
	publicationRevision int64,
) error {
	return r.transitionTheme(target, source, publicationRevision, true, false)
}

// RollbackThemeTransition restores a process-local transition whose later Page
// publication failed. It succeeds only while that exact durable revision is
// still current, so a delayed rollback cannot revoke a newer artifact.
func (r *ComponentRegistry) RollbackThemeTransition(
	target,
	source *extensions.Extension,
	publicationRevision int64,
) error {
	return r.transitionTheme(target, source, publicationRevision, true, true)
}

func (r *ComponentRegistry) transitionTheme(
	target,
	source *extensions.Extension,
	publicationRevision int64,
	publish bool,
	rollback bool,
) error {
	if r == nil || validateThemeTransitionArtifact(target) != nil || validateThemeTransitionArtifact(source) != nil ||
		(publish && publicationRevision <= 0) || rollback && !publish {
		return ErrComponentRegistryInvalid
	}
	if target != nil && source != nil && target.ID == source.ID &&
		!componentThemeArtifactMatches(*target, *source) {
		if err := validateComponentUpgrade(*source, *target); err != nil {
			return err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if rollback {
		if r.themePublicationRevision != publicationRevision {
			return ErrComponentRegistryRevisionConflict
		}
	} else if publish && publicationRevision < r.themePublicationRevision {
		return ErrComponentRegistryRevisionConflict
	}
	current := r.load()
	if current.safeMode {
		return ErrComponentRegistrySafeMode
	}
	registrations := cloneComponentRegistrations(current.registrations)
	for extensionID, registration := range registrations {
		if registration.extension.Type != extensions.TypeTheme {
			continue
		}
		if themeRegistrationMatches(registration, target) || themeRegistrationMatches(registration, source) {
			if target != nil && source != nil && target.ID == source.ID &&
				themeRegistrationMatches(registration, source) &&
				!componentThemeArtifactMatches(*target, *source) {
				if err := validateComponentUpgrade(registration.extension, *target); err != nil {
					return err
				}
			}
			delete(registrations, extensionID)
			continue
		}
		// A stale activation may not remove or coexist with a newer exact theme.
		return fmt.Errorf(
			"%w: active theme component runtime %s is outside the transition fence",
			ErrComponentRegistryConflict,
			extensionID,
		)
	}

	if rollback {
		if r.themePreviousRegistrations == nil {
			return ErrComponentRegistryRevisionConflict
		}
		for extensionID, registration := range r.themePreviousRegistrations {
			registrations[extensionID] = componentRuntimeRegistration{
				extension:  cloneComponentExtension(registration.extension),
				instanceID: registration.instanceID,
			}
		}
	} else if target != nil && len(target.Manifest.Components) > 0 {
		instanceID := componentPackageRuntimeInstanceID(*target)
		if validateComponentRuntime(*target, instanceID) != nil {
			return ErrComponentRegistryInvalid
		}
		registrations[target.ID] = componentRuntimeRegistration{
			extension:  cloneComponentExtension(*target),
			instanceID: instanceID,
		}
	}
	registrationsMatch := componentRuntimeRegistrationsMatch(current.registrations, registrations)
	if publish && !rollback && publicationRevision == r.themePublicationRevision {
		if !registrationsMatch {
			return ErrComponentRegistryRevisionConflict
		}
		return nil
	}
	if !registrationsMatch {
		next, err := buildComponentRegistryState(current.revision+1, registrations, current.selectionsByTarget)
		if err != nil {
			return err
		}
		if publish {
			r.state.Store(next)
		}
	} else if !publish {
		return nil
	}
	if publish {
		if rollback {
			r.themePublicationRevision = r.themePreviousPublicationRevision
			r.themePreviousPublicationRevision = 0
			r.themePreviousRegistrations = nil
		} else {
			r.themePreviousRegistrations = themeComponentRegistrations(current.registrations)
			r.themePreviousPublicationRevision = r.themePublicationRevision
			r.themePublicationRevision = publicationRevision
		}
	}
	return nil
}

func themeComponentRegistrations(
	registrations map[string]componentRuntimeRegistration,
) map[string]componentRuntimeRegistration {
	result := make(map[string]componentRuntimeRegistration)
	for extensionID, registration := range registrations {
		if registration.extension.Type != extensions.TypeTheme {
			continue
		}
		result[extensionID] = componentRuntimeRegistration{
			extension:  cloneComponentExtension(registration.extension),
			instanceID: registration.instanceID,
		}
	}
	return result
}

func validateThemeTransitionArtifact(extension *extensions.Extension) error {
	if extension == nil {
		return nil
	}
	if extension.Type != extensions.TypeTheme || strings.TrimSpace(extension.ID) == "" ||
		extension.ID != strings.TrimSpace(extension.ID) || strings.TrimSpace(extension.Version) == "" ||
		extension.Version != strings.TrimSpace(extension.Version) || !validThemeTransitionDigest(extension.PackageDigest) {
		return ErrComponentRegistryInvalid
	}
	if len(extension.Manifest.Components) == 0 {
		return nil
	}
	return validateComponentRuntime(*extension, componentPackageRuntimeInstanceID(*extension))
}

func validThemeTransitionDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) || value != strings.TrimSpace(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func componentThemeArtifactMatches(left, right extensions.Extension) bool {
	return left.ID == right.ID && left.Type == extensions.TypeTheme && right.Type == extensions.TypeTheme &&
		left.Version == right.Version && left.PackageDigest == right.PackageDigest
}

func themeRegistrationMatches(
	registration componentRuntimeRegistration,
	extension *extensions.Extension,
) bool {
	return extension != nil && componentRuntimeRegistrationMatches(
		registration,
		*extension,
		componentPackageRuntimeInstanceID(*extension),
	)
}
