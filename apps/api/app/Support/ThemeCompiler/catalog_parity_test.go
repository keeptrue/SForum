package themecompiler_test

import (
	"testing"

	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestCorePageViewModelsMatchAuthoritativePageCatalog(t *testing.T) {
	want := make(map[string]string)
	for _, page := range pages.Catalog() {
		if previous, exists := want[page.ID]; exists {
			t.Fatalf("duplicate Page Catalog identity %s (%s, %s)", page.ID, previous, page.ContractVersion)
		}
		want[page.ID] = page.ContractVersion
	}
	for _, schema := range themecompiler.CorePageViewModelRegistry().Catalog() {
		contract, exists := want[schema.PageID]
		if !exists {
			t.Fatalf("ViewModel registry has non-catalog page %s", schema.PageID)
		}
		if contract != schema.SchemaVersion {
			t.Fatalf("ViewModel contract drift for %s: registry=%s catalog=%s", schema.PageID, schema.SchemaVersion, contract)
		}
		delete(want, schema.PageID)
	}
	if len(want) != 0 {
		t.Fatalf("Page Catalog identities missing ViewModels: %#v", want)
	}
}
