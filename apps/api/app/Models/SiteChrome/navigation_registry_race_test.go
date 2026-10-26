package sitechrome

import (
	"fmt"
	"sync"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

func TestSiteChromeConcurrentCompositionAndProviderSelection(t *testing.T) {
	service := NewService(newFakeStore())
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry).WithNavigationRuntime(&siteChromeRuntime{}, &siteChromeRuntime{})
	alpha := siteChromePublication("chrome.alpha", 'a')
	alphaReplace := navigationregistry.NavigationDeclaration{
		ID: "chrome.alpha.header.replace", ContractVersion: "chrome.alpha.header.replace@1",
		Kind: navigationregistry.NavigationKindHeader, Action: navigationregistry.ActionReplace,
		TargetID: navigationregistry.CoreHeaderNavigationID, Label: "Alpha",
	}
	alpha.Navigation = []navigationregistry.NavigationDeclaration{alphaReplace}
	beta := siteChromePublication("chrome.beta", 'b')
	betaReplace := navigationregistry.NavigationDeclaration{
		ID: "chrome.beta.header.replace", ContractVersion: "chrome.beta.header.replace@1",
		Kind: navigationregistry.NavigationKindHeader, Action: navigationregistry.ActionReplace,
		TargetID: navigationregistry.CoreHeaderNavigationID, Label: "Beta",
	}
	beta.Navigation = []navigationregistry.NavigationDeclaration{betaReplace}
	if _, err := registry.Publish(alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Publish(beta); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	problems := make(chan error, 1)
	report := func(err error) {
		select {
		case problems <- err:
		default:
		}
	}
	var group sync.WaitGroup
	group.Add(5)
	go func() {
		defer group.Done()
		<-start
		providers := []navigationregistry.ProviderRef{
			{ContributionID: alphaReplace.ID, Artifact: alpha.Artifact},
			{ContributionID: betaReplace.ID, Artifact: beta.Artifact},
		}
		for index := 0; index < 150; index++ {
			if index%3 == 2 {
				_, _, err := registry.ResetProvider(navigationregistry.ResetProviderRequest{
					ExpectedRevision: registry.Revision(), Family: navigationregistry.ProviderFamilyNavigation,
					TargetID: navigationregistry.CoreHeaderNavigationID,
				})
				if err != nil {
					report(fmt.Errorf("reset: %w", err))
					return
				}
				continue
			}
			_, err := registry.SelectProvider(navigationregistry.SelectProviderRequest{
				ExpectedRevision: registry.Revision(), Family: navigationregistry.ProviderFamilyNavigation,
				TargetID: navigationregistry.CoreHeaderNavigationID, Provider: providers[index%2],
			})
			if err != nil {
				report(fmt.Errorf("select: %w", err))
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		go func(reader int) {
			defer group.Done()
			<-start
			for index := 0; index < 300; index++ {
				view, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "en-US")
				if err != nil {
					report(fmt.Errorf("reader %d: %w", reader, err))
					return
				}
				if len(view.Headers) != 1 || view.Revision == 0 || view.Digest == "" || view.CacheKey == "" ||
					(view.Headers[0].Label != "Alpha" && view.Headers[0].Label != "Beta" && view.Headers[0].Label != "Header") {
					report(fmt.Errorf("reader %d partial view: %#v", reader, view))
					return
				}
			}
		}(reader)
	}
	close(start)
	group.Wait()
	select {
	case err := <-problems:
		t.Fatal(err)
	default:
	}
}
