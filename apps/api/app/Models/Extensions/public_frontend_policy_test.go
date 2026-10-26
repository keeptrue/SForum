package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
)

func TestPublicPagePolicyAggregatesStrictStructuredDirectives(t *testing.T) {
	extension := publicFrontendFixture(t)
	extension.Manifest.Assets[0].CSP = []string{
		"style-src 'self'",
		"img-src https://cdn.example.com",
		"connect-src https://API.Example.com 'self'",
	}
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	tuple := publicPagePolicyTuple(extension)
	policy, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{tuple, tuple})
	if err != nil {
		t.Fatal(err)
	}
	wantDirectives := []PublicFrontendPolicyDirective{
		{Name: "connect-src", Sources: []string{"'self'", "https://api.example.com"}},
		{Name: "img-src", Sources: []string{"https://cdn.example.com"}},
		{Name: "style-src", Sources: []string{"'self'"}},
	}
	wantDocumentDirectives := []PublicFrontendPolicyDirective{
		{Name: "base-uri", Sources: []string{"'none'"}},
		{Name: "connect-src", Sources: []string{"'self'", "https://api.example.com"}},
		{Name: "default-src", Sources: []string{"'none'"}},
		{Name: "font-src", Sources: []string{"'self'"}},
		{Name: "form-action", Sources: []string{"'self'"}},
		{Name: "frame-ancestors", Sources: []string{"'none'"}},
		{Name: "img-src", Sources: []string{"'self'", "https://cdn.example.com"}},
		{Name: "media-src", Sources: []string{"'self'"}},
		{Name: "object-src", Sources: []string{"'none'"}},
		{Name: "script-src", Sources: []string{"'self'"}},
		{Name: "style-src", Sources: []string{"'self'"}},
		{Name: "worker-src", Sources: []string{"'self'"}},
	}
	if policy.SchemaVersion != PublicFrontendPolicySchemaV1 ||
		policy.GraphDigest != service.publicAssets.Snapshot().Digest ||
		normalizedPublicDigest(policy.ExtensionPolicyDigest) == "" ||
		!reflect.DeepEqual(policy.Directives, wantDirectives) ||
		policy.DocumentPolicy.SchemaVersion != PublicFrontendDocumentPolicySchemaV1 ||
		normalizedPublicDigest(policy.DocumentPolicy.Digest) == "" ||
		!reflect.DeepEqual(policy.DocumentPolicy.Directives, wantDocumentDirectives) ||
		policy.DocumentPolicy.HeaderValue != publicPagePolicyHeaderValue(wantDocumentDirectives) ||
		len(publicPagePolicyHeaderPrefix)+len(policy.DocumentPolicy.HeaderValue) > maxPublicPagePolicyHeaderBytes {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	if len(policy.AdmittedComponents) != 1 {
		t.Fatalf("duplicate rendered tuple was not deduplicated: %#v", policy.AdmittedComponents)
	}
	admitted := policy.AdmittedComponents[0]
	publication, found := service.publicAssets.SnapshotPublication(extension.ID)
	if !found || admitted.ExtensionID != tuple.ExtensionID ||
		admitted.ExtensionVersion != tuple.ExtensionVersion || admitted.PackageDigest != tuple.PackageDigest ||
		admitted.ComponentID != tuple.ComponentID || admitted.ContractVersion != tuple.ContractVersion ||
		admitted.ImpactDigest != publication.Artifact.ImpactDigest {
		t.Fatalf("admitted tuple is not exact: %#v", admitted)
	}

	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(body)
	if strings.Contains(serialized, "revision") || strings.Contains(serialized, "contributors") ||
		strings.Contains(serialized, `"policyDigest"`) || !strings.Contains(serialized, `"extensionPolicyDigest"`) ||
		!strings.Contains(serialized, `"documentPolicy"`) {
		t.Fatalf("internal or ambiguous policy fields leaked: %s", body)
	}
	changedProvenance := policy
	changedProvenance.contributors = clonePublicFrontendPolicyContributors(policy.contributors)
	changedProvenance.contributors[0].ImpactDigest = strings.Repeat("f", 64)
	changedProvenance.ExtensionPolicyDigest = publicPageExtensionPolicyDigest(changedProvenance)
	changedDocument, err := publicPageDocumentPolicy(changedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	if changedDocument.HeaderValue != policy.DocumentPolicy.HeaderValue ||
		changedDocument.Digest == policy.DocumentPolicy.Digest {
		t.Fatalf("document digest did not bind exact provenance: original=%#v changed=%#v", policy.DocumentPolicy, changedDocument)
	}
	stale := tuple
	stale.PackageDigest = strings.Repeat("f", 64)
	_, err = service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{stale})
	policyErr := requirePublicPagePolicyError(t, err, PublicPagePolicyComponentUnavailable)
	if err.Error() != ErrPublicPagePolicyUnavailable.Error() ||
		policyErr.ExtensionID != stale.ExtensionID || policyErr.ComponentID != stale.ComponentID {
		t.Fatalf("public error leaked detail or typed evidence was lost: text=%q typed=%#v", err, policyErr)
	}
	service.WithPublicComponentAdmission(denyPublicComponentAdmission{})
	_, err = service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{tuple})
	requirePublicPagePolicyError(t, err, PublicPagePolicyComponentUnavailable)
}

func TestPublicPagePolicyValidatesDependencyOwnerLiveTrust(t *testing.T) {
	owner := publicAssetOnlyFixture(t, "owner.policy")
	owner.Manifest.Assets[0].CSP = []string{"font-src https://cdn.example.com"}
	consumer := publicFrontendFixtureFor(t, "consumer.policy", []string{owner.Manifest.Assets[0].Handle})
	consumer.Manifest.Assets[0].CSP = []string{"connect-src 'self'"}
	reader := &fakeFrontendExtensionReader{items: []Extension{consumer, owner}}
	store := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, owner)
	grantPublicFrontend(t, trust, consumer)
	if err := service.RestorePublicAssetPublications(t.Context(), []Extension{consumer, owner}, false); err != nil {
		t.Fatal(err)
	}

	policy, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(consumer)})
	if err != nil {
		t.Fatal(err)
	}
	want := []PublicFrontendPolicyDirective{
		{Name: "connect-src", Sources: []string{"'self'"}},
		{Name: "font-src", Sources: []string{"https://cdn.example.com"}},
	}
	if !reflect.DeepEqual(policy.Directives, want) || len(policy.AdmittedComponents) != 1 ||
		policy.AdmittedComponents[0].ExtensionID != consumer.ID {
		t.Fatalf("dependency policy=%#v", policy)
	}
	fontPolicy := publicPagePolicyDirectiveByName(t, policy.DocumentPolicy.Directives, "font-src")
	if !reflect.DeepEqual(fontPolicy.Sources, []string{"'self'", "https://cdn.example.com"}) {
		t.Fatalf("Host baseline did not merge dependency font source: %#v", fontPolicy)
	}
	contributors := policy.Contributors()
	if len(contributors) != 2 {
		t.Fatalf("cross-owner provenance=%#v", contributors)
	}
	ownerEvidence := publicPagePolicyContributorByID(t, contributors, owner.ID)
	if ownerEvidence.PackageDigest != owner.PackageDigest || ownerEvidence.OwnerKind != assetregistry.OwnerKindPlugin ||
		!reflect.DeepEqual(ownerEvidence.AssetHandles, []string{owner.Manifest.Assets[0].Handle}) ||
		!reflect.DeepEqual(ownerEvidence.Directives, []PublicFrontendPolicyDirective{{
			Name: "font-src", Sources: []string{"https://cdn.example.com"},
		}}) {
		t.Fatalf("asset-only dependency provenance=%#v", ownerEvidence)
	}
	contributors[0].AssetHandles[0] = "mutated"
	if fresh := policy.Contributors(); fresh[0].AssetHandles[0] == "mutated" {
		t.Fatal("caller mutation escaped into contributor evidence")
	}

	if err := store.RevokeAll(t.Context(), owner.ID, 1, "test"); err != nil {
		t.Fatal(err)
	}
	_, err = service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(consumer)})
	requirePublicPagePolicyError(t, err, PublicPagePolicyTrustUnavailable)
	if !errors.Is(err, ErrTrustGrantNotFound) {
		t.Fatalf("typed policy error lost live-grant cause: %v", err)
	}
	if snapshot := service.publicAssets.Snapshot(); len(snapshot.Publications) != 0 || len(snapshot.Assets) != 0 {
		t.Fatalf("revoked dependency did not quarantine its consumer closure: %#v", snapshot)
	}
}

func TestPublicPagePolicySafeModeFailsClosed(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)
	service.WithSafeMode(true)

	policy, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(extension)})
	requirePublicPagePolicyError(t, err, PublicPagePolicyRuntimeUnavailable)
	if !reflect.DeepEqual(policy, PublicFrontendPolicy{}) {
		t.Fatalf("Safe Mode returned partial policy: %#v", policy)
	}
}

func TestPublicPagePolicyForComponentsExpandsSoftRefsAndBaseline(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	// 空 soft refs → 仅 Host 基线，供无 L2 挂载的页面在 public L2 开启时仍可下发 CSP。
	baseline, err := service.PublicPagePolicyForComponents(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.DocumentPolicy.HeaderValue == "" ||
		!strings.Contains(baseline.DocumentPolicy.HeaderValue, "default-src 'none'") ||
		!strings.Contains(baseline.DocumentPolicy.HeaderValue, "script-src 'self'") ||
		len(baseline.AdmittedComponents) != 0 {
		t.Fatalf("unexpected baseline policy: %#v", baseline)
	}

	component := extension.Manifest.Components[0]
	expanded, err := service.PublicPagePolicyForComponents(t.Context(), []PublicFrontendComponentRef{{
		ExtensionID: extension.ID, ComponentID: component.ID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	exact, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(extension)})
	if err != nil {
		t.Fatal(err)
	}
	if expanded.DocumentPolicy.HeaderValue != exact.DocumentPolicy.HeaderValue ||
		expanded.DocumentPolicy.Digest != exact.DocumentPolicy.Digest ||
		len(expanded.AdmittedComponents) != 1 {
		t.Fatalf("soft-ref expansion drifted from exact tuples:\nsoft=%#v\nexact=%#v", expanded, exact)
	}

	_, err = service.PublicPagePolicyForComponents(t.Context(), []PublicFrontendComponentRef{{
		ExtensionID: extension.ID, ComponentID: "missing.component",
	}})
	requirePublicPagePolicyError(t, err, PublicPagePolicyComponentUnavailable)
}

func TestPublicPagePolicyIsDeterministicAcrossOrderAndRestart(t *testing.T) {
	owner := publicFrontendFixtureFor(t, "a.policy", nil)
	owner.Manifest.Assets[0].CSP = []string{"img-src https://images.example.com", "connect-src 'self'"}
	consumer := publicFrontendFixtureFor(t, "b.policy", []string{owner.Manifest.Assets[0].Handle})
	consumer.Manifest.Assets[0].CSP = []string{"connect-src https://api.example.com 'self'"}
	reader := &fakeFrontendExtensionReader{items: []Extension{consumer, owner}}

	firstTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	first := newAdmittedPublicFrontendService(reader, firstTrust)
	grantPublicFrontend(t, firstTrust, owner)
	grantPublicFrontend(t, firstTrust, consumer)
	if err := first.RestorePublicAssetPublications(t.Context(), []Extension{consumer, owner}, false); err != nil {
		t.Fatal(err)
	}
	firstPolicy, err := first.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{
		publicPagePolicyTuple(consumer), publicPagePolicyTuple(owner), publicPagePolicyTuple(consumer),
	})
	if err != nil {
		t.Fatal(err)
	}

	secondTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	second := newAdmittedPublicFrontendService(reader, secondTrust)
	grantPublicFrontend(t, secondTrust, consumer)
	grantPublicFrontend(t, secondTrust, owner)
	if err := second.RestorePublicAssetPublications(t.Context(), []Extension{owner, consumer}, false); err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := second.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{
		publicPagePolicyTuple(owner), publicPagePolicyTuple(consumer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPolicy, secondPolicy) {
		t.Fatalf("policy changed across order/restart:\nfirst=%#v\nsecond=%#v", firstPolicy, secondPolicy)
	}
}

func TestPublicPagePolicyRejectsUnsafeSourcesAndInvalidNone(t *testing.T) {
	tests := []struct {
		name string
		csp  string
	}{
		{name: "wildcard", csp: "script-src *"},
		{name: "unsafe keyword", csp: "script-src 'unsafe-inline'"},
		{name: "extension nonce", csp: "script-src 'nonce-deadbeef'"},
		{name: "external script", csp: "script-src https://scripts.example.com"},
		{name: "external style", csp: "style-src https://styles.example.com"},
		{name: "external worker", csp: "worker-src https://workers.example.com"},
		{name: "data scheme", csp: "img-src data:"},
		{name: "broad scheme", csp: "connect-src https:"},
		{name: "none combination", csp: "connect-src 'none' https://api.example.com"},
		{name: "host baseline none conflict", csp: "connect-src 'none'"},
		{name: "path source", csp: "connect-src https://api.example.com/v1"},
		{name: "comma delimiter", csp: "connect-src https://api.example.com,https://evil.example"},
		{name: "invalid host punctuation", csp: "connect-src https://api(example).com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := publicFrontendFixtureFor(t, "unsafe."+strings.ReplaceAll(test.name, " ", "_"), nil)
			extension.Manifest.Assets[0].CSP = []string{test.csp}
			reader := &fakeFrontendExtensionReader{item: extension}
			trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
			service := newAdmittedPublicFrontendService(reader, trust)
			grantPublicFrontend(t, trust, extension)
			publishTrustedPublicAssets(t, service, extension)

			_, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(extension)})
			requirePublicPagePolicyError(t, err, PublicPagePolicyDirectiveInvalid)
		})
	}
}

func TestStrictPublicPagePolicySourceCanonicalization(t *testing.T) {
	tests := []struct {
		name      string
		directive string
		source    string
		want      string
		ok        bool
	}{
		{name: "https default port", directive: "connect-src", source: "https://API.Example.com:443", want: "https://api.example.com", ok: true},
		{name: "wss default port", directive: "connect-src", source: "wss://Socket.Example.com:443", want: "wss://socket.example.com", ok: true},
		{name: "non-default port", directive: "img-src", source: "https://images.example.com:8443", want: "https://images.example.com:8443", ok: true},
		{name: "expanded IPv6", directive: "media-src", source: "https://[2001:0db8:0000:0000:0000:0000:0000:0001]:443", want: "https://[2001:db8::1]", ok: true},
		{name: "IPv6 non-default port", directive: "font-src", source: "https://[2001:db8::2]:8443", want: "https://[2001:db8::2]:8443", ok: true},
		{name: "punycode IDN", directive: "img-src", source: "https://xn--fsqu00a.xn--0zwm56d:443", want: "https://xn--fsqu00a.xn--0zwm56d", ok: true},
		{name: "unicode IDN", directive: "img-src", source: "https://\u4f8b\u5b50.\u6d4b\u8bd5", ok: false},
		{name: "script self", directive: "script-src", source: "'self'", want: "'self'", ok: true},
		{name: "worker none", directive: "worker-src", source: "'none'", want: "'none'", ok: true},
		{name: "remote script", directive: "script-src", source: "https://scripts.example.com", ok: false},
		{name: "remote style", directive: "style-src", source: "https://styles.example.com", ok: false},
		{name: "remote worker", directive: "worker-src", source: "https://workers.example.com", ok: false},
		{name: "wss image", directive: "img-src", source: "wss://images.example.com", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := strictPublicPagePolicySource(test.directive, test.source)
			if ok != test.ok || got != test.want {
				t.Fatalf("source=%q got=(%q,%t) want=(%q,%t)", test.source, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestPublicPagePolicyEnforcesComponentAndHeaderBounds(t *testing.T) {
	t.Run("components", func(t *testing.T) {
		extension := publicFrontendFixture(t)
		reader := &fakeFrontendExtensionReader{item: extension}
		trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
		service := newAdmittedPublicFrontendService(reader, trust)
		tuples := make([]PublicFrontendComponentTuple, maxPublicPagePolicyComponents+1)
		for index := range tuples {
			tuples[index] = publicPagePolicyTuple(extension)
		}
		_, err := service.PublicPagePolicy(t.Context(), tuples)
		requirePublicPagePolicyError(t, err, PublicPagePolicyBoundsExceeded)
	})

	t.Run("serialized header", func(t *testing.T) {
		extension := publicFrontendFixtureFor(t, "bounds.policy", nil)
		reader := &fakeFrontendExtensionReader{item: extension}
		trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
		service := newAdmittedPublicFrontendService(reader, trust)
		grantPublicFrontend(t, trust, extension)
		identity, err := trust.RuntimeIdentity(t.Context(), extension)
		if err != nil {
			t.Fatal(err)
		}
		publication, err := BuildPublicAssetPublication(extension, identity.ImpactDigest)
		if err != nil {
			t.Fatal(err)
		}
		styleIndex, entryIndex := -1, -1
		for index := range publication.Assets {
			switch {
			case publication.Assets[index].Type == "style":
				styleIndex = index
			case strings.HasSuffix(publication.Assets[index].Handle, publicL2EntrySuffix):
				entryIndex = index
			}
		}
		if styleIndex < 0 || entryIndex < 0 {
			t.Fatalf("fixture publication=%#v", publication)
		}
		publication.Assets[styleIndex].CSP = longPublicPagePolicyDeclarations(0)
		extra := publication.Assets[styleIndex]
		extra.Handle = extension.ID + ".asset.extra"
		extra.ContractVersion = extra.Handle + "@1"
		extra.CSP = longPublicPagePolicyDeclarations(32)
		publication.Assets = append(publication.Assets, extra)
		publication.Assets[entryIndex].Dependencies = append(publication.Assets[entryIndex].Dependencies, extra.Handle)
		if _, err := service.publicAssets.Publish(publication); err != nil {
			t.Fatal(err)
		}

		_, err = service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(extension)})
		requirePublicPagePolicyError(t, err, PublicPagePolicyBoundsExceeded)
	})

	t.Run("merged document header", func(t *testing.T) {
		sets := make(map[string]map[string]struct{})
		foundFinalOnlyOverflow := false
		for index := 0; index < maxPublicPageDirectiveSources; index++ {
			host := fmt.Sprintf(
				"%s.%s.%s.%03d.example",
				strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60), index,
			)
			addPublicPagePolicySource(sets, "connect-src", "https://"+host)
			directives, err := publicPagePolicyDirectivesFromSets(sets, true)
			if err != nil {
				break
			}
			policy := PublicFrontendPolicy{
				SchemaVersion: PublicFrontendPolicySchemaV1,
				GraphDigest:   strings.Repeat("a", 64),
				Directives:    directives,
			}
			policy.ExtensionPolicyDigest = publicPageExtensionPolicyDigest(policy)
			_, err = publicPageDocumentPolicy(policy)
			if err == nil {
				continue
			}
			requirePublicPagePolicyError(t, err, PublicPagePolicyBoundsExceeded)
			if publicPageExtensionPolicySerializedBytes(directives) > maxPublicPageExtensionPolicyBytes {
				t.Fatal("extension fragment exceeded its own bound before final merge")
			}
			foundFinalOnlyOverflow = true
			break
		}
		if !foundFinalOnlyOverflow {
			t.Fatal("test fixture never crossed the complete CSP header bound")
		}
	})
}

func TestPublicPagePolicyRejectsGraphChangeDuringAggregation(t *testing.T) {
	extension := publicFrontendFixture(t)
	baseReader := &fakeFrontendExtensionReader{item: extension}
	reader := &publicPagePolicyMutationReader{FrontendExtensionReader: baseReader}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	var mutationErr error
	reader.mutate = func() {
		_, mutationErr = service.publicAssets.Publish(assetregistry.Publication{Artifact: assetregistry.Artifact{
			ExtensionID: "core.policy", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
			OwnerKind: assetregistry.OwnerKindCore, Core: true,
		}})
	}
	_, err := service.PublicPagePolicy(t.Context(), []PublicFrontendComponentTuple{publicPagePolicyTuple(extension)})
	if mutationErr != nil {
		t.Fatal(mutationErr)
	}
	requirePublicPagePolicyError(t, err, PublicPagePolicySnapshotChanged)
}

func publicPagePolicyTuple(extension Extension) PublicFrontendComponentTuple {
	component := extension.Manifest.Components[0]
	return PublicFrontendComponentTuple{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ComponentID: component.ID,
		ContractVersion: component.ContractVersion,
	}
}

func publicPagePolicyContributorByID(
	t *testing.T,
	contributors []PublicFrontendPolicyContributor,
	extensionID string,
) PublicFrontendPolicyContributor {
	t.Helper()
	for _, contributor := range contributors {
		if contributor.ExtensionID == extensionID {
			return contributor
		}
	}
	t.Fatalf("contributor %q not found in %#v", extensionID, contributors)
	return PublicFrontendPolicyContributor{}
}

func publicPagePolicyDirectiveByName(
	t *testing.T,
	directives []PublicFrontendPolicyDirective,
	name string,
) PublicFrontendPolicyDirective {
	t.Helper()
	for _, directive := range directives {
		if directive.Name == name {
			return directive
		}
	}
	t.Fatalf("directive %q not found in %#v", name, directives)
	return PublicFrontendPolicyDirective{}
}

func requirePublicPagePolicyError(
	t *testing.T,
	err error,
	code PublicPagePolicyErrorCode,
) *PublicPagePolicyError {
	t.Helper()
	if !errors.Is(err, ErrPublicPagePolicyUnavailable) {
		t.Fatalf("error %v does not wrap ErrPublicPagePolicyUnavailable", err)
	}
	var policyErr *PublicPagePolicyError
	if !errors.As(err, &policyErr) || policyErr.Code != code {
		t.Fatalf("error=%v typed=%#v want code=%s", err, policyErr, code)
	}
	return policyErr
}

func longPublicPagePolicyDeclarations(offset int) []string {
	result := make([]string, 32)
	for index := range result {
		host := fmt.Sprintf(
			"%s.%s.%s.%02d.example",
			strings.Repeat("a", 60), strings.Repeat("b", 60), strings.Repeat("c", 60), offset+index,
		)
		result[index] = "connect-src https://" + host
	}
	return result
}

type publicPagePolicyMutationReader struct {
	FrontendExtensionReader
	mutate func()
}

type denyPublicComponentAdmission struct{}

func (denyPublicComponentAdmission) AdmitPublicComponent(Extension, ManifestComponent) bool {
	return false
}

func (r *publicPagePolicyMutationReader) Get(ctx context.Context, id string) (Extension, error) {
	extension, err := r.FrontendExtensionReader.Get(ctx, id)
	if err == nil && r.mutate != nil {
		mutate := r.mutate
		r.mutate = nil
		mutate()
	}
	return extension, err
}
