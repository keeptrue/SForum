package sitechrome

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

const navigationPreviewLifetime = 10 * time.Minute

type navigationPreviewRecord struct {
	actorID  int64
	revision uint64
	mode     string
	document NavigationDocument
	expires  time.Time
}

// navigationPreviewStore intentionally keeps only opaque, actor-bound proposed
// documents. It has no client-supplied descriptions and is bounded to avoid
// turning preview into a second persistent configuration store.
type navigationPreviewStore struct {
	mu      sync.Mutex
	records map[string]navigationPreviewRecord
}

func newNavigationPreviewStore() *navigationPreviewStore {
	return &navigationPreviewStore{records: make(map[string]navigationPreviewRecord)}
}

func (s *navigationPreviewStore) put(actorID int64, revision uint64, mode string, document NavigationDocument) (string, error) {
	if s == nil {
		return "", ErrInvalid
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate navigation preview token: %w", err)
	}
	token := hex.EncodeToString(raw)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, record := range s.records {
		if !record.expires.After(now) {
			delete(s.records, key)
		}
	}
	if len(s.records) >= 64 {
		var oldest string
		var expires time.Time
		for key, record := range s.records {
			if oldest == "" || record.expires.Before(expires) {
				oldest, expires = key, record.expires
			}
		}
		delete(s.records, oldest)
	}
	s.records[token] = navigationPreviewRecord{actorID: actorID, revision: revision, mode: mode, document: document, expires: now.Add(navigationPreviewLifetime)}
	return token, nil
}

func (s *navigationPreviewStore) take(actorID int64, revision uint64, token string) (navigationPreviewRecord, error) {
	if s == nil || token == "" {
		return navigationPreviewRecord{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[token]
	if !ok || !record.expires.After(time.Now().UTC()) || record.actorID != actorID || record.revision != revision {
		delete(s.records, token)
		return navigationPreviewRecord{}, ErrConflict
	}
	delete(s.records, token)
	return record, nil
}

func (s *Service) ReadAdminNavigationDocument(ctx context.Context, actor identity.Actor) (NavigationDocument, error) {
	if !s.canManage(actor) {
		return NavigationDocument{}, identity.ErrPermissionDenied
	}
	if s == nil || s.navigationDocuments == nil {
		return NavigationDocument{}, ErrInvalid
	}
	document, err := s.navigationDocuments.ReadNavigationDocument(ctx)
	if err != nil {
		return NavigationDocument{}, err
	}
	return s.navigationDocumentForAdmin(document), nil
}

func (s *Service) navigationDocumentForAdmin(document NavigationDocument) NavigationDocument {
	if document.Revision < 1 {
		document.Revision = 1
	}
	document.ThemeLocations = make([]NavigationThemeLocation, 0, len(NavigationLocations()))
	for _, location := range NavigationLocations() {
		document.ThemeLocations = append(document.ThemeLocations, NavigationThemeLocation{Location: location, Supported: s.navigationLocationSupported(location)})
	}
	return document
}

func (s *Service) ApplyNavigationDocument(ctx context.Context, actor identity.Actor, input NavigationApplyInput) (NavigationDocument, error) {
	if !s.canManage(actor) {
		return NavigationDocument{}, identity.ErrPermissionDenied
	}
	return s.applyNavigationMutation(ctx, actor, input.ExpectedRevision, input.Reason, audit.ActionSiteNavigationApply, "apply", input.Document, true)
}

func (s *Service) PreviewNavigationDefaults(ctx context.Context, actor identity.Actor, input NavigationDefaultsPreviewInput) (NavigationPreview, error) {
	if !s.canManage(actor) {
		return NavigationPreview{}, identity.ErrPermissionDenied
	}
	if input.ExpectedRevision < 1 || (input.Scope != "location" && input.Scope != "all") ||
		(input.Scope == "location" && !isNavigationLocation(input.Location)) || (input.Scope == "all" && input.Location != "") {
		return NavigationPreview{}, ErrInvalid
	}
	current, err := s.ReadAdminNavigationDocument(ctx, actor)
	if err != nil {
		return NavigationPreview{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return NavigationPreview{}, ErrConflict
	}
	locations := NavigationLocations()
	if input.Scope == "location" {
		locations = []string{input.Location}
	}
	proposed := navigationDefaultsDocument(current, locations)
	proposed, err = normalizeNavigationDocument(proposed)
	if err != nil {
		return NavigationPreview{}, err
	}
	return s.makeNavigationPreview(actor, current, proposed, "defaults", nil, nil)
}

func (s *Service) ApplyNavigationPreview(ctx context.Context, actor identity.Actor, input NavigationPreviewApplyInput) (NavigationDocument, error) {
	if !s.canManage(actor) {
		return NavigationDocument{}, identity.ErrPermissionDenied
	}
	if input.ExpectedRevision < 1 || !validNavigationReason(input.Reason) {
		return NavigationDocument{}, ErrInvalid
	}
	record, err := s.navigationPreviews.take(actor.ID, input.ExpectedRevision, input.PreviewToken)
	if err != nil {
		return NavigationDocument{}, err
	}
	action := audit.ActionSiteNavigationImport
	operation := "import_" + record.mode
	if record.mode == "defaults" {
		action, operation = audit.ActionSiteNavigationDefaultsRestore, "defaults_restore"
	}
	return s.applyNavigationMutation(ctx, actor, input.ExpectedRevision, input.Reason, action, operation, record.document, false)
}

func (s *Service) ListNavigationSnapshots(ctx context.Context, actor identity.Actor) ([]NavigationSnapshot, error) {
	if !s.canManage(actor) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.navigationCommands == nil {
		return nil, ErrInvalid
	}
	return s.navigationCommands.ListNavigationSnapshots(ctx)
}

func (s *Service) GetNavigationSnapshot(ctx context.Context, actor identity.Actor, id int64) (NavigationSnapshot, error) {
	if !s.canManage(actor) {
		return NavigationSnapshot{}, identity.ErrPermissionDenied
	}
	if s == nil || s.navigationCommands == nil || id <= 0 {
		return NavigationSnapshot{}, ErrInvalid
	}
	return s.navigationCommands.GetNavigationSnapshot(ctx, id)
}

func (s *Service) RestoreNavigationSnapshot(ctx context.Context, actor identity.Actor, id int64, expectedRevision uint64, reason string) (NavigationDocument, error) {
	if !s.canManage(actor) {
		return NavigationDocument{}, identity.ErrPermissionDenied
	}
	if s == nil || s.navigationCommands == nil || id <= 0 || expectedRevision < 1 || !validNavigationReason(reason) {
		return NavigationDocument{}, ErrInvalid
	}
	snapshot, err := s.navigationCommands.GetNavigationSnapshot(ctx, id)
	if err != nil {
		return NavigationDocument{}, err
	}
	return s.applyNavigationMutation(ctx, actor, expectedRevision, reason, audit.ActionSiteNavigationSnapshotRestore, "snapshot_restore", snapshot.Document, false)
}

func (s *Service) ExportNavigationBackup(ctx context.Context, actor identity.Actor) (NavigationBackup, error) {
	document, err := s.ReadAdminNavigationDocument(ctx, actor)
	if err != nil {
		return NavigationBackup{}, err
	}
	return NavigationBackup{Schema: NavigationBackupSchemaID, ExportedAt: time.Now().UTC(), Definitions: document.Definitions, Placements: document.Placements}, nil
}

func (s *Service) PreviewNavigationImport(ctx context.Context, actor identity.Actor, input NavigationImportPreviewInput) (NavigationPreview, error) {
	if !s.canManage(actor) {
		return NavigationPreview{}, identity.ErrPermissionDenied
	}
	if input.ExpectedRevision < 1 || (input.Mode != "merge" && input.Mode != "replace") || input.Backup.Schema != NavigationBackupSchemaID {
		return NavigationPreview{}, ErrInvalid
	}
	if encoded, err := json.Marshal(input.Backup); err != nil || len(encoded) > NavigationMaxBackupBytes {
		return NavigationPreview{}, ErrInvalid
	}
	imported, err := normalizeNavigationDocument(NavigationDocument{Definitions: input.Backup.Definitions, Placements: input.Backup.Placements})
	if err != nil {
		return NavigationPreview{}, err
	}
	current, err := s.ReadAdminNavigationDocument(ctx, actor)
	if err != nil {
		return NavigationPreview{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return NavigationPreview{}, ErrConflict
	}
	proposed := imported
	if input.Mode == "merge" {
		proposed = mergeNavigationDocuments(current, imported)
	}
	proposed, err = normalizeNavigationDocument(proposed)
	if err != nil {
		return NavigationPreview{}, err
	}
	warnings, warningEntries := navigationImportWarnings(proposed)
	return s.makeNavigationPreview(actor, current, proposed, input.Mode, warnings, warningEntries)
}

func (s *Service) makeNavigationPreview(actor identity.Actor, current, proposed NavigationDocument, mode string, warnings []string, warningEntries []NavigationPreviewWarning) (NavigationPreview, error) {
	token, err := s.navigationPreviews.put(actor.ID, current.Revision, mode, proposed)
	if err != nil {
		return NavigationPreview{}, err
	}
	if warningEntries == nil {
		warningEntries = []NavigationPreviewWarning{}
	}
	return NavigationPreview{
		PreviewToken: token, ExpectedRevision: current.Revision, Mode: mode,
		Changes: navigationDocumentChanges(current, proposed), Warnings: nonNilStrings(warnings),
		ChangeEntries: navigationDocumentChangeEntries(current, proposed), WarningEntries: warningEntries,
	}, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Service) applyNavigationMutation(ctx context.Context, actor identity.Actor, expectedRevision uint64, reason, action, operation string, proposed NavigationDocument, enforceOperatorOwnership bool) (NavigationDocument, error) {
	if s == nil || s.navigationCommands == nil || s.navigationRevision == nil || expectedRevision < 1 || !validNavigationReason(reason) {
		return NavigationDocument{}, ErrInvalid
	}
	var publicRevision int64
	document, err := s.navigationCommands.ExecuteNavigationTransaction(ctx, expectedRevision, func(ctx context.Context, tx pgx.Tx, current NavigationDocument) (NavigationTransactionResult, error) {
		normalized, err := normalizeNavigationDocument(proposed)
		if err != nil {
			return NavigationTransactionResult{}, err
		}
		if enforceOperatorOwnership && !operatorOwnsNavigationDefinitions(current, normalized) {
			return NavigationTransactionResult{}, ErrInvalid
		}
		affected := navigationAffectedLocations(current, normalized)
		if s.navigationAudit != nil {
			if err := s.navigationAudit.AppendTx(ctx, tx, audit.Event{ActorUserID: actor.ID, Action: action, Metadata: map[string]any{
				"operation": operation, "reason": reason, "expectedRevision": expectedRevision, "affectedLocations": affected,
			}}); err != nil {
				return NavigationTransactionResult{}, fmt.Errorf("audit navigation mutation: %w", err)
			}
		}
		publicRevision, err = s.navigationRevision.BumpPublicSurfaceRevisionTx(ctx, tx)
		if err != nil {
			return NavigationTransactionResult{}, err
		}
		return NavigationTransactionResult{Document: normalized, ActorUserID: actor.ID, Operation: operation, Reason: reason, AffectedLocations: affected}, nil
	})
	if err != nil {
		return NavigationDocument{}, err
	}
	if publicRevision > 0 {
		s.navigationRevision.Invalidate()
	}
	return s.navigationDocumentForAdmin(document), nil
}

func normalizeNavigationDocument(input NavigationDocument) (NavigationDocument, error) {
	if len(input.Definitions) > NavigationMaxDefinitions || len(input.Placements) > NavigationMaxPlacements {
		return NavigationDocument{}, ErrInvalid
	}
	result := NavigationDocument{Revision: input.Revision, Definitions: make([]NavigationDefinition, 0, len(input.Definitions)+len(CoreNavigationDefinitions())), Placements: make([]NavigationPlacement, 0, len(input.Placements))}
	definitions := map[string]NavigationDefinition{}
	for _, definition := range input.Definitions {
		canonical, ok := canonicalNavigationDefinition(definition)
		if !ok || definitions[canonical.SourceKey].SourceKey != "" {
			return NavigationDocument{}, ErrInvalid
		}
		definitions[canonical.SourceKey] = canonical
	}
	for _, core := range CoreNavigationDefinitions() {
		definitions[core.SourceKey] = core
	}
	for _, definition := range definitions {
		result.Definitions = append(result.Definitions, definition)
	}
	sort.Slice(result.Definitions, func(i, j int) bool { return result.Definitions[i].SourceKey < result.Definitions[j].SourceKey })
	seenPlacements := map[string]bool{}
	for _, placement := range input.Placements {
		placement.SourceKey = strings.TrimSpace(placement.SourceKey)
		placement.Location = strings.TrimSpace(placement.Location)
		placement.Visibility = strings.TrimSpace(placement.Visibility)
		placement.Permission = strings.TrimSpace(placement.Permission)
		definition := definitions[placement.SourceKey]
		if !isNavigationLocation(placement.Location) || definition.SourceKey == "" || !validNavigationPlacement(placement) ||
			(placement.MaxItems != 0 && definition.LinkKind != NavigationLinkDynamicBlock) {
			return NavigationDocument{}, ErrInvalid
		}
		key := navigationPlacementKey(placement.SourceKey, placement.Location)
		if seenPlacements[key] {
			return NavigationDocument{}, ErrInvalid
		}
		seenPlacements[key] = true
		result.Placements = append(result.Placements, placement)
	}
	sort.Slice(result.Placements, func(i, j int) bool {
		if result.Placements[i].Location != result.Placements[j].Location {
			return result.Placements[i].Location < result.Placements[j].Location
		}
		if result.Placements[i].Order != result.Placements[j].Order {
			return result.Placements[i].Order < result.Placements[j].Order
		}
		return result.Placements[i].SourceKey < result.Placements[j].SourceKey
	})
	return result, nil
}

func canonicalNavigationDefinition(definition NavigationDefinition) (NavigationDefinition, bool) {
	definition.SourceKey = strings.TrimSpace(definition.SourceKey)
	definition.SourceKind = strings.TrimSpace(definition.SourceKind)
	definition.LinkKind = strings.TrimSpace(definition.LinkKind)
	for _, core := range CoreNavigationDefinitions() {
		if definition.SourceKey == core.SourceKey && definition.SourceKind == core.SourceKind && definition.LinkKind == core.LinkKind {
			return core, true
		}
	}
	if definition.SourceKind == NavigationSourceOperator && ValidateNavigationDefinition(definition) {
		return definition, true
	}
	if definition.SourceKind == NavigationSourceExtension && validInertExtensionDefinition(definition) {
		return definition, true
	}
	return NavigationDefinition{}, false
}

func validInertExtensionDefinition(definition NavigationDefinition) bool {
	if !strings.HasPrefix(definition.SourceKey, "extension.") || definition.ExtensionID == "" || definition.ContributionID == "" ||
		(definition.LinkKind != NavigationLinkExtensionHost && definition.LinkKind != NavigationLinkExtensionRoute) || definition.Href != "" ||
		utf8.RuneCountInString(definition.SourceKey) > 160 || utf8.RuneCountInString(definition.ExtensionID) > 120 || utf8.RuneCountInString(definition.ContributionID) > 120 ||
		utf8.RuneCountInString(definition.LabelZhCN) > NavigationMaxLabelRunes || utf8.RuneCountInString(definition.LabelEnUS) > NavigationMaxLabelRunes || utf8.RuneCountInString(definition.Icon) > NavigationMaxIconRunes {
		return false
	}
	return !containsUnsafeNavigationText(definition.SourceKey) && !containsUnsafeNavigationText(definition.ExtensionID) && !containsUnsafeNavigationText(definition.ContributionID) && !containsUnsafeNavigationText(definition.LabelZhCN) && !containsUnsafeNavigationText(definition.LabelEnUS) && !containsUnsafeNavigationText(definition.Icon)
}

func validNavigationPlacement(placement NavigationPlacement) bool {
	if placement.Order < positionMin || placement.Order > positionMax || placement.MaxItems < 0 || placement.MaxItems > NavigationMaxDynamicItems || (placement.IconHidden && placement.Icon != "") || utf8.RuneCountInString(placement.LabelZhCN) > NavigationMaxLabelRunes || utf8.RuneCountInString(placement.LabelEnUS) > NavigationMaxLabelRunes || utf8.RuneCountInString(placement.Icon) > NavigationMaxIconRunes || utf8.RuneCountInString(placement.Permission) > 120 || containsUnsafeNavigationText(placement.LabelZhCN) || containsUnsafeNavigationText(placement.LabelEnUS) || containsUnsafeNavigationText(placement.Icon) || containsUnsafeNavigationText(placement.Permission) {
		return false
	}
	switch placement.Visibility {
	case NavigationVisibilityPublic, NavigationVisibilityAnonymous, NavigationVisibilityAuthenticated:
		return placement.Permission == ""
	case NavigationVisibilityPermission:
		return placement.Permission != ""
	default:
		return false
	}
}

func operatorOwnsNavigationDefinitions(current, proposed NavigationDocument) bool {
	currentByKey := navigationDefinitionsByKey(current.Definitions)
	proposedByKey := navigationDefinitionsByKey(proposed.Definitions)
	for key, definition := range proposedByKey {
		if definition.SourceKind == NavigationSourceOperator {
			continue
		}
		if definition.SourceKind == NavigationSourceCore || definition.SourceKind == NavigationSourceDynamic {
			continue
		}
		if existing, ok := currentByKey[key]; !ok || !reflect.DeepEqual(existing, definition) {
			return false
		}
	}
	for key, definition := range currentByKey {
		if definition.SourceKind == NavigationSourceExtension {
			if proposed, ok := proposedByKey[key]; !ok || !reflect.DeepEqual(definition, proposed) {
				return false
			}
		}
	}
	return true
}

func navigationDefaultsDocument(current NavigationDocument, locations []string) NavigationDocument {
	result := NavigationDocument{Revision: current.Revision, Definitions: append([]NavigationDefinition(nil), current.Definitions...), Placements: make([]NavigationPlacement, 0, len(current.Placements))}
	target := map[string]bool{}
	for _, location := range locations {
		target[location] = true
	}
	for _, placement := range current.Placements {
		if !target[placement.Location] {
			result.Placements = append(result.Placements, placement)
		}
	}
	for _, recommended := range NavigationRecommendedPlacements() {
		if target[recommended.Location] {
			result.Placements = append(result.Placements, NavigationPlacement{SourceKey: recommended.SourceKey, Location: recommended.Location, Order: recommended.Order, Enabled: true, Visibility: NavigationVisibilityPublic})
		}
	}
	return result
}

func mergeNavigationDocuments(current, imported NavigationDocument) NavigationDocument {
	definitions := navigationDefinitionsByKey(current.Definitions)
	for _, definition := range imported.Definitions {
		definitions[definition.SourceKey] = definition
	}
	placements := navigationPlacementsByKey(current.Placements)
	for _, placement := range imported.Placements {
		placements[navigationPlacementKey(placement.SourceKey, placement.Location)] = placement
	}
	result := NavigationDocument{Revision: current.Revision, Definitions: make([]NavigationDefinition, 0, len(definitions)), Placements: make([]NavigationPlacement, 0, len(placements))}
	for _, definition := range definitions {
		result.Definitions = append(result.Definitions, definition)
	}
	for _, placement := range placements {
		result.Placements = append(result.Placements, placement)
	}
	return result
}

func navigationDocumentChanges(current, proposed NavigationDocument) []string {
	changes := make([]string, 0)
	for _, location := range NavigationLocations() {
		before, after := navigationPlacementsForLocation(current.Placements, location), navigationPlacementsForLocation(proposed.Placements, location)
		if !reflect.DeepEqual(before, after) {
			changes = append(changes, location)
		}
	}
	if !reflect.DeepEqual(navigationDefinitionsByKey(current.Definitions), navigationDefinitionsByKey(proposed.Definitions)) {
		changes = append(changes, "definitions")
	}
	return changes
}

func navigationDocumentChangeEntries(current, proposed NavigationDocument) []NavigationPreviewChange {
	entries := make([]NavigationPreviewChange, 0)
	for _, location := range NavigationLocations() {
		before := navigationPlacementsForLocation(current.Placements, location)
		after := navigationPlacementsForLocation(proposed.Placements, location)
		if !reflect.DeepEqual(before, after) {
			entries = append(entries, NavigationPreviewChange{Kind: NavigationPreviewChangeLocation, Location: location, BeforeCount: len(before), AfterCount: len(after)})
		}
	}
	beforeDefinitions := navigationDefinitionsByKey(current.Definitions)
	afterDefinitions := navigationDefinitionsByKey(proposed.Definitions)
	if !reflect.DeepEqual(beforeDefinitions, afterDefinitions) {
		entries = append(entries, NavigationPreviewChange{Kind: NavigationPreviewChangeDefinitions, BeforeCount: len(beforeDefinitions), AfterCount: len(afterDefinitions)})
	}
	return entries
}

func navigationAffectedLocations(current, proposed NavigationDocument) []string {
	changes := navigationDocumentChanges(current, proposed)
	locations := make([]string, 0, len(changes))
	for _, change := range changes {
		if isNavigationLocation(change) {
			locations = append(locations, change)
		}
	}
	return locations
}

func navigationPlacementsForLocation(placements []NavigationPlacement, location string) []NavigationPlacement {
	result := make([]NavigationPlacement, 0)
	for _, placement := range placements {
		if placement.Location == location {
			result = append(result, placement)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Order != result[j].Order {
			return result[i].Order < result[j].Order
		}
		return result[i].SourceKey < result[j].SourceKey
	})
	return result
}

func navigationImportWarnings(document NavigationDocument) ([]string, []NavigationPreviewWarning) {
	warnings := make([]string, 0)
	entries := make([]NavigationPreviewWarning, 0)
	for _, definition := range document.Definitions {
		if definition.SourceKind == NavigationSourceExtension {
			warnings = append(warnings, "extension reference remains inert until the active artifact publishes it: "+definition.SourceKey)
			entries = append(entries, NavigationPreviewWarning{Code: NavigationPreviewWarningExtensionReferenceInert, SourceKey: definition.SourceKey, ExtensionID: definition.ExtensionID})
		}
	}
	return warnings, entries
}

func isNavigationLocation(location string) bool {
	for _, known := range NavigationLocations() {
		if location == known {
			return true
		}
	}
	return false
}

func validNavigationReason(reason string) bool {
	return utf8.RuneCountInString(reason) <= NavigationMaxReasonRunes && !containsUnsafeNavigationText(reason)
}
