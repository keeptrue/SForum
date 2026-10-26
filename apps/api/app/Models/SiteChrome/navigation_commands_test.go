package sitechrome

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type memoryNavigationCommandStore struct {
	document  NavigationDocument
	snapshots []NavigationSnapshot
}

func (s *memoryNavigationCommandStore) ReadNavigationDocument(context.Context) (NavigationDocument, error) {
	return s.document, nil
}

func (s *memoryNavigationCommandStore) ExecuteNavigationTransaction(ctx context.Context, expected uint64, mutation func(context.Context, pgx.Tx, NavigationDocument) (NavigationTransactionResult, error)) (NavigationDocument, error) {
	if expected != s.document.Revision {
		return NavigationDocument{}, ErrConflict
	}
	result, err := mutation(ctx, nil, s.document)
	if err != nil {
		return NavigationDocument{}, err
	}
	s.snapshots = append([]NavigationSnapshot{{ID: int64(len(s.snapshots) + 1), Revision: s.document.Revision, ActorUserID: result.ActorUserID, Operation: result.Operation, Reason: result.Reason, AffectedLocations: result.AffectedLocations, Document: s.document}}, s.snapshots...)
	if len(s.snapshots) > NavigationMaxSnapshots {
		s.snapshots = s.snapshots[:NavigationMaxSnapshots]
	}
	result.Document.Revision = s.document.Revision + 1
	s.document = result.Document
	return result.Document, nil
}

func (s *memoryNavigationCommandStore) ListNavigationSnapshots(context.Context) ([]NavigationSnapshot, error) {
	return append([]NavigationSnapshot(nil), s.snapshots...), nil
}

func (s *memoryNavigationCommandStore) GetNavigationSnapshot(_ context.Context, id int64) (NavigationSnapshot, error) {
	for _, snapshot := range s.snapshots {
		if snapshot.ID == id {
			return snapshot, nil
		}
	}
	return NavigationSnapshot{}, ErrNotFound
}

type memoryNavigationRevisionBumper struct{ bumps, invalidations int }

func (s *memoryNavigationRevisionBumper) BumpPublicSurfaceRevisionTx(context.Context, pgx.Tx) (int64, error) {
	s.bumps++
	return int64(s.bumps + 1), nil
}
func (s *memoryNavigationRevisionBumper) Invalidate() { s.invalidations++ }

func commandTestService(document NavigationDocument) (*Service, *memoryNavigationCommandStore, *memoryNavigationRevisionBumper) {
	store := &memoryNavigationCommandStore{document: document}
	revision := &memoryNavigationRevisionBumper{}
	service := NewService(newFakeStore()).WithNavigationCommandDependencies(store, nil, revision)
	return service, store, revision
}

func navigationManager() identity.Actor {
	return identity.Actor{ID: 11, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}
}

func TestNavigationCommandsApplyIsRevisionedAndOperatorOwned(t *testing.T) {
	service, store, revision := commandTestService(NavigationDocument{Revision: 1})
	operator := NavigationDefinition{SourceKey: "operator.docs", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "文档", LabelEnUS: "Docs", Href: "/docs"}
	document, err := service.ApplyNavigationDocument(t.Context(), navigationManager(), NavigationApplyInput{ExpectedRevision: 1, Reason: "add docs", Document: NavigationDocument{Definitions: []NavigationDefinition{operator}, Placements: []NavigationPlacement{{SourceKey: operator.SourceKey, Location: NavigationLocationTopbar, Order: 45, Enabled: true, Visibility: NavigationVisibilityPublic}}}})
	if err != nil {
		t.Fatal(err)
	}
	if document.Revision != 2 || store.document.Revision != 2 || len(store.snapshots) != 1 || store.snapshots[0].ActorUserID != navigationManager().ID || revision.bumps != 1 || revision.invalidations != 1 {
		t.Fatalf("apply state document=%#v snapshots=%d bumps=%d invalidations=%d", document, len(store.snapshots), revision.bumps, revision.invalidations)
	}
	if _, err := service.ApplyNavigationDocument(t.Context(), navigationManager(), NavigationApplyInput{ExpectedRevision: 1, Document: document}); err != ErrConflict {
		t.Fatalf("stale apply error=%v", err)
	}
	forged := NavigationDefinition{SourceKey: "core.forged", SourceKind: NavigationSourceCore, LinkKind: NavigationLinkCoreRoute, Href: "/forged"}
	if _, err := service.ApplyNavigationDocument(t.Context(), navigationManager(), NavigationApplyInput{ExpectedRevision: 2, Document: NavigationDocument{Definitions: []NavigationDefinition{forged}}}); err != ErrInvalid {
		t.Fatalf("forged core error=%v", err)
	}
	if _, err := service.ApplyNavigationDocument(t.Context(), identity.Actor{ID: 12, Status: identity.UserStatusActive}, NavigationApplyInput{ExpectedRevision: 2, Document: document}); err != identity.ErrPermissionDenied {
		t.Fatalf("denied apply error=%v", err)
	}
}

func TestNavigationCommandsDefaultsPreviewIsActorAndRevisionFenced(t *testing.T) {
	operator := NavigationDefinition{SourceKey: "operator.docs", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "文档", Href: "/docs"}
	service, store, _ := commandTestService(NavigationDocument{Revision: 4, Definitions: []NavigationDefinition{operator}, Placements: []NavigationPlacement{{SourceKey: operator.SourceKey, Location: NavigationLocationTopbar, Order: 1, Enabled: true, Visibility: NavigationVisibilityPublic}}})
	preview, err := service.PreviewNavigationDefaults(t.Context(), navigationManager(), NavigationDefaultsPreviewInput{ExpectedRevision: 4, Scope: "location", Location: NavigationLocationTopbar})
	if err != nil || preview.Mode != "defaults" || preview.PreviewToken == "" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	locationChange, ok := previewChangeForLocation(preview.ChangeEntries, NavigationLocationTopbar)
	if !ok || locationChange.BeforeCount != 1 || locationChange.AfterCount != 3 {
		t.Fatalf("structured defaults changes=%#v", preview.ChangeEntries)
	}
	if _, err := service.ApplyNavigationPreview(t.Context(), identity.Actor{ID: 99, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}, NavigationPreviewApplyInput{ExpectedRevision: 4, PreviewToken: preview.PreviewToken}); err != ErrConflict {
		t.Fatalf("wrong actor token error=%v", err)
	}
	preview, err = service.PreviewNavigationDefaults(t.Context(), navigationManager(), NavigationDefaultsPreviewInput{ExpectedRevision: 4, Scope: "location", Location: NavigationLocationTopbar})
	if err != nil {
		t.Fatal(err)
	}
	document, err := service.ApplyNavigationPreview(t.Context(), navigationManager(), NavigationPreviewApplyInput{ExpectedRevision: 4, PreviewToken: preview.PreviewToken, Reason: "restore topbar"})
	if err != nil {
		t.Fatal(err)
	}
	if document.Revision != 5 || hasPlacement(store.document.Placements, operator.SourceKey, NavigationLocationTopbar) || !hasPlacement(store.document.Placements, "core.home", NavigationLocationTopbar) {
		t.Fatalf("defaults document=%#v", store.document)
	}
	if _, err := service.ApplyNavigationPreview(t.Context(), navigationManager(), NavigationPreviewApplyInput{ExpectedRevision: 5, PreviewToken: preview.PreviewToken}); err != ErrConflict {
		t.Fatalf("reused token error=%v", err)
	}
}

func TestNavigationCommandsImportPreservesPortableInertReferences(t *testing.T) {
	service, _, _ := commandTestService(NavigationDocument{Revision: 1})
	backup := NavigationBackup{Schema: NavigationBackupSchemaID, Definitions: []NavigationDefinition{
		{SourceKey: "operator.docs", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkExternal, LabelZhCN: "文档", Href: "https://example.test/docs"},
		{SourceKey: "extension.missing.docs", SourceKind: NavigationSourceExtension, LinkKind: NavigationLinkExtensionRoute, ExtensionID: "missing", ContributionID: "docs"},
	}, Placements: []NavigationPlacement{{SourceKey: "operator.docs", Location: NavigationLocationFooter, Order: 10, Enabled: true, Visibility: NavigationVisibilityPublic}, {SourceKey: "extension.missing.docs", Location: NavigationLocationFooter, Order: 20, Enabled: true, Visibility: NavigationVisibilityPublic}}}
	preview, err := service.PreviewNavigationImport(t.Context(), navigationManager(), NavigationImportPreviewInput{ExpectedRevision: 1, Mode: "replace", Backup: backup})
	if err != nil || len(preview.Warnings) != 1 || !strings.Contains(preview.Warnings[0], "inert") || len(preview.WarningEntries) != 1 || preview.WarningEntries[0].Code != NavigationPreviewWarningExtensionReferenceInert || preview.WarningEntries[0].SourceKey != "extension.missing.docs" || preview.WarningEntries[0].ExtensionID != "missing" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	if len(preview.ChangeEntries) != 2 || preview.ChangeEntries[0].Kind != NavigationPreviewChangeLocation || preview.ChangeEntries[1].Kind != NavigationPreviewChangeDefinitions {
		t.Fatalf("structured import changes=%#v", preview.ChangeEntries)
	}
	document, err := service.ApplyNavigationPreview(t.Context(), navigationManager(), NavigationPreviewApplyInput{ExpectedRevision: 1, PreviewToken: preview.PreviewToken, Reason: "import backup"})
	if err != nil {
		t.Fatal(err)
	}
	if document.Revision != 2 {
		t.Fatalf("import revision=%d", document.Revision)
	}
	export, err := service.ExportNavigationBackup(t.Context(), navigationManager())
	if err != nil || export.Schema != NavigationBackupSchemaID || export.ExportedAt.IsZero() {
		t.Fatalf("export=%#v err=%v", export, err)
	}
	for _, definition := range export.Definitions {
		if strings.Contains(definition.SourceKey, "site_navigation_definitions") {
			t.Fatalf("database identifier leaked: %#v", definition)
		}
	}
	tooLarge := backup
	tooLarge.Definitions = make([]NavigationDefinition, NavigationMaxDefinitions+1)
	if _, err := service.PreviewNavigationImport(t.Context(), navigationManager(), NavigationImportPreviewInput{ExpectedRevision: 2, Mode: "replace", Backup: tooLarge}); err != ErrInvalid {
		t.Fatalf("oversized import error=%v", err)
	}
}

func hasPlacement(placements []NavigationPlacement, sourceKey, location string) bool {
	for _, placement := range placements {
		if placement.SourceKey == sourceKey && placement.Location == location {
			return true
		}
	}
	return false
}

func previewChangeForLocation(entries []NavigationPreviewChange, location string) (NavigationPreviewChange, bool) {
	for _, entry := range entries {
		if entry.Kind == NavigationPreviewChangeLocation && entry.Location == location {
			return entry, true
		}
	}
	return NavigationPreviewChange{}, false
}
