package regioncatalog_test

import (
	"testing"

	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	regioncatalog "github.com/keeptrue/sforum/apps/api/app/Support/RegionCatalog"
)

func TestStandardRegionsAreWellFormed(t *testing.T) {
	seen := map[string]struct{}{}
	for _, region := range regioncatalog.StandardRegions() {
		if region.ID == "" || region.ContractVersion == "" || region.Kind == "" {
			t.Fatalf("region %+v missing required fields", region)
		}
		if _, dup := seen[region.ID]; dup {
			t.Fatalf("duplicate region id %q", region.ID)
		}
		seen[region.ID] = struct{}{}
		if !region.Multiple {
			t.Fatalf("standard region %q must accept multiple placements", region.ID)
		}
	}
}

func TestPageRegionMatrixReferencesCatalogPages(t *testing.T) {
	catalog := map[string]pages.PageDefinition{}
	for _, definition := range pages.Catalog() {
		catalog[definition.ID] = definition
	}
	for _, pageID := range regioncatalog.Pages() {
		definition, ok := catalog[pageID]
		if !ok {
			t.Fatalf("region matrix references unknown page %q", pageID)
		}
		if definition.Access == pages.AccessModeration {
			t.Fatalf("region matrix must not expose moderation page %q", pageID)
		}
		if definition.Virtual {
			t.Fatalf("region matrix must not expose virtual page %q", pageID)
		}
		regions := regioncatalog.PageRegions(pageID)
		if len(regions) == 0 {
			t.Fatalf("page %q listed without regions", pageID)
		}
		for _, region := range regions {
			if _, found := regioncatalog.FindRegion(region.ID); !found {
				t.Fatalf("page %q references unknown region %q", pageID, region.ID)
			}
			if !regioncatalog.Valid(pageID, region.ID) {
				t.Fatalf("Valid(%q, %q) should be true", pageID, region.ID)
			}
		}
	}
}

func TestValidRejectsUnknownPairs(t *testing.T) {
	if regioncatalog.Valid("forum.home", "header") {
		t.Fatal("unknown region must be rejected")
	}
	if regioncatalog.Valid("forum.notifications", regioncatalog.RegionSidebar) {
		t.Fatal("notifications has no sidebar region")
	}
	if regioncatalog.Valid("admin.dashboard", regioncatalog.RegionContentBefore) {
		t.Fatal("unknown page must be rejected")
	}
	if regioncatalog.Valid("", "") {
		t.Fatal("empty pair must be rejected")
	}
}

func TestPageRegionsUnknownPageIsEmpty(t *testing.T) {
	if got := regioncatalog.PageRegions("forum.unknown"); len(got) != 0 {
		t.Fatalf("unknown page should have no regions, got %d", len(got))
	}
}

func TestEveryMatrixRegionIsDeclared(t *testing.T) {
	if missing := regioncatalog.UndeclaredRegions(); len(missing) != 0 {
		t.Fatalf("matrix references undeclared regions: %v", missing)
	}
}

func TestEveryDeclaredRegionIsReachableFromSomePage(t *testing.T) {
	// 声明一个任何页面都不暴露的区域等于对作者撒谎：它永远拿不到内容。
	used := map[string]struct{}{}
	for _, pageID := range regioncatalog.Pages() {
		for _, regionID := range regioncatalog.PageRegionIDs(pageID) {
			used[regionID] = struct{}{}
		}
	}
	for _, region := range regioncatalog.StandardRegions() {
		if _, ok := used[region.ID]; !ok {
			t.Fatalf("region %q is declared but no page exposes it", region.ID)
		}
	}
}

func TestPageRegionIDsMatchesPageRegionsOrder(t *testing.T) {
	for _, pageID := range regioncatalog.Pages() {
		ids := regioncatalog.PageRegionIDs(pageID)
		regions := regioncatalog.PageRegions(pageID)
		if len(ids) != len(regions) {
			t.Fatalf("page %q id/region length mismatch: %d != %d", pageID, len(ids), len(regions))
		}
		for index := range ids {
			if ids[index] != regions[index].ID {
				t.Fatalf("page %q order mismatch at %d: %q != %q", pageID, index, ids[index], regions[index].ID)
			}
		}
	}
}

func TestPageRegionIDsReturnsACopy(t *testing.T) {
	before := regioncatalog.PageRegionIDs("forum.home")
	before[0] = "mutated"
	after := regioncatalog.PageRegionIDs("forum.home")
	if after[0] == "mutated" {
		t.Fatal("PageRegionIDs must not expose the internal matrix slice")
	}
}
