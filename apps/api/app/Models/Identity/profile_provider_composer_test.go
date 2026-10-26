package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestProfileProviderComposerListSectionsOmitsFailedProviders(t *testing.T) {
	okProvider := testProfileProviderContribution("demo.profile.a", 20, []string{ProfileOperationSectionsList})
	omitProvider := testProfileProviderContribution("demo.profile.b", 10, []string{ProfileOperationSectionsList})
	omitProvider.Operations[0].FailurePolicy = identityregistry.ProviderFailureOmit

	source := &fakeProfileProviderSource{providers: []identityregistry.ProviderContribution{okProvider, omitProvider}}
	invoker := &fakeProfileProviderInvoker{
		byProvider: map[string]map[string]any{
			"demo.profile.a": {
				"sections": []any{
					map[string]any{"sectionId": "bio", "title": "Bio", "fields": map[string]any{"text": "hello"}},
				},
			},
		},
		failProvider: "demo.profile.b",
	}
	composer, err := NewProfileProviderComposer(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := composer.ListSections(context.Background(), 7, 7)
	if err != nil || len(sections) != 1 || sections[0].SectionID != "bio" || sections[0].ProviderID != "demo.profile.a" {
		t.Fatalf("sections=%#v err=%v", sections, err)
	}
}

func TestProfileProviderComposerUpdateSectionFailClosed(t *testing.T) {
	provider := testProfileProviderContribution("demo.profile", 1, []string{ProfileOperationSectionUpdate})
	source := &fakeProfileProviderSource{providers: []identityregistry.ProviderContribution{provider}}
	invoker := &fakeProfileProviderInvoker{
		byProvider: map[string]map[string]any{
			"demo.profile": {"sectionId": "bio", "fields": map[string]any{"text": "updated"}},
		},
	}
	composer, err := NewProfileProviderComposer(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	section, err := composer.UpdateSection(context.Background(), "demo.profile", "bio", 9, 9, map[string]any{"text": "updated"})
	if err != nil || section.SectionID != "bio" || section.Fields["text"] != "updated" {
		t.Fatalf("section=%#v err=%v", section, err)
	}
	// 非本人写入失败关闭。
	if _, err := composer.UpdateSection(context.Background(), "demo.profile", "bio", 9, 10, map[string]any{"text": "x"}); !errors.Is(err, ErrProfileProviderInvalid) {
		t.Fatalf("cross-user update: %v", err)
	}
	// 提供方失败 fail_closed。
	invoker.failProvider = "demo.profile"
	if _, err := composer.UpdateSection(context.Background(), "demo.profile", "bio", 9, 9, map[string]any{"text": "x"}); !errors.Is(err, ErrProfileProviderUnavailable) {
		t.Fatalf("provider failure: %v", err)
	}
}

func TestProfileProviderComposerAccountReadAndUpdate(t *testing.T) {
	provider := testProfileProviderContribution("demo.profile", 1, []string{
		ProfileOperationAccountRead, ProfileOperationAccountUpdate,
	})
	source := &fakeProfileProviderSource{providers: []identityregistry.ProviderContribution{provider}}
	invoker := &fakeProfileProviderInvoker{
		byProvider: map[string]map[string]any{
			"demo.profile": {"fields": map[string]any{"locale": "zh-CN"}},
		},
	}
	composer, err := NewProfileProviderComposer(source, invoker)
	if err != nil {
		t.Fatal(err)
	}
	view, err := composer.ReadAccount(context.Background(), "demo.profile", 3, 3)
	if err != nil || view.Fields["locale"] != "zh-CN" {
		t.Fatalf("read=%#v err=%v", view, err)
	}
	updated, err := composer.UpdateAccount(context.Background(), "demo.profile", 3, 3, map[string]any{"locale": "en-US"})
	if err != nil || updated.ProviderID != "demo.profile" {
		t.Fatalf("update=%#v err=%v", updated, err)
	}
}

func testProfileProviderContribution(id string, priority int, operations []string) identityregistry.ProviderContribution {
	ops := make([]identityregistry.ProviderOperation, 0, len(operations))
	for _, name := range operations {
		policy := identityregistry.ProviderFailureFailClosed
		if name == ProfileOperationSectionsList || name == ProfileOperationSectionRead {
			policy = identityregistry.ProviderFailureOmit
		}
		ops = append(ops, identityregistry.ProviderOperation{
			Name: name, InputSchema: "in@1", OutputSchema: "out@1",
			TimeoutMS: 1000, FailurePolicy: policy,
		})
	}
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: id, ContractVersion: id + "@1", Kind: identityregistry.ProviderKindProfile,
			Handler: "identity.profile", Priority: priority, Operations: ops,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "demo.membership", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("2", 64), VersionID: 8,
			RuntimeInstanceID: "runtime-profile",
		},
	}
}

type fakeProfileProviderSource struct {
	providers []identityregistry.ProviderContribution
}

func (f *fakeProfileProviderSource) ProfileProviders(context.Context) ([]identityregistry.ProviderContribution, error) {
	return f.providers, nil
}

func (f *fakeProfileProviderSource) ResolveProfileProvider(_ context.Context, providerID string) (identityregistry.ProviderContribution, error) {
	for _, provider := range f.providers {
		if provider.ID == providerID {
			return provider, nil
		}
	}
	return identityregistry.ProviderContribution{}, identityregistry.ErrNotFound
}

type fakeProfileProviderInvoker struct {
	byProvider   map[string]map[string]any
	failProvider string
}

func (f *fakeProfileProviderInvoker) InvokeExact(
	_ context.Context,
	provider identityregistry.ProviderContribution,
	_ string,
	_ int64,
	_ map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	if f.failProvider != "" && provider.ID == f.failProvider {
		return errors.New("provider down")
	}
	output := f.byProvider[provider.ID]
	return accept(context.Background(), output, func() error { return nil })
}
