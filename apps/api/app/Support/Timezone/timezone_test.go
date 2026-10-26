package timezone_test

import (
	"testing"

	timezone "github.com/keeptrue/sforum/apps/api/app/Support/Timezone"
)

func TestLoadAcceptsIANAZones(t *testing.T) {
	for _, name := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Australia/Sydney"} {
		location, err := timezone.Load(name)
		if err != nil {
			t.Fatalf("Load(%q) returned error: %v", name, err)
		}
		if location == nil {
			t.Fatalf("Load(%q) returned nil location", name)
		}
		if got := location.String(); got != name {
			t.Fatalf("Load(%q).String() = %q, want %q", name, got, name)
		}
	}
}

func TestLoadTrimsWhitespace(t *testing.T) {
	location, err := timezone.Load("\tAsia/Tokyo\n")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if location.String() != "Asia/Tokyo" {
		t.Fatalf("expected Asia/Tokyo, got %q", location.String())
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	for _, name := range []string{"Not/AZone", "GMT+8"} {
		if _, err := timezone.Load(name); err == nil {
			t.Fatalf("expected error for %q", name)
		}
	}
}

// 空时区与 time.LoadLocation 语义一致：解析为 UTC。需要显式时区的调用方
// （例如站点选项）必须自己先拒绝空值。
func TestLoadEmptyMeansUTC(t *testing.T) {
	for _, name := range []string{"", "   "} {
		location, err := timezone.Load(name)
		if err != nil {
			t.Fatalf("Load(%q) returned error: %v", name, err)
		}
		if location.String() != "UTC" {
			t.Fatalf("Load(%q) = %q, want UTC", name, location.String())
		}
	}
}
