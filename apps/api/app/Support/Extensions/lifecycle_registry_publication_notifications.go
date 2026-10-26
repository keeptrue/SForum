package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

func notificationDescriptorOwner(material *lifecycleRegistryMaterial) notifications.DescriptorOwner {
	if material == nil {
		return notifications.DescriptorOwner{}
	}
	return notifications.DescriptorOwner{
		ExtensionID: material.extension.ID, Version: material.extension.Version,
		ArtifactDigest: material.extension.PackageDigest,
	}
}

func (b *PostgresLifecycleBoundaryRegistries) validateNotificationTransition(target *lifecycleRegistryMaterial) error {
	if target == nil || len(target.extension.Manifest.NotificationTypes) == 0 {
		return nil
	}
	if err := b.notifications.ValidatePublish(notificationDescriptorOwner(target), target.extension.Manifest.NotificationTypes); err != nil {
		return fmt.Errorf("validate notification registry: %w", err)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileNotifications(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if b.notifications == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	snapshot := b.notifications.Snapshot()
	currentOwner, hasCurrent := notificationOwnerForExtension(snapshot, extensionID)
	if desired == nil || len(desired.extension.Manifest.NotificationTypes) == 0 {
		if !hasCurrent {
			return nil
		}
		if source != nil && currentOwner != notificationDescriptorOwner(source) &&
			target != nil && currentOwner != notificationDescriptorOwner(target) {
			return fmt.Errorf("%w: notification descriptor artifact", ErrLifecycleRegistryPublicationConflict)
		}
		_, err := b.notifications.Deactivate(ctx, currentOwner, snapshot.Revision)
		if errors.Is(err, notifications.ErrRegistryRevisionConflict) {
			return fmt.Errorf("%w: notification registry revision", ErrLifecycleRegistryPublicationConflict)
		}
		return err
	}
	owner := notificationDescriptorOwner(desired)
	if hasCurrent && currentOwner != owner {
		allowed := source != nil && currentOwner == notificationDescriptorOwner(source) ||
			target != nil && currentOwner == notificationDescriptorOwner(target)
		if !allowed {
			return fmt.Errorf("%w: notification descriptor artifact", ErrLifecycleRegistryPublicationConflict)
		}
	}
	_, err := b.notifications.Publish(ctx, owner, desired.extension.Manifest.NotificationTypes, snapshot.Revision)
	if errors.Is(err, notifications.ErrRegistryRevisionConflict) {
		return fmt.Errorf("%w: notification registry revision", ErrLifecycleRegistryPublicationConflict)
	}
	return err
}

func notificationOwnerForExtension(snapshot notifications.RegistrySnapshot, extensionID string) (notifications.DescriptorOwner, bool) {
	for _, descriptor := range snapshot.Descriptors {
		if descriptor.Owner.ExtensionID == extensionID {
			return descriptor.Owner, true
		}
	}
	return notifications.DescriptorOwner{}, false
}

func (b *PostgresLifecycleBoundaryRegistries) restoreNotificationPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b.notifications == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if safeMode {
		_, err := b.notifications.Restore(ctx, nil, true)
		return err
	}
	publications := make([]notifications.RegistryPublication, 0, len(items))
	for _, item := range items {
		if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled || len(item.Manifest.NotificationTypes) == 0 {
			continue
		}
		// Pure declarations are inert package data. Executable plugins must also
		// retain the exact available runtime that owns Host API emission.
		if strings.TrimSpace(item.Manifest.Backend.Entry) != "" {
			if b.manager == nil {
				return ErrLifecycleRegistryPublicationUnavailable
			}
			runtime, err := b.manager.ActiveRuntimeInstance(item.ID)
			if err != nil {
				continue
			}
			if !runtimeInstanceMatchesExtension(runtime, item) || !b.manager.RuntimeInstanceAvailable(runtime.Identity) {
				return fmt.Errorf("%w: startup notification runtime for %s is not exact and available",
					ErrLifecycleRegistryPublicationConflict, item.ID)
			}
		}
		publications = append(publications, notifications.RegistryPublication{
			Owner: notifications.DescriptorOwner{
				ExtensionID: item.ID, Version: item.Version, ArtifactDigest: item.PackageDigest,
			},
			Declarations: item.Manifest.NotificationTypes,
		})
	}
	if _, err := b.notifications.Restore(ctx, publications, false); err != nil {
		return fmt.Errorf("restore notification publications: %w", err)
	}
	return nil
}
