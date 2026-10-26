package attachments

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func TestCompressionSettingsPermissionsAndPersistence(t *testing.T) {
	optionStore := &fakeOptionStore{}
	service := NewCompressionService(nil, nil, options.NewServiceWithCacheTTL(optionStore, time.Minute), nil)
	denied := identity.Actor{ID: 1, Status: identity.UserStatusActive}
	if _, err := service.Settings(context.Background(), denied); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("Settings without permission = %v", err)
	}
	if _, err := service.UpdateSettings(context.Background(), denied, CompressionSettings{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("UpdateSettings without permission = %v", err)
	}

	actor := attachmentSettingsActor()
	updated, err := service.UpdateSettings(context.Background(), actor, CompressionSettings{
		Enabled: true, Strength: 80, MaxDimension: 1800, MinSizeKB: 512, MinSavingsPercent: 12,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if updated.Strength != 80 || updated.JPEGQuality != 75 || updated.PolicyDigest == "" {
		t.Fatalf("updated settings = %#v", updated)
	}
	if optionStore.items[options.NameAttachmentCompressionStrength] != "80" ||
		optionStore.items[options.NameAttachmentCompressionMaxDimension] != "1800" {
		t.Fatalf("persisted options = %#v", optionStore.items)
	}
	if _, err := service.UpdateSettings(context.Background(), actor, CompressionSettings{
		Enabled: true, Strength: 101, MaxDimension: 1800, MinSizeKB: 512, MinSavingsPercent: 12,
	}); !errors.Is(err, ErrInvalidAttachment) {
		t.Fatalf("out-of-range settings = %v", err)
	}
}

func TestCompressionBackfillRequiresAttachmentManage(t *testing.T) {
	service := NewCompressionService(nil, nil, newAttachmentOptions(nil), nil)
	if _, err := service.Backfill(context.Background(), attachmentSettingsActor(), 100); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("settings manager must not backfill without attachment.manage: %v", err)
	}
}

func TestCompressionScheduleHonorsEnabledEligibilityAndDurableDedupe(t *testing.T) {
	ctx := context.Background()
	store := &fakeCompressionStore{createID: 77, createCreated: true}
	enqueued := []int64{}
	service := NewCompressionService(store, nil, newAttachmentOptions(nil), func(_ context.Context, id int64) error {
		enqueued = append(enqueued, id)
		return nil
	})
	eligible := Attachment{ID: 1, Status: StatusActive, ContentType: "image/jpeg", SizeBytes: 300 * 1024, SHA256: strings.Repeat("a", 64)}
	if err := service.Schedule(ctx, eligible); err != nil {
		t.Fatal(err)
	}
	if store.createCalls != 1 || len(enqueued) != 1 || enqueued[0] != 77 {
		t.Fatalf("eligible schedule calls=%d enqueued=%v", store.createCalls, enqueued)
	}
	if err := service.Schedule(ctx, Attachment{ID: 2, Status: StatusActive, ContentType: "image/jpeg", SizeBytes: 10 * 1024}); err != nil {
		t.Fatal(err)
	}
	if err := service.Schedule(ctx, Attachment{ID: 3, Status: StatusActive, ContentType: "image/webp", SizeBytes: 300 * 1024}); err != nil {
		t.Fatal(err)
	}
	if store.createCalls != 1 {
		t.Fatalf("ineligible images created tasks: %d", store.createCalls)
	}

	disabledStore := &fakeCompressionStore{createID: 88, createCreated: true}
	disabled := NewCompressionService(disabledStore, nil, newAttachmentOptions(map[string]string{
		options.NameAttachmentCompressionEnabled: "disabled",
	}), nil)
	if err := disabled.Schedule(ctx, eligible); err != nil {
		t.Fatal(err)
	}
	if disabledStore.createCalls != 0 {
		t.Fatalf("disabled compression created %d tasks", disabledStore.createCalls)
	}
}

func TestCompressionStatsAndBackfillUseDistinctPermissions(t *testing.T) {
	store := &fakeCompressionStore{
		stats:       CompressionStats{Pending: 2, ReadyVariants: 3, SavedBytes: 4096},
		backfillIDs: []int64{10, 11},
	}
	enqueued := []int64{}
	service := NewCompressionService(store, nil, newAttachmentOptions(nil), func(_ context.Context, id int64) error {
		enqueued = append(enqueued, id)
		return nil
	})
	stats, err := service.Stats(context.Background(), attachmentSettingsActor())
	if err != nil || stats.ReadyVariants != 3 {
		t.Fatalf("Stats = %#v, %v", stats, err)
	}
	if _, err := service.Stats(context.Background(), manageActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("attachment manager without settings permission read stats: %v", err)
	}
	result, err := service.Backfill(context.Background(), manageActor(), 100)
	if err != nil || result.Scheduled != 2 || len(enqueued) != 2 {
		t.Fatalf("Backfill = %#v, enqueued=%v, err=%v", result, enqueued, err)
	}
}

func TestOpenVariantPreservesAuthorizationAndFallsBackToOriginal(t *testing.T) {
	ctx := context.Background()
	attachment := Attachment{
		ID: 1, PublicID: "pub-compression", Provider: storage.ProviderLocal,
		ObjectKey: "original.jpg", OriginalName: "photo.jpg", ContentType: "image/jpeg",
		SizeBytes: 300 * 1024, SHA256: strings.Repeat("a", 64), Visibility: VisibilityPrivate,
		Status: StatusActive, Owner: &OwnerSummary{ID: 42},
	}
	attachmentStore := &accessFakeStore{item: attachment}
	adapter := &compressionReadAdapter{objects: map[string]string{
		"original.jpg": "original-bytes",
		"display.jpg":  "variant-bytes",
	}}
	attachmentService := NewServiceWithAdapterFactory(attachmentStore, newAttachmentOptions(nil), func(storage.Config) (storage.Adapter, error) {
		return adapter, nil
	})
	compressionOptions := newAttachmentOptions(nil)
	currentSettings, err := NewCompressionService(nil, nil, compressionOptions, nil).runtimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compressionStore := &fakeCompressionStore{variant: AttachmentVariant{
		AttachmentID: 1, Name: CompressionVariantDisplay, Provider: storage.ProviderLocal,
		ObjectKey: "display.jpg", ContentType: "image/jpeg", SourceSHA256: attachment.SHA256,
		PolicyDigest: currentSettings.PolicyDigest,
	}}
	service := NewCompressionService(compressionStore, attachmentService, compressionOptions, nil)
	owner := identity.Actor{ID: 42, Status: identity.UserStatusActive}

	_, reader, err := service.OpenVariant(ctx, owner, attachment.PublicID, CompressionVariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCompressionBody(t, reader); got != "variant-bytes" {
		t.Fatalf("ready variant body = %q", got)
	}

	compressionStore.variantErr = ErrAttachmentNotFound
	_, reader, err = service.OpenVariant(ctx, owner, attachment.PublicID, CompressionVariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCompressionBody(t, reader); got != "original-bytes" {
		t.Fatalf("missing variant fallback = %q", got)
	}

	compressionStore.variantErr = nil
	adapter.openErrors = map[string]error{"display.jpg": errors.New("variant unavailable")}
	_, reader, err = service.OpenVariant(ctx, owner, attachment.PublicID, CompressionVariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCompressionBody(t, reader); got != "original-bytes" {
		t.Fatalf("storage failure fallback = %q", got)
	}

	adapter.openErrors = nil
	compressionStore.variant.PolicyDigest = strings.Repeat("b", 64)
	_, reader, err = service.OpenVariant(ctx, owner, attachment.PublicID, CompressionVariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCompressionBody(t, reader); got != "original-bytes" {
		t.Fatalf("stale policy fallback = %q", got)
	}

	disabled := NewCompressionService(compressionStore, attachmentService, newAttachmentOptions(map[string]string{
		options.NameAttachmentCompressionEnabled: "disabled",
	}), nil)
	_, reader, err = disabled.OpenVariant(ctx, owner, attachment.PublicID, CompressionVariantDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCompressionBody(t, reader); got != "original-bytes" {
		t.Fatalf("disabled compression fallback = %q", got)
	}

	if _, _, err := service.OpenVariant(ctx, identity.Actor{ID: 7, Status: identity.UserStatusActive}, attachment.PublicID, CompressionVariantDisplay); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("unauthorized private variant read = %v", err)
	}
}

func TestCompressionEligibilityUsesFormatSizeAndDimensions(t *testing.T) {
	settings := (CompressionSettings{Enabled: true, Strength: 55, MaxDimension: 2560, MinSizeKB: 256, MinSavingsPercent: 8}).normalized()
	width, height := 3000, 1000
	tests := []struct {
		name string
		item Attachment
		want bool
	}{
		{name: "large jpeg", item: Attachment{Status: StatusActive, ContentType: "image/jpeg", SizeBytes: 300 * 1024}, want: true},
		{name: "large dimension", item: Attachment{Status: StatusActive, ContentType: "image/png", SizeBytes: 10 * 1024, ImageWidth: &width, ImageHeight: &height}, want: true},
		{name: "small image", item: Attachment{Status: StatusActive, ContentType: "image/jpeg", SizeBytes: 10 * 1024}, want: false},
		{name: "unsupported", item: Attachment{Status: StatusActive, ContentType: "image/webp", SizeBytes: 300 * 1024}, want: false},
		{name: "disabled attachment", item: Attachment{Status: StatusDisabled, ContentType: "image/png", SizeBytes: 300 * 1024}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := compressionEligible(test.item, settings); got != test.want {
				t.Fatalf("compressionEligible() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCompressionObjectKeyIsDeterministicAndScopedBesideSource(t *testing.T) {
	settings := (CompressionSettings{Enabled: true, Strength: 55, MaxDimension: 2560, MinSizeKB: 256, MinSavingsPercent: 8}).normalized()
	key := compressionObjectKey("2026/07/photo.jpg", CompressionVariantDisplay, settings.PolicyDigest, ".jpg")
	if key != "2026/07/photo.jpg.variants/display-"+settings.PolicyDigest[:12]+".jpg" {
		t.Fatalf("compression object key = %q", key)
	}
}

type fakeCompressionStore struct {
	createID      int64
	createCreated bool
	createCalls   int
	stats         CompressionStats
	backfillIDs   []int64
	variant       AttachmentVariant
	variantErr    error
}

func (s *fakeCompressionStore) CreateCompressionTask(context.Context, Attachment, CompressionSettings) (int64, bool, error) {
	s.createCalls++
	return s.createID, s.createCreated, nil
}
func (s *fakeCompressionStore) ClaimCompressionTask(context.Context, int64) (CompressionTask, error) {
	return CompressionTask{}, ErrAttachmentNotFound
}
func (s *fakeCompressionStore) CompleteCompressionTask(context.Context, CompressionTask, AttachmentVariant) (*AttachmentVariant, error) {
	return nil, nil
}
func (s *fakeCompressionStore) FinishCompressionTask(context.Context, int64, string, string) error {
	return nil
}
func (s *fakeCompressionStore) GetAttachmentVariant(context.Context, int64, string) (AttachmentVariant, error) {
	return s.variant, s.variantErr
}
func (s *fakeCompressionStore) CompressionStats(context.Context) (CompressionStats, error) {
	return s.stats, nil
}
func (s *fakeCompressionStore) BackfillCompressionTasks(context.Context, CompressionSettings, int) ([]int64, error) {
	return append([]int64(nil), s.backfillIDs...), nil
}

type compressionReadAdapter struct {
	objects    map[string]string
	openErrors map[string]error
}

func (a *compressionReadAdapter) Put(context.Context, string, storage.PutInput) error { return nil }
func (a *compressionReadAdapter) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if err := a.openErrors[key]; err != nil {
		return nil, err
	}
	body, ok := a.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewBufferString(body)), nil
}
func (a *compressionReadAdapter) Delete(context.Context, string) error { return nil }
func (a *compressionReadAdapter) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}
func (a *compressionReadAdapter) Exists(context.Context, string) (bool, error) { return true, nil }
func (a *compressionReadAdapter) PublicURL(string) string                      { return "" }
func (a *compressionReadAdapter) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (a *compressionReadAdapter) Probe(context.Context) error { return nil }

func readCompressionBody(t *testing.T, reader io.ReadCloser) string {
	t.Helper()
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
