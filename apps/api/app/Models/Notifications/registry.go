package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	ErrRegistryDescriptorInvalid = errors.New("notifications: registry descriptor invalid")
	ErrRegistryOwnerConflict     = errors.New("notifications: registry owner conflict")
	ErrRegistryRevisionConflict  = errors.New("notifications: registry revision conflict")
)

var (
	notificationTypeIDPattern         = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,160}$`)
	notificationArtifactDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type DescriptorOwner struct {
	ExtensionID    string `json:"extensionId,omitempty"`
	Version        string `json:"version,omitempty"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
}

type TypeDescriptor struct {
	Type                string                          `json:"type"`
	ContractVersion     int                             `json:"contractVersion"`
	PayloadVersion      int                             `json:"payloadVersion"`
	Owner               DescriptorOwner                 `json:"owner"`
	Category            string                          `json:"category"`
	PayloadSchema       string                          `json:"payloadSchema,omitempty"`
	Label               extensionmanifest.LocalizedText `json:"label"`
	Body                extensionmanifest.LocalizedText `json:"body"`
	Icon                string                          `json:"icon,omitempty"`
	TargetKind          string                          `json:"targetKind"`
	TargetID            string                          `json:"targetId,omitempty"`
	Channels            []string                        `json:"channels"`
	RecommendedChannels []string                        `json:"recommendedChannels,omitempty"`
	Required            bool                            `json:"required"`
	Active              bool                            `json:"active"`
}

type RegistrySnapshot struct {
	Revision    uint64
	SafeMode    bool
	Descriptors map[string]TypeDescriptor
}

// RegistryPublication is one plugin's complete declaration set for an exact
// artifact. Startup restore replaces the whole plugin graph with these items.
type RegistryPublication struct {
	Owner        DescriptorOwner
	Declarations []extensionmanifest.ManifestNotificationType
}

type Registry struct {
	mu       sync.RWMutex
	revision uint64
	safeMode bool
	core     map[string]TypeDescriptor
	plugins  map[string]map[string]TypeDescriptor
	snapshot RegistrySnapshot
	pool     *pgxpool.Pool
}

func NewRegistry() *Registry {
	core := coreTypeDescriptors()
	r := &Registry{core: core, plugins: make(map[string]map[string]TypeDescriptor)}
	r.rebuildLocked()
	return r
}

func NewPersistentRegistry(pool *pgxpool.Pool, initialSafeMode ...bool) *Registry {
	r := NewRegistry()
	r.pool = pool
	if len(initialSafeMode) > 0 && initialSafeMode[0] {
		r.safeMode = true
		r.rebuildLocked()
	}
	return r
}

func (r *Registry) Snapshot() RegistrySnapshot {
	if r == nil {
		return RegistrySnapshot{Descriptors: map[string]TypeDescriptor{}}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneRegistrySnapshot(r.snapshot)
}

func (r *Registry) Resolve(typeID string) TypeDescriptor {
	snapshot := r.Snapshot()
	if descriptor, ok := snapshot.Descriptors[typeID]; ok {
		return descriptor
	}
	return TypeDescriptor{
		Type: typeID, ContractVersion: 1, PayloadVersion: 1, Category: "plugin_unknown",
		Label: extensionmanifest.LocalizedText{Default: "Notification"},
		Body:  extensionmanifest.LocalizedText{Default: "This notification is no longer available."},
		Icon:  "i-lucide-bell", TargetKind: "none", Channels: []string{"in_app"}, Active: false,
	}
}

// Publish replaces one plugin's complete exact-artifact declaration set. An
// expected revision provides CAS for concurrent lifecycle publications.
func (r *Registry) Publish(ctx context.Context, owner DescriptorOwner, declarations []extensionmanifest.ManifestNotificationType, expected uint64) (RegistrySnapshot, error) {
	if r == nil || !validPluginOwner(owner) {
		return RegistrySnapshot{}, ErrRegistryDescriptorInvalid
	}
	descriptors, err := pluginDescriptors(owner, declarations)
	if err != nil {
		return RegistrySnapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if expected != r.revision {
		return RegistrySnapshot{}, ErrRegistryRevisionConflict
	}
	for typeID := range descriptors {
		if _, core := r.core[typeID]; core {
			return RegistrySnapshot{}, ErrRegistryOwnerConflict
		}
		for extensionID, owned := range r.plugins {
			if extensionID != owner.ExtensionID {
				if _, conflict := owned[typeID]; conflict {
					return RegistrySnapshot{}, ErrRegistryOwnerConflict
				}
			}
		}
	}
	if err := r.persistPublish(ctx, owner, descriptors); err != nil {
		return RegistrySnapshot{}, err
	}
	r.plugins[owner.ExtensionID] = descriptors
	r.revision++
	r.rebuildLocked()
	return cloneRegistrySnapshot(r.snapshot), nil
}

func (r *Registry) Deactivate(ctx context.Context, owner DescriptorOwner, expected uint64) (RegistrySnapshot, error) {
	if r == nil || !validPluginOwner(owner) {
		return RegistrySnapshot{}, ErrRegistryDescriptorInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if expected != r.revision {
		return RegistrySnapshot{}, ErrRegistryRevisionConflict
	}
	current, ok := r.plugins[owner.ExtensionID]
	if !ok {
		return cloneRegistrySnapshot(r.snapshot), nil
	}
	for _, descriptor := range current {
		if descriptor.Owner != owner {
			return RegistrySnapshot{}, ErrRegistryOwnerConflict
		}
	}
	if err := r.persistDeactivate(ctx, owner); err != nil {
		return RegistrySnapshot{}, err
	}
	delete(r.plugins, owner.ExtensionID)
	r.revision++
	r.rebuildLocked()
	return cloneRegistrySnapshot(r.snapshot), nil
}

func (r *Registry) SetSafeMode(ctx context.Context, enabled bool) (RegistrySnapshot, error) {
	if r == nil || ctx == nil {
		return RegistrySnapshot{}, ErrRegistryDescriptorInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.safeMode != enabled {
		owners := make(map[string]DescriptorOwner, len(r.plugins))
		for extensionID, descriptors := range r.plugins {
			for _, descriptor := range descriptors {
				owners[extensionID] = descriptor.Owner
				break
			}
		}
		if err := r.persistRestore(ctx, owners, r.plugins, enabled); err != nil {
			return RegistrySnapshot{}, err
		}
		r.safeMode = enabled
		r.revision++
		r.rebuildLocked()
	}
	return cloneRegistrySnapshot(r.snapshot), nil
}

// Restore replaces every plugin publication from the enabled exact-artifact
// inventory. It prevents stale disabled or uninstalled owners from surviving
// process restart and publishes the complete graph only after validation.
func (r *Registry) Restore(ctx context.Context, publications []RegistryPublication, safeMode bool) (RegistrySnapshot, error) {
	if r == nil || ctx == nil {
		return RegistrySnapshot{}, ErrRegistryDescriptorInvalid
	}
	plugins := make(map[string]map[string]TypeDescriptor, len(publications))
	owners := make(map[string]DescriptorOwner, len(publications))
	typeOwners := make(map[string]string)
	for _, publication := range publications {
		if !validPluginOwner(publication.Owner) {
			return RegistrySnapshot{}, ErrRegistryDescriptorInvalid
		}
		if _, duplicate := plugins[publication.Owner.ExtensionID]; duplicate {
			return RegistrySnapshot{}, ErrRegistryOwnerConflict
		}
		descriptors, err := pluginDescriptors(publication.Owner, publication.Declarations)
		if err != nil {
			return RegistrySnapshot{}, err
		}
		for typeID := range descriptors {
			if _, core := r.core[typeID]; core {
				return RegistrySnapshot{}, ErrRegistryOwnerConflict
			}
			if extensionID, conflict := typeOwners[typeID]; conflict && extensionID != publication.Owner.ExtensionID {
				return RegistrySnapshot{}, ErrRegistryOwnerConflict
			}
			typeOwners[typeID] = publication.Owner.ExtensionID
		}
		plugins[publication.Owner.ExtensionID] = descriptors
		owners[publication.Owner.ExtensionID] = publication.Owner
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.persistRestore(ctx, owners, plugins, safeMode); err != nil {
		return RegistrySnapshot{}, err
	}
	r.plugins = plugins
	r.safeMode = safeMode
	r.revision++
	r.rebuildLocked()
	return cloneRegistrySnapshot(r.snapshot), nil
}

func (r *Registry) ValidatePublish(owner DescriptorOwner, declarations []extensionmanifest.ManifestNotificationType) error {
	if r == nil || !validPluginOwner(owner) {
		return ErrRegistryDescriptorInvalid
	}
	descriptors, err := pluginDescriptors(owner, declarations)
	if err != nil {
		return err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for typeID := range descriptors {
		if _, core := r.core[typeID]; core {
			return ErrRegistryOwnerConflict
		}
		for extensionID, owned := range r.plugins {
			if extensionID != owner.ExtensionID {
				if _, conflict := owned[typeID]; conflict {
					return ErrRegistryOwnerConflict
				}
			}
		}
	}
	return nil
}

func (r *Registry) persistPublish(ctx context.Context, owner DescriptorOwner, descriptors map[string]TypeDescriptor) error {
	if r.pool == nil {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE notification_type_descriptors SET active=FALSE, updated_at=now() WHERE owner_extension_id=$1`, owner.ExtensionID); err != nil {
		return err
	}
	if err := persistPluginDescriptors(ctx, tx, owner, descriptors, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Registry) persistRestore(
	ctx context.Context,
	owners map[string]DescriptorOwner,
	plugins map[string]map[string]TypeDescriptor,
	safeMode bool,
) error {
	if r.pool == nil {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE notification_type_descriptors SET active=FALSE, updated_at=now() WHERE owner_extension_id<>'' AND active=TRUE`); err != nil {
		return err
	}
	if !safeMode {
		for _, extensionID := range slices.Sorted(maps.Keys(plugins)) {
			descriptors := plugins[extensionID]
			if err := persistPluginDescriptors(ctx, tx, owners[extensionID], descriptors, true); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

type registryTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func persistPluginDescriptors(
	ctx context.Context,
	tx registryTx,
	owner DescriptorOwner,
	descriptors map[string]TypeDescriptor,
	active bool,
) error {
	for _, typeID := range slices.Sorted(maps.Keys(descriptors)) {
		descriptor := descriptors[typeID]
		var existingOwner string
		err := tx.QueryRow(ctx, `SELECT owner_extension_id FROM notification_type_descriptors WHERE type=$1 FOR UPDATE`, descriptor.Type).Scan(&existingOwner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && existingOwner != owner.ExtensionID {
			return ErrRegistryOwnerConflict
		}
		presentation, _ := json.Marshal(map[string]any{
			"label": descriptor.Label, "body": descriptor.Body, "icon": descriptor.Icon,
			"channels": descriptor.Channels, "recommendedChannels": descriptor.RecommendedChannels,
		})
		target, _ := json.Marshal(map[string]any{"kind": descriptor.TargetKind, "id": descriptor.TargetID})
		payloadSchema, _ := json.Marshal(map[string]string{"contract": descriptor.PayloadSchema})
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_type_descriptors (
			  type,contract_version,payload_version,owner_extension_id,owner_artifact_digest,
			  category,payload_schema,presentation,target_contract,active,required
			) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9::jsonb,$10,FALSE)
			ON CONFLICT (type) DO UPDATE SET
			  contract_version=EXCLUDED.contract_version,payload_version=EXCLUDED.payload_version,
			  owner_artifact_digest=EXCLUDED.owner_artifact_digest,category=EXCLUDED.category,
			  payload_schema=EXCLUDED.payload_schema,presentation=EXCLUDED.presentation,
			  target_contract=EXCLUDED.target_contract,active=EXCLUDED.active,updated_at=now()
			WHERE notification_type_descriptors.owner_extension_id=EXCLUDED.owner_extension_id`,
			descriptor.Type, descriptor.ContractVersion, descriptor.PayloadVersion, owner.ExtensionID,
			owner.ArtifactDigest, descriptor.Category, payloadSchema, presentation, target, active); err != nil {
			return fmt.Errorf("persist notification descriptor: %w", err)
		}
		for _, channel := range []string{"in_app", "email", "web_push"} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO notification_type_policies (type,channel,enabled,recommended_enabled,user_configurable,required)
				VALUES ($1,$2,FALSE,FALSE,TRUE,FALSE) ON CONFLICT (type,channel) DO NOTHING`, descriptor.Type, channel); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE notification_type_policies SET enabled=FALSE,recommended_enabled=FALSE,updated_at=now()
			WHERE type=$1 AND NOT (channel=ANY($2))`, descriptor.Type, descriptor.Channels); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) persistDeactivate(ctx context.Context, owner DescriptorOwner) error {
	if r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_type_descriptors SET active=FALSE, updated_at=now()
		WHERE owner_extension_id=$1 AND owner_artifact_digest=$2`, owner.ExtensionID, owner.ArtifactDigest)
	return err
}

func (r *Registry) rebuildLocked() {
	descriptors := make(map[string]TypeDescriptor, len(r.core))
	for typeID, descriptor := range r.core {
		descriptors[typeID] = cloneTypeDescriptor(descriptor)
	}
	if !r.safeMode {
		for _, owned := range r.plugins {
			for typeID, descriptor := range owned {
				descriptors[typeID] = cloneTypeDescriptor(descriptor)
			}
		}
	}
	r.snapshot = RegistrySnapshot{Revision: r.revision, SafeMode: r.safeMode, Descriptors: descriptors}
}

func pluginDescriptors(owner DescriptorOwner, declarations []extensionmanifest.ManifestNotificationType) (map[string]TypeDescriptor, error) {
	result := make(map[string]TypeDescriptor, len(declarations))
	for _, declaration := range declarations {
		version, ok := strings.CutPrefix(declaration.ContractVersion, declaration.ID+"@")
		if !ok || version != "1" || !strings.HasPrefix(declaration.ID, owner.ExtensionID+".") || declaration.Required ||
			!notificationTypeIDPattern.MatchString(declaration.ID) || declaration.PayloadVersion <= 0 {
			return nil, ErrRegistryDescriptorInvalid
		}
		if _, duplicate := result[declaration.ID]; duplicate {
			return nil, ErrRegistryDescriptorInvalid
		}
		result[declaration.ID] = TypeDescriptor{
			Type: declaration.ID, ContractVersion: 1, PayloadVersion: declaration.PayloadVersion,
			Owner: owner, Category: declaration.Category, PayloadSchema: declaration.PayloadSchema,
			Label: declaration.Label, Body: declaration.Body, Icon: declaration.Icon,
			TargetKind: declaration.TargetKind, TargetID: declaration.TargetID,
			Channels: slices.Clone(declaration.Channels), RecommendedChannels: slices.Clone(declaration.RecommendedChannels),
			Required: false, Active: true,
		}
	}
	return result, nil
}

func validPluginOwner(owner DescriptorOwner) bool {
	return owner.ExtensionID != "" && owner.ExtensionID == strings.TrimSpace(owner.ExtensionID) &&
		owner.Version != "" && owner.Version == strings.TrimSpace(owner.Version) &&
		notificationArtifactDigestPattern.MatchString(owner.ArtifactDigest)
}

func coreTypeDescriptors() map[string]TypeDescriptor {
	values := []struct{ id, category, icon string }{
		{TypeReply, "conversation", "i-lucide-message-circle"},
		{TypeMention, "mention", "i-lucide-at-sign"},
		{TypeModerationPending, "moderation", "i-lucide-shield-alert"},
		{TypeModerationApproved, "moderation", "i-lucide-circle-check"},
		{TypeModerationRejected, "moderation", "i-lucide-circle-x"},
		{TypeAdminTest, "system", "i-lucide-bell"},
	}
	result := make(map[string]TypeDescriptor, len(values))
	for _, value := range values {
		result[value.id] = TypeDescriptor{
			Type: value.id, ContractVersion: 1, PayloadVersion: 1, Category: value.category,
			Label: extensionmanifest.LocalizedText{Default: value.id}, Body: extensionmanifest.LocalizedText{Default: "Notification"},
			Icon: value.icon, TargetKind: "entity", TargetID: "core.entity.notification_target",
			Channels: []string{"in_app", "email", "web_push"}, Active: true,
		}
	}
	return result
}

func cloneRegistrySnapshot(snapshot RegistrySnapshot) RegistrySnapshot {
	descriptors := make(map[string]TypeDescriptor, len(snapshot.Descriptors))
	for typeID, descriptor := range snapshot.Descriptors {
		descriptors[typeID] = cloneTypeDescriptor(descriptor)
	}
	return RegistrySnapshot{Revision: snapshot.Revision, SafeMode: snapshot.SafeMode, Descriptors: descriptors}
}

func cloneTypeDescriptor(value TypeDescriptor) TypeDescriptor {
	value.Channels = slices.Clone(value.Channels)
	value.RecommendedChannels = slices.Clone(value.RecommendedChannels)
	value.Label.ByLocale = maps.Clone(value.Label.ByLocale)
	value.Body.ByLocale = maps.Clone(value.Body.ByLocale)
	return value
}
