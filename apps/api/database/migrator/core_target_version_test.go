package migrator

import (
	"errors"
	"testing"

	semver "github.com/Masterminds/semver/v3"
)

func TestResolveCoreTargetVersionDistinguishesPinnedFromDerived(t *testing.T) {
	original := coreCompatibilityDeclared
	t.Cleanup(func() { coreCompatibilityDeclared = original })

	coreCompatibilityDeclared = func() (string, bool) { return "1.0.0", false }
	if target, declared := resolveCoreTargetVersion(Config{}); target != "1.0.0" || declared {
		t.Fatalf("derived undeclared target = %q/%t, want 1.0.0/false", target, declared)
	}
	if target, declared := resolveCoreTargetVersion(Config{TargetCoreVersion: " 3.0.18 "}); target != "3.0.18" || !declared {
		t.Fatalf("pinned target = %q/%t, want 3.0.18/true", target, declared)
	}
	// 把探针值显式钉进来也仍然是声明：写了版本就是意图，护栏不能因为调用方
	// 少设一个 bool 就被绕过。
	if target, declared := resolveCoreTargetVersion(Config{TargetCoreVersion: "1.0.0"}); target != "1.0.0" || !declared {
		t.Fatalf("pinned sentinel target = %q/%t, want 1.0.0/true", target, declared)
	}

	coreCompatibilityDeclared = func() (string, bool) { return "3.0.18+10.gd1153068c.dirty", true }
	if target, declared := resolveCoreTargetVersion(Config{}); target != "3.0.18+10.gd1153068c.dirty" || !declared {
		t.Fatalf("derived declared target = %q/%t", target, declared)
	}
}

// 「不知道我是谁」不等于「我比库里低」：同一个 1.0.0 哨兵在未声明时不得触发
// 降级否决，而显式声明时仍必须被否决。这是护栏没有减弱的直接证据。
func TestValidateCoreMigrationTargetTreatsUnknownAsNotLower(t *testing.T) {
	target := semver.MustParse("1.0.0")

	migrated := coreRuntimeState{
		CurrentVersion: "3.0.18+4.ga4e91267b.dirty",
		TargetVersion:  "3.0.18+4.ga4e91267b.dirty",
		Status:         "ready",
	}
	if err := validateCoreMigrationTarget(migrated, target, false); err != nil {
		t.Fatalf("undeclared target was read as a downgrade: %v", err)
	}
	if err := validateCoreMigrationTarget(migrated, target, true); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("declared target skipped the migrated-version fence: %v", err)
	}

	pending := coreRuntimeState{
		CurrentVersion: "1.0.0",
		TargetVersion:  "3.0.18",
		Status:         "migrating",
	}
	if err := validateCoreMigrationTarget(pending, target, false); err != nil {
		t.Fatalf("undeclared target was read as older than a pending migration: %v", err)
	}
	if err := validateCoreMigrationTarget(pending, target, true); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("declared target skipped the pending-migration fence: %v", err)
	}

	// 结构校验描述的是行本身而不是构建身份，未声明时同样要报。
	for _, tc := range []struct {
		name  string
		state coreRuntimeState
	}{
		{"invalid status", coreRuntimeState{Status: "broken"}},
		{"invalid stored version", coreRuntimeState{CurrentVersion: "not-semver", Status: "ready"}},
		{"invalid pending version", coreRuntimeState{TargetVersion: "not-semver", Status: "migrating"}},
	} {
		if err := validateCoreMigrationTarget(tc.state, target, false); !errors.Is(err, ErrCoreAuthorityConflict) {
			t.Fatalf("%s error = %v, want ErrCoreAuthorityConflict", tc.name, err)
		}
	}
}
