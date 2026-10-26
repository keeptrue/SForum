package attachments

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

const storageInstanceSecretPurpose = "attachment.storage.instance"

func (s *Service) ListStorageInstances(ctx context.Context, actor identity.Actor, locale string) ([]StorageInstance, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return nil, identity.ErrPermissionDenied
	}
	if s.instanceStore == nil {
		return []StorageInstance{}, nil
	}
	items, err := s.instanceStore.ListStorageInstances(ctx)
	if err != nil {
		return nil, err
	}
	current := ""
	if values, loadErr := s.options.InternalValues(ctx); loadErr == nil {
		current = values[options.NameAttachmentProvider]
	}
	for i := range items {
		items[i].Active = current == storage.FormatInstanceSelection(items[i].ID)
		items[i], err = s.decorateStorageInstance(ctx, items[i], locale)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Service) CreateStorageInstance(ctx context.Context, actor identity.Actor, input StorageInstanceInput, locale string) (StorageInstance, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return StorageInstance{}, identity.ErrPermissionDenied
	}
	if s.instanceStore == nil || s.providers == nil || s.secrets == nil {
		return StorageInstance{}, ErrStorageUnavailable
	}
	input.ExtensionID, input.Name = strings.TrimSpace(input.ExtensionID), strings.TrimSpace(input.Name)
	if input.ExtensionID == "" || input.Name == "" || len(input.Name) > 120 {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	ok, err := s.providers.IsStorageProviderAvailable(ctx, input.ExtensionID)
	if err != nil || !ok {
		if err != nil {
			return StorageInstance{}, err
		}
		return StorageInstance{}, ErrStorageUnavailable
	}
	multiInstance, err := s.isMultiInstanceStorageProvider(ctx, input.ExtensionID)
	if err != nil {
		return StorageInstance{}, err
	}
	if !multiInstance {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	schema, err := s.providers.StorageProviderSchema(ctx, input.ExtensionID, locale)
	if err != nil {
		return StorageInstance{}, err
	}
	id := uuid.NewString()
	plain, err := s.persistStorageInstanceValues(ctx, actor, id, 1, schema, nil, input.Values)
	if err != nil {
		return StorageInstance{}, err
	}
	item, err := s.instanceStore.CreateStorageInstance(ctx, StorageInstanceCreate{ID: id, ExtensionID: input.ExtensionID, Name: input.Name, Settings: plain, CreatedByUserID: actor.ID})
	if err != nil {
		return StorageInstance{}, err
	}
	return s.decorateStorageInstance(ctx, item, locale)
}

func (s *Service) UpdateStorageInstance(ctx context.Context, actor identity.Actor, id string, input StorageInstanceInput, locale string) (StorageInstance, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return StorageInstance{}, identity.ErrPermissionDenied
	}
	if s.instanceStore == nil || s.providers == nil || s.secrets == nil {
		return StorageInstance{}, ErrStorageUnavailable
	}
	item, err := s.instanceStore.GetStorageInstance(ctx, strings.TrimSpace(id))
	if err != nil {
		return StorageInstance{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 120 || input.ConfigRevision <= 0 {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	schema, err := s.providers.StorageProviderSchema(ctx, item.ExtensionID, locale)
	if err != nil {
		return StorageInstance{}, err
	}
	plain, err := s.persistStorageInstanceValues(ctx, actor, item.ID, item.ConfigRevision+1, schema, item.Values, input.Values)
	if err != nil {
		return StorageInstance{}, err
	}
	updated, err := s.instanceStore.UpdateStorageInstance(ctx, item.ID, input.ConfigRevision, name, plain)
	if err != nil {
		return StorageInstance{}, err
	}
	if s.storageRuntime != nil {
		_ = s.storageRuntime.RemoveStorageInstance(ctx, item.ExtensionID, item.ID)
	}
	return s.decorateStorageInstance(ctx, updated, locale)
}

func (s *Service) ProbeStorageInstance(ctx context.Context, actor identity.Actor, input StorageInstanceProbeInput, locale string) (ProbeResult, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return ProbeResult{}, identity.ErrPermissionDenied
	}
	if s.instanceStore == nil || s.providers == nil || s.storageRuntime == nil || s.secrets == nil {
		return ProbeResult{}, ErrStorageUnavailable
	}
	extensionID := strings.TrimSpace(input.ExtensionID)
	var existing *StorageInstance
	if id := strings.TrimSpace(input.InstanceID); id != "" {
		item, err := s.instanceStore.GetStorageInstance(ctx, id)
		if err != nil {
			return ProbeResult{}, err
		}
		existing, extensionID = &item, item.ExtensionID
	}
	multiInstance, err := s.isMultiInstanceStorageProvider(ctx, extensionID)
	if err != nil {
		return ProbeResult{}, err
	}
	if !multiInstance {
		return ProbeResult{}, ErrStorageInstanceInvalid
	}
	schema, err := s.providers.StorageProviderSchema(ctx, extensionID, locale)
	if err != nil {
		return ProbeResult{}, err
	}
	base := map[string]string(nil)
	if existing != nil {
		base = existing.Values
	}
	values, err := s.mergeStorageInstanceRuntimeValues(ctx, actor, schema, existing, base, input.Values)
	if err != nil {
		return ProbeResult{}, err
	}
	err = s.storageRuntime.ProbeStorageInstance(ctx, extensionID, values)
	result := ProbeResult{Provider: storage.FormatInstanceSelection(input.InstanceID), OK: err == nil, Reason: "storage.ok", Message: "ok"}
	if err != nil {
		result.Reason, result.Message = probeFailureReason(err), probeFailureMessage(err)
	}
	if existing != nil {
		_ = s.instanceStore.UpdateStorageInstanceProbe(ctx, existing.ID, map[bool]string{true: "ok", false: "error"}[result.OK], result.Message)
	}
	result.Message = localizedProbeMessage(locale, result.Reason)
	return result, nil
}

func (s *Service) ActivateStorageInstance(ctx context.Context, actor identity.Actor, id, locale string) (StorageInstance, error) {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return StorageInstance{}, identity.ErrPermissionDenied
	}
	if s.instanceStore == nil || s.providers == nil || s.storageRuntime == nil || s.secrets == nil {
		return StorageInstance{}, ErrStorageUnavailable
	}
	item, err := s.instanceStore.GetStorageInstance(ctx, strings.TrimSpace(id))
	if err != nil {
		return StorageInstance{}, err
	}
	multiInstance, err := s.isMultiInstanceStorageProvider(ctx, item.ExtensionID)
	if err != nil {
		return StorageInstance{}, err
	}
	if !multiInstance {
		return StorageInstance{}, ErrStorageInstanceInvalid
	}
	values, err := s.storageInstanceRuntimeValues(ctx, item, "user:"+strconv.FormatInt(actor.ID, 10))
	if err != nil {
		return StorageInstance{}, err
	}
	if err := s.storageRuntime.ProbeStorageInstance(ctx, item.ExtensionID, values); err != nil {
		_ = s.instanceStore.UpdateStorageInstanceProbe(ctx, item.ID, "error", probeFailureMessage(err))
		return StorageInstance{}, errors.Join(ErrStorageUnavailable, err)
	}
	_ = s.instanceStore.UpdateStorageInstanceProbe(ctx, item.ID, "ok", "ok")
	if _, err := s.options.UpdateMany(ctx, actor, []options.UpdateInput{{Name: options.NameAttachmentProvider, Value: storage.FormatInstanceSelection(item.ID)}}); err != nil {
		return StorageInstance{}, err
	}
	item, err = s.instanceStore.GetStorageInstance(ctx, item.ID)
	if err != nil {
		return StorageInstance{}, err
	}
	item.Active = true
	return s.decorateStorageInstance(ctx, item, locale)
}

func (s *Service) RestoreLocalStorage(ctx context.Context, actor identity.Actor) error {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return identity.ErrPermissionDenied
	}
	_, err := s.options.UpdateMany(ctx, actor, []options.UpdateInput{{Name: options.NameAttachmentProvider, Value: storage.ProviderLocal}})
	return err
}

func (s *Service) DeleteStorageInstance(ctx context.Context, actor identity.Actor, id string) error {
	if !actor.Can(identity.PermissionAttachmentSettings) {
		return identity.ErrPermissionDenied
	}
	item, err := s.instanceStore.GetStorageInstance(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if item.AttachmentCount > 0 {
		return ErrStorageInstanceReferenced
	}
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return err
	}
	if values[options.NameAttachmentProvider] == storage.FormatInstanceSelection(item.ID) {
		return ErrStorageInstanceReferenced
	}
	if err := s.instanceStore.DeleteStorageInstance(ctx, item.ID); err != nil {
		return err
	}
	if s.storageRuntime != nil {
		_ = s.storageRuntime.RemoveStorageInstance(ctx, item.ExtensionID, item.ID)
	}
	return nil
}

func (s *Service) persistStorageInstanceValues(ctx context.Context, actor identity.Actor, id string, revision int64, schema storage.ProviderSchema, current, submitted map[string]string) (map[string]string, error) {
	known := map[string]storage.ProviderField{}
	for _, field := range schema.Fields {
		known[field.Key] = field
	}
	for key := range submitted {
		if _, ok := known[key]; !ok {
			return nil, ErrStorageInstanceInvalid
		}
	}
	out := map[string]string{}
	for _, field := range schema.Fields {
		value, supplied := submitted[field.Key]
		if field.Type == "secret" {
			if !supplied || secretstore.ShouldPreserve(value) {
				if current != nil {
					out[field.Key] = current[field.Key]
				}
				continue
			}
			meta, err := s.secrets.Put(ctx, storageInstanceSecretRef(schema.ExtensionID, id, revision, field.Key), []byte(value), secretstore.PutOptions{Actor: "user:" + strconv.FormatInt(actor.ID, 10), Purposes: []string{storageInstanceSecretPurpose}})
			if err != nil {
				return nil, err
			}
			out[field.Key] = meta.Reference
			continue
		}
		if !supplied {
			value = current[field.Key]
			if value == "" {
				value = field.Default
			}
		}
		value = strings.TrimSpace(value)
		if err := validateStorageField(field, value); err != nil {
			return nil, err
		}
		out[field.Key] = value
	}
	return out, nil
}

func (s *Service) storageInstanceRuntimeValues(ctx context.Context, item StorageInstance, actor string) (map[string]string, error) {
	out := make(map[string]string, len(item.Values))
	for key, value := range item.Values {
		ref, err := secretstore.ParseReference(value)
		if err != nil {
			out[key] = value
			continue
		}
		lease, err := s.secrets.Resolve(ctx, secretstore.Caller{Actor: actor}, ref, storageInstanceSecretPurpose, 30*time.Second)
		if err != nil {
			return nil, err
		}
		out[key] = string(lease.Value)
	}
	return out, nil
}

func (s *Service) mergeStorageInstanceRuntimeValues(ctx context.Context, actor identity.Actor, schema storage.ProviderSchema, existing *StorageInstance, base, submitted map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, field := range schema.Fields {
		value, supplied := submitted[field.Key]
		if field.Type != "secret" {
			if !supplied {
				value = base[field.Key]
				if value == "" {
					value = field.Default
				}
			}
			if err := validateStorageField(field, value); err != nil {
				return nil, err
			}
			out[field.Key] = strings.TrimSpace(value)
			continue
		}
		if supplied && !secretstore.ShouldPreserve(value) {
			out[field.Key] = value
			continue
		}
		if existing == nil {
			continue
		}
		ref, parseErr := secretstore.ParseReference(existing.Values[field.Key])
		if parseErr != nil {
			continue
		}
		lease, err := s.secrets.Resolve(ctx, secretstore.Caller{Actor: "host"}, ref, storageInstanceSecretPurpose, 30*time.Second)
		if err != nil {
			if errors.Is(err, secretstore.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out[field.Key] = string(lease.Value)
	}
	return out, nil
}

func (s *Service) decorateStorageInstance(ctx context.Context, item StorageInstance, locale string) (StorageInstance, error) {
	schema, err := s.providers.StorageProviderSchema(ctx, item.ExtensionID, locale)
	if err != nil {
		return StorageInstance{}, err
	}
	for i := range schema.Fields {
		if schema.Fields[i].Type != "secret" {
			continue
		}
		ref, parseErr := secretstore.ParseReference(item.Values[schema.Fields[i].Key])
		_, metaErr := s.secrets.Meta(ctx, ref)
		if parseErr != nil {
			metaErr = parseErr
		}
		schema.Fields[i].SecretSet = metaErr == nil
		item.Values[schema.Fields[i].Key] = ""
	}
	item.Schema = schema
	return item, nil
}

func storageInstanceSecretRef(extensionID, instanceID string, revision int64, key string) secretstore.Ref {
	return secretstore.Ref{Namespace: strings.ToLower(strings.TrimSpace(extensionID)), SecretID: strings.ToLower("storage-instance." + instanceID + ".r" + strconv.FormatInt(revision, 10) + "." + key)}
}

func (s *Service) isMultiInstanceStorageProvider(ctx context.Context, extensionID string) (bool, error) {
	candidates, err := s.providers.ListStorageProviderCandidates(ctx)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		if candidate.ExtensionID == extensionID {
			return candidate.Available && candidate.MultiInstance, nil
		}
	}
	return false, nil
}

func validateStorageField(field storage.ProviderField, value string) error {
	value = strings.TrimSpace(value)
	if field.Required && value == "" {
		return fmt.Errorf("%w: field %s is required", ErrStorageInstanceInvalid, field.Key)
	}
	if field.Type == "boolean" && value != "true" && value != "false" {
		return ErrStorageInstanceInvalid
	}
	if len(field.Options) > 0 {
		for _, option := range field.Options {
			if value == option.Value {
				return nil
			}
		}
		return ErrStorageInstanceInvalid
	}
	if len(value) > 4096 {
		return fmt.Errorf("%w: field %s is too long", ErrStorageInstanceInvalid, field.Key)
	}
	return nil
}
