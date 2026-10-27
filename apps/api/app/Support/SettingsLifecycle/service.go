package settingslifecycle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// Service is the Host settings lifecycle runtime.
// Document authority is DocumentStore (extension_settings in production).
// Schema/migration registries remain process-local (declared at enable).
type Service struct {
	mu           sync.Mutex
	schemas      map[string][]FieldSchema
	migrations   map[string][]Migration
	targetVer    map[string]int
	docs         DocumentStore
	secrets      *secretstore.Service
	legacyCipher *cryptox.OptionCipher
}

// New builds a settings lifecycle service with an in-memory document store
// (tests only). Prefer NewWithStore for production.
func New(secrets *secretstore.Service) *Service {
	return NewWithStore(NewMemoryDocumentStore(), secrets)
}

// NewWithStore builds a lifecycle service with durable document authority.
func NewWithStore(docs DocumentStore, secrets *secretstore.Service) *Service {
	if docs == nil {
		docs = NewMemoryDocumentStore()
	}
	return &Service{
		schemas:    make(map[string][]FieldSchema),
		migrations: make(map[string][]Migration),
		targetVer:  make(map[string]int),
		docs:       docs,
		secrets:    secrets,
	}
}

// WithLegacyCipher enables one-time migration of historical enc:: settings
// into SecretStore. New secret writes always use SecretStore directly.
func (s *Service) WithLegacyCipher(cipher *cryptox.OptionCipher) *Service {
	if s != nil {
		s.legacyCipher = cipher
	}
	return s
}

// RegisterSchema installs field schema and target data version for an extension.
func (s *Service) RegisterSchema(extensionID string, dataVersion int, fields []FieldSchema) error {
	if s == nil {
		return ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" || dataVersion < 1 || len(fields) == 0 {
		return ErrInvalid
	}
	cloned := make([]FieldSchema, len(fields))
	copy(cloned, fields)
	s.mu.Lock()
	s.schemas[extensionID] = cloned
	s.targetVer[extensionID] = dataVersion
	s.mu.Unlock()
	return nil
}

// RegisterMigration adds a data migration step.
func (s *Service) RegisterMigration(extensionID string, migration Migration) error {
	if s == nil || migration.Apply == nil || migration.From < 0 || migration.To <= migration.From {
		return ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	if extensionID == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	s.migrations[extensionID] = append(s.migrations[extensionID], migration)
	s.mu.Unlock()
	return nil
}

// Put replaces values (non-secret) and optional secret plaintexts (via Secret Store).
// Empty secret submissions preserve existing secrets when preserveSecrets is true.
// Uses request ctx for all persistence and secret operations.
func (s *Service) Put(ctx context.Context, extensionID, actor string, values map[string]string, preserveSecrets bool) (Document, error) {
	doc, _, err := s.put(ctx, extensionID, actor, values, preserveSecrets, "")
	return doc, err
}

// HasSecretField reports whether the extension's registered schema declares key as a
// secret field. Runtime writes must pass this gate: an undeclared key has no document
// slot, so accepting it would store plaintext nothing could ever read.
func (s *Service) HasSecretField(extensionID, key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, field := range s.schemas[normalizeID(extensionID)] {
		if field.Name == key && (field.Secret || field.Type == "secret") {
			return true
		}
	}
	return false
}

// PutSecret writes exactly one declared secret field and returns the new document
// revision. An empty value clears the field (other secrets are preserved either way).
// This is the write half the Host API exposes as SecretService.Put/Clear; admin saves
// keep using Put so the two paths cannot disagree about CAS or migration.
func (s *Service) PutSecret(ctx context.Context, extensionID, actor, key, value, mediaType string) (int64, error) {
	key = strings.TrimSpace(key)
	if !s.HasSecretField(extensionID, key) {
		return 0, ErrValidation
	}
	// Present-and-empty means "clear"; anything else means "set". preserveSecrets mirrors
	// that split so every other secret field in the document survives untouched.
	clear := secretstore.ShouldPreserve(value)
	_, revision, err := s.put(ctx, extensionID, actor, map[string]string{key: value}, !clear, mediaType)
	return revision, err
}

func (s *Service) put(
	ctx context.Context,
	extensionID, actor string,
	values map[string]string,
	preserveSecrets bool,
	secretMediaType string,
) (Document, int64, error) {
	if s == nil {
		return Document{}, 0, ErrInvalid
	}
	if ctx == nil {
		return Document{}, 0, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	actor = strings.TrimSpace(actor)
	if extensionID == "" || actor == "" {
		return Document{}, 0, ErrPermissionDenied
	}
	s.mu.Lock()
	fields := s.schemas[extensionID]
	target := s.targetVer[extensionID]
	s.mu.Unlock()
	if len(fields) == 0 || target < 1 {
		return Document{}, 0, ErrNotFound
	}
	if values == nil {
		values = map[string]string{}
	}

	current, rev, err := s.loadDocument(ctx, extensionID, actor)
	hasCurrent := err == nil
	if err != nil && err != ErrNotFound {
		return Document{}, 0, err
	}

	doc := Document{
		SchemaVersion: SchemaVersion, ExtensionID: extensionID, DataVersion: target,
		Values: make(map[string]string), SecretRefs: map[string]string{}, SecretSet: map[string]bool{},
		UpdatedAt: time.Now().UTC(), UpdatedBy: actor,
	}
	if hasCurrent {
		for k, v := range current.Values {
			doc.Values[k] = v
		}
		for k, v := range current.SecretRefs {
			doc.SecretRefs[k] = v
		}
		for k, v := range current.SecretSet {
			doc.SecretSet[k] = v
		}
		doc.DataVersion = current.DataVersion
	}
	for _, field := range fields {
		raw, present := values[field.Name]
		if field.Secret || field.Type == "secret" {
			if !present || secretstore.ShouldPreserve(raw) {
				if preserveSecrets {
					continue
				}
				// Explicit clear only when preserveSecrets is false and empty submitted.
				if present && secretstore.ShouldPreserve(raw) && !preserveSecrets {
					delete(doc.SecretRefs, field.Name)
					doc.SecretSet[field.Name] = false
				}
				continue
			}
			if s.secrets == nil {
				return Document{}, 0, ErrInvalid
			}
			ref := secretstore.Ref{Namespace: extensionID, SecretID: field.Name}
			meta, putErr := s.secrets.Put(ctx, ref, []byte(raw), secretstore.PutOptions{
				Actor: actor, Purposes: []string{"settings"}, MediaType: secretMediaType,
			})
			if putErr != nil {
				return Document{}, 0, putErr
			}
			doc.SecretRefs[field.Name] = meta.Reference
			doc.SecretSet[field.Name] = true
			continue
		}
		if present {
			doc.Values[field.Name] = raw
		}
	}

	// 迁移在内存副本上执行；失败则不写入 DocumentStore（回滚）。
	migrated, err := s.migrate(extensionID, doc)
	if err != nil {
		return Document{}, 0, err
	}
	expected := rev
	if !hasCurrent {
		expected = 0
	}
	saved, err := s.docs.Save(ctx, extensionID, migrated, expected)
	if err != nil {
		// CAS 冲突时重试一次读-合并不合适（调用方应重试）；直接返回。
		return Document{}, 0, err
	}
	return cloneDocument(migrated), saved, nil
}

// Get returns the current document (secrets masked).
func (s *Service) Get(ctx context.Context, extensionID string) (Document, error) {
	if s == nil {
		return Document{}, ErrInvalid
	}
	if ctx == nil {
		return Document{}, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	doc, _, err := s.loadDocument(ctx, extensionID, "actor:settings-migration")
	if err != nil {
		return Document{}, err
	}
	return cloneDocument(doc), nil
}

// RuntimeValues resolves a settings document into plaintext values for the
// owning plugin runtime. Secret plaintext is read through SecretStore only for
// the extension namespace that owns the document.
func (s *Service) RuntimeValues(ctx context.Context, extensionID string, purpose string) (map[string]string, error) {
	if s == nil {
		return nil, ErrInvalid
	}
	if ctx == nil {
		return nil, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	purpose = strings.TrimSpace(purpose)
	if extensionID == "" || purpose == "" {
		return nil, ErrInvalid
	}
	doc, _, err := s.loadDocument(ctx, extensionID, "actor:runtime-migration")
	if err != nil {
		return nil, err
	}
	values := cloneMap(doc.Values)
	for key, refValue := range doc.SecretRefs {
		if strings.TrimSpace(refValue) == "" {
			continue
		}
		if s.secrets == nil {
			return nil, ErrInvalid
		}
		ref, err := secretstore.ParseReference(refValue)
		if err != nil {
			return nil, err
		}
		lease, err := s.secrets.Resolve(ctx, secretstore.Caller{ExtensionID: extensionID}, ref, purpose, 0)
		if err != nil {
			return nil, err
		}
		values[key] = string(lease.Value)
	}
	return values, nil
}

// ResetOptions controls ResetDefaults secret policy.
// PreserveSecrets must be set explicitly for beginner-friendly admin UX:
// true keeps Secret Store refs; false clears secret refs (values remain in Secret Store history).
type ResetOptions struct {
	// PreserveSecrets when true keeps existing secret references after reset.
	// Beginner-friendly default path should pass true and state this in UI.
	PreserveSecrets bool
}

// ResetDefaults restores field defaults.
// opts.PreserveSecrets selects whether secret refs are kept (recommended default true).
func (s *Service) ResetDefaults(ctx context.Context, extensionID, actor string, opts ResetOptions) (Document, error) {
	if s == nil {
		return Document{}, ErrInvalid
	}
	if ctx == nil {
		return Document{}, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	actor = strings.TrimSpace(actor)
	if extensionID == "" || actor == "" {
		return Document{}, ErrPermissionDenied
	}
	s.mu.Lock()
	fields := append([]FieldSchema(nil), s.schemas[extensionID]...)
	target := s.targetVer[extensionID]
	s.mu.Unlock()
	if len(fields) == 0 {
		return Document{}, ErrNotFound
	}

	current, rev, err := s.loadDocument(ctx, extensionID, actor)
	hasCurrent := err == nil
	if err != nil && err != ErrNotFound {
		return Document{}, err
	}

	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if field.Secret || field.Type == "secret" {
			continue
		}
		values[field.Name] = field.Default
	}
	doc := Document{
		SchemaVersion: SchemaVersion, ExtensionID: extensionID, DataVersion: target,
		Values: values, SecretRefs: map[string]string{}, SecretSet: map[string]bool{},
		UpdatedAt: time.Now().UTC(), UpdatedBy: actor,
	}
	if opts.PreserveSecrets && hasCurrent {
		for k, v := range current.SecretRefs {
			doc.SecretRefs[k] = v
		}
		for k, v := range current.SecretSet {
			doc.SecretSet[k] = v
		}
	}
	expected := int64(0)
	if hasCurrent {
		expected = rev
	}
	if _, err := s.docs.Save(ctx, extensionID, doc, expected); err != nil {
		return Document{}, err
	}
	return cloneDocument(doc), nil
}

// Preview validates values and evaluates conditionals without persistence.
func (s *Service) Preview(ctx context.Context, extensionID string, values map[string]string) (ValidationPreview, error) {
	if s == nil {
		return ValidationPreview{}, ErrInvalid
	}
	if ctx == nil {
		return ValidationPreview{}, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	s.mu.Lock()
	fields := append([]FieldSchema(nil), s.schemas[extensionID]...)
	s.mu.Unlock()
	if len(fields) == 0 {
		return ValidationPreview{}, ErrNotFound
	}
	if values == nil {
		values = map[string]string{}
	}
	preview := ValidationPreview{OK: true, Errors: map[string]string{}, Warnings: map[string]string{}}
	for _, field := range fields {
		if !fieldVisible(field, values) {
			continue
		}
		preview.VisibleFields = append(preview.VisibleFields, field.Name)
		raw := values[field.Name]
		if field.Required && strings.TrimSpace(raw) == "" && !field.Secret && field.Type != "secret" {
			preview.OK = false
			preview.Errors[field.Name] = "required"
			continue
		}
		if field.Type == "select" && len(field.Options) > 0 && strings.TrimSpace(raw) != "" {
			if !contains(field.Options, raw) {
				preview.OK = false
				preview.Errors[field.Name] = "invalid_option"
			}
		}
	}
	return preview, nil
}

// Export returns a masked bundle (no secret plaintext).
func (s *Service) Export(ctx context.Context, extensionID string) (ExportBundle, error) {
	doc, err := s.Get(ctx, extensionID)
	if err != nil {
		return ExportBundle{}, err
	}
	return ExportBundle{
		SchemaVersion: SchemaVersion, ExtensionID: doc.ExtensionID, DataVersion: doc.DataVersion,
		Values: cloneMap(doc.Values), SecretRefs: cloneMap(doc.SecretRefs), SecretSet: cloneBoolMap(doc.SecretSet),
		SecretsNeverIncluded: true,
	}, nil
}

// Import applies a bundle. Secret refs are kept; plaintext secrets are rejected.
func (s *Service) Import(ctx context.Context, extensionID, actor string, bundle ExportBundle) (Document, error) {
	if s == nil {
		return Document{}, ErrInvalid
	}
	if ctx == nil {
		return Document{}, ErrInvalid
	}
	extensionID = normalizeID(extensionID)
	actor = strings.TrimSpace(actor)
	if extensionID == "" || actor == "" || bundle.ExtensionID != "" && normalizeID(bundle.ExtensionID) != extensionID {
		return Document{}, ErrInvalid
	}
	// Reject any value that looks like a leaked secret payload.
	for _, value := range bundle.Values {
		if strings.HasPrefix(value, "enc::") {
			return Document{}, fmt.Errorf("%w: ciphertext in values", ErrValidation)
		}
	}
	s.mu.Lock()
	target := s.targetVer[extensionID]
	fields := s.schemas[extensionID]
	s.mu.Unlock()
	if len(fields) == 0 {
		return Document{}, ErrNotFound
	}
	doc := Document{
		SchemaVersion: SchemaVersion, ExtensionID: extensionID,
		DataVersion: bundle.DataVersion, Values: cloneMap(bundle.Values),
		SecretRefs: cloneMap(bundle.SecretRefs), SecretSet: cloneBoolMap(bundle.SecretSet),
		UpdatedAt: time.Now().UTC(), UpdatedBy: actor,
	}
	if doc.DataVersion == 0 {
		doc.DataVersion = 1
	}
	// 迁移失败则不落盘。
	if doc.DataVersion != target {
		migrated, err := s.migrate(extensionID, doc)
		if err != nil {
			return Document{}, err
		}
		doc = migrated
	}
	current, rev, err := s.loadDocument(ctx, extensionID, actor)
	expected := int64(0)
	if err == nil {
		expected = rev
		_ = current
	} else if err != ErrNotFound {
		return Document{}, err
	}
	if _, err := s.docs.Save(ctx, extensionID, doc, expected); err != nil {
		return Document{}, err
	}
	return cloneDocument(doc), nil
}

func (s *Service) migrate(extensionID string, doc Document) (Document, error) {
	s.mu.Lock()
	target := s.targetVer[extensionID]
	steps := append([]Migration(nil), s.migrations[extensionID]...)
	s.mu.Unlock()
	if target < 1 {
		return doc, nil
	}
	// 在克隆上迁移；Apply 失败时原 doc 未写入 store。
	working := cloneDocument(doc)
	guard := 0
	for working.DataVersion < target {
		guard++
		if guard > 64 {
			return Document{}, fmt.Errorf("%w: migration loop", ErrMigration)
		}
		applied := false
		for _, step := range steps {
			if step.From == working.DataVersion && step.To > step.From {
				next, err := step.Apply(cloneMap(working.Values))
				if err != nil {
					return Document{}, fmt.Errorf("%w: %v", ErrMigration, err)
				}
				working.Values = next
				working.DataVersion = step.To
				applied = true
				break
			}
		}
		if !applied {
			return Document{}, fmt.Errorf("%w: missing step from %d to %d", ErrMigration, working.DataVersion, target)
		}
	}
	return working, nil
}

// loadDocument upgrades legacy encrypted values before a caller can read or
// overwrite the document. Any failure leaves the old document untouched.
func (s *Service) loadDocument(ctx context.Context, extensionID, actor string) (Document, int64, error) {
	doc, revision, err := s.docs.Load(ctx, extensionID)
	if err != nil {
		return Document{}, 0, err
	}
	if !hasLegacyCiphertext(doc.Values) {
		return doc, revision, nil
	}
	if s.legacyCipher == nil || !s.legacyCipher.Enabled() {
		return Document{}, 0, fmt.Errorf("%w: legacy encrypted settings require APP_OPTION_ENC_KEY", ErrMigration)
	}
	if s.secrets == nil {
		return Document{}, 0, fmt.Errorf("%w: SecretStore is unavailable for legacy settings", ErrMigration)
	}
	fields := s.secretFields(extensionID)
	working := cloneDocument(doc)
	for key, stored := range doc.Values {
		if !cryptox.IsEncrypted(stored) {
			continue
		}
		if !fields[key] {
			return Document{}, 0, fmt.Errorf("%w: encrypted value %q is not declared as a secret field", ErrMigration, key)
		}
		plain, decryptErr := s.legacyCipher.Decrypt(stored)
		if decryptErr != nil {
			return Document{}, 0, fmt.Errorf("%w: decrypt legacy setting %q: %v", ErrMigration, key, decryptErr)
		}
		if strings.TrimSpace(plain) == "" {
			delete(working.Values, key)
			working.SecretSet[key] = false
			continue
		}
		meta, putErr := s.secrets.Put(ctx, secretstore.Ref{Namespace: extensionID, SecretID: key}, []byte(plain), secretstore.PutOptions{
			Actor: actorOrSystem(actor), Purposes: []string{"settings"},
		})
		if putErr != nil {
			return Document{}, 0, fmt.Errorf("%w: migrate legacy setting %q: %v", ErrMigration, key, putErr)
		}
		working.SecretRefs[key] = meta.Reference
		working.SecretSet[key] = true
		delete(working.Values, key)
	}
	working.UpdatedAt = time.Now().UTC()
	working.UpdatedBy = actorOrSystem(actor)
	newRevision, saveErr := s.docs.Save(ctx, extensionID, working, revision)
	if saveErr != nil {
		return Document{}, 0, fmt.Errorf("%w: persist legacy settings migration: %v", ErrMigration, saveErr)
	}
	return working, newRevision, nil
}

func (s *Service) secretFields(extensionID string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := make(map[string]bool)
	for _, field := range s.schemas[extensionID] {
		if field.Secret || field.Type == "secret" {
			fields[field.Name] = true
		}
	}
	return fields
}

func hasLegacyCiphertext(values map[string]string) bool {
	for _, value := range values {
		if cryptox.IsEncrypted(value) {
			return true
		}
	}
	return false
}

func actorOrSystem(actor string) string {
	if actor = strings.TrimSpace(actor); actor != "" {
		return actor
	}
	return "actor:settings-migration"
}

func fieldVisible(field FieldSchema, values map[string]string) bool {
	cond := strings.TrimSpace(field.VisibleWhen)
	if cond == "" {
		return true
	}
	parts := strings.SplitN(cond, "=", 2)
	if len(parts) != 2 {
		return true
	}
	return values[strings.TrimSpace(parts[0])] == strings.TrimSpace(parts[1])
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func cloneDocument(doc Document) Document {
	out := doc
	out.Values = cloneMap(doc.Values)
	out.SecretRefs = cloneMap(doc.SecretRefs)
	out.SecretSet = cloneBoolMap(doc.SecretSet)
	return out
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
