package extensionmanifest

import (
	"errors"
	"testing"
)

func TestCheckSForumVersionEvaluatesAgainstCoreVersion(t *testing.T) {
	manifest := Manifest{ID: "demo.plugin", SForumVersion: "^1.0.0"}

	if err := CheckSForumVersion(manifest, "1.4.2"); err != nil {
		t.Fatalf("compatible core rejected: %v", err)
	}
	err := CheckSForumVersion(manifest, "3.0.16")
	if !errors.Is(err, ErrSForumVersionIncompatible) {
		t.Fatalf("expected incompatible, got %v", err)
	}
	var incompatible *SForumVersionIncompatibleError
	if !errors.As(err, &incompatible) || incompatible.Required != "^1.0.0" || incompatible.CoreVersion != "3.0.16" {
		t.Fatalf("incompatible detail = %#v", incompatible)
	}
}

func TestCheckSForumVersionReportsUnverifiableInsteadOfRejecting(t *testing.T) {
	manifest := Manifest{ID: "demo.plugin", SForumVersion: ">=0.1.0"}
	for _, core := range []string{"v3.0.16", "", "not-semver"} {
		err := CheckSForumVersion(manifest, core)
		if !errors.Is(err, ErrSForumVersionUnverifiable) {
			t.Fatalf("core %q: expected unverifiable, got %v", core, err)
		}
		if errors.Is(err, ErrSForumVersionIncompatible) {
			t.Fatalf("core %q: unverifiable must not be reported as incompatible", core)
		}
	}
}

func TestCheckSForumVersionAcceptsPrereleaseCoresOnTheSameReleaseLine(t *testing.T) {
	// Masterminds 默认不匹配预发布版本；预发布 core 必须按发行基线再求值一次，
	// 否则每次 alpha/beta 都会让所有第三方包无法安装。
	compatible := Manifest{ID: "demo.plugin", SForumVersion: ">=3.0.0"}
	if err := CheckSForumVersion(compatible, "3.0.13-alpha.1"); err != nil {
		t.Fatalf("prerelease core on the same line rejected: %v", err)
	}
	// 仍然不能跨版本线放行。
	if err := CheckSForumVersion(Manifest{ID: "demo.plugin", SForumVersion: "^2.0.0"}, "3.0.13-alpha.1"); !errors.Is(err, ErrSForumVersionIncompatible) {
		t.Fatalf("cross-line prerelease must stay incompatible: %v", err)
	}
}
