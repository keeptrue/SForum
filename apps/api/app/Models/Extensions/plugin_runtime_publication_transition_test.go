package extensions

import (
	"errors"
	"strings"
	"testing"
)

func TestTransitionPluginRuntimeDesiredMembersDirections(t *testing.T) {
	unrelated := PluginRuntimeMember{
		ExtensionID: "other.plugin", ExtensionVersionID: 50,
		ExtensionVersion: "5.0.0", PackageDigest: strings.Repeat("f", 64),
	}
	source := transitionFixturePlugin(t, "demo.plugin", 10, "1.0.0", strings.Repeat("a", 64), "backend/plugin")
	target := transitionFixturePlugin(t, "demo.plugin", 11, "1.1.0", strings.Repeat("b", 64), "backend/plugin")
	staticTarget := transitionFixturePlugin(t, "demo.plugin", 12, "1.2.0", strings.Repeat("c", 64), "")
	staticSource := transitionFixturePlugin(t, "demo.plugin", 9, "0.9.0", strings.Repeat("d", 64), "")

	t.Run("enable executable from empty", func(t *testing.T) {
		next, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, transitionMember(target))
	})

	t.Run("upgrade executable preserves unrelated", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(source)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &source, Target: target, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated, transitionMember(target))
		if latest[0] != unrelated || latest[1] != transitionMember(source) {
			t.Fatalf("transition mutated caller latest: %#v", latest)
		}
	})

	t.Run("rollback executable preserves unrelated", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(target)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &target, Target: source, Activate: true, Reason: PluginRuntimePublicationRollback,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated, transitionMember(source))
	})

	t.Run("disable removes only target", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(source)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &source, Target: source, Activate: false, Reason: PluginRuntimePublicationDisable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated)
	})

	t.Run("uninstall same as disable", func(t *testing.T) {
		latest := []PluginRuntimeMember{transitionMember(source)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &source, Target: source, Activate: false, Reason: PluginRuntimePublicationUninstall,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next)
	})

	t.Run("executable to static removes member", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(source)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &source, Target: staticTarget, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated)
	})

	t.Run("static to executable adds member", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &staticSource, Target: target, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated, transitionMember(target))
	})

	t.Run("declaration only never becomes member", func(t *testing.T) {
		next, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Target: staticTarget, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next)
	})
}

func TestTransitionPluginRuntimeDesiredMembersIdempotentReplay(t *testing.T) {
	target := transitionFixturePlugin(t, "demo.plugin", 11, "1.1.0", strings.Repeat("b", 64), "backend/plugin")
	staticTarget := transitionFixturePlugin(t, "demo.plugin", 12, "1.2.0", strings.Repeat("c", 64), "")
	unrelated := PluginRuntimeMember{
		ExtensionID: "other.plugin", ExtensionVersionID: 50,
		ExtensionVersion: "5.0.0", PackageDigest: strings.Repeat("f", 64),
	}

	t.Run("already exact target", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(target)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated, transitionMember(target))
	})

	t.Run("already absent for disable", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &target, Target: target, Activate: false, Reason: PluginRuntimePublicationDisable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated)
	})

	t.Run("exact target disable without redundant source", func(t *testing.T) {
		latest := []PluginRuntimeMember{unrelated, transitionMember(target)}
		next, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Target: target, Activate: false, Reason: PluginRuntimePublicationDisable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next, unrelated)
	})

	t.Run("already absent for static activate", func(t *testing.T) {
		next, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Target: staticTarget, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertTransitionMembers(t, next)
	})
}

func TestTransitionPluginRuntimeDesiredMembersReasonDirection(t *testing.T) {
	target := transitionFixturePlugin(t, "demo.plugin", 11, "1.1.0", strings.Repeat("b", 64), "backend/plugin")

	tests := []struct {
		name       string
		reason     PluginRuntimePublicationReason
		activate   bool
		wantReject bool
	}{
		{name: "enable requires activate", reason: PluginRuntimePublicationEnable, activate: false, wantReject: true},
		{name: "upgrade requires activate", reason: PluginRuntimePublicationUpgrade, activate: false, wantReject: true},
		{name: "rollback requires activate", reason: PluginRuntimePublicationRollback, activate: false, wantReject: true},
		{name: "disable requires deactivate", reason: PluginRuntimePublicationDisable, activate: true, wantReject: true},
		{name: "uninstall requires deactivate", reason: PluginRuntimePublicationUninstall, activate: true, wantReject: true},
		{name: "startup reconcile forbidden", reason: PluginRuntimePublicationStartupReconcile, activate: true, wantReject: true},
		{name: "recovery forbidden", reason: PluginRuntimePublicationRecovery, activate: true, wantReject: true},
		{name: "unknown reason forbidden", reason: "not-a-reason", activate: true, wantReject: true},
		{name: "enable with activate", reason: PluginRuntimePublicationEnable, activate: true, wantReject: false},
		{name: "disable with deactivate", reason: PluginRuntimePublicationDisable, activate: false, wantReject: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
				Target: target, Activate: test.activate, Reason: test.reason,
			})
			if test.wantReject {
				if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("negative actor", func(t *testing.T) {
		if err := validatePluginRuntimePublicationTransition(PluginRuntimePublicationTransition{
			Target: target, Activate: true, Reason: PluginRuntimePublicationEnable, ActorUserID: -1,
		}); !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("upgrade requires source", func(t *testing.T) {
		_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Target: target, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("upgrade source must differ", func(t *testing.T) {
		_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Source: &target, Target: target, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("enable source must be exact target", func(t *testing.T) {
		source := target
		source.Version = "0.9.0"
		source.Manifest.Version = source.Version
		source.ActiveVersionID--
		source.PackageDigest = strings.Repeat("a", 64)
		_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Source: &source, Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestTransitionPluginRuntimeDesiredMembersConflicts(t *testing.T) {
	source := transitionFixturePlugin(t, "demo.plugin", 10, "1.0.0", strings.Repeat("a", 64), "backend/plugin")
	target := transitionFixturePlugin(t, "demo.plugin", 11, "1.1.0", strings.Repeat("b", 64), "backend/plugin")
	stale := transitionFixturePlugin(t, "demo.plugin", 99, "9.9.9", strings.Repeat("e", 64), "backend/plugin")
	other := transitionFixturePlugin(t, "other.plugin", 20, "2.0.0", strings.Repeat("c", 64), "backend/plugin")

	t.Run("stale source member", func(t *testing.T) {
		latest := []PluginRuntimeMember{transitionMember(stale)}
		_, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Source: &source, Target: target, Activate: true, Reason: PluginRuntimePublicationUpgrade,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("member present without matching source", func(t *testing.T) {
		latest := []PluginRuntimeMember{transitionMember(stale)}
		_, err := TransitionPluginRuntimeDesiredMembers(latest, PluginRuntimePublicationTransition{
			Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("source extension id mismatch", func(t *testing.T) {
		_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Source: &other, Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("duplicate previous set", func(t *testing.T) {
		member := transitionMember(source)
		_, err := TransitionPluginRuntimeDesiredMembers(
			[]PluginRuntimeMember{member, member},
			PluginRuntimePublicationTransition{
				Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
			},
		)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("invalid previous set member", func(t *testing.T) {
		_, err := TransitionPluginRuntimeDesiredMembers(
			[]PluginRuntimeMember{{
				ExtensionID: "bad.plugin", ExtensionVersionID: 0,
				ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
			}},
			PluginRuntimePublicationTransition{
				Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
			},
		)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestTransitionPluginRuntimeDesiredMembersRejectsInvalidArtifacts(t *testing.T) {
	valid := transitionFixturePlugin(t, "demo.plugin", 11, "1.1.0", strings.Repeat("b", 64), "backend/plugin")

	tests := []struct {
		name   string
		mutate func(*Extension)
	}{
		{
			name: "theme type",
			mutate: func(extension *Extension) {
				extension.Type = TypeTheme
				extension.Manifest.Type = TypeTheme
			},
		},
		{
			name: "missing active version id",
			mutate: func(extension *Extension) {
				extension.ActiveVersionID = 0
			},
		},
		{
			name: "manifest version mismatch",
			mutate: func(extension *Extension) {
				extension.Manifest.Version = "9.0.0"
			},
		},
		{
			name: "manifest id mismatch",
			mutate: func(extension *Extension) {
				extension.Manifest.ID = "other.plugin"
			},
		},
		{
			name: "invalid package digest",
			mutate: func(extension *Extension) {
				extension.PackageDigest = "not-a-digest"
			},
		},
		{
			name: "untrimmed extension id",
			mutate: func(extension *Extension) {
				extension.ID = " demo.plugin"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := valid
			test.mutate(&target)
			_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
				Target: target, Activate: true, Reason: PluginRuntimePublicationEnable,
			})
			if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	t.Run("invalid source artifact", func(t *testing.T) {
		source := valid
		source.ActiveVersionID = 0
		_, err := TransitionPluginRuntimeDesiredMembers(nil, PluginRuntimePublicationTransition{
			Source: &source, Target: valid, Activate: true, Reason: PluginRuntimePublicationEnable,
		})
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("error=%v", err)
		}
	})
}

func transitionFixturePlugin(
	t *testing.T,
	id string,
	versionID int64,
	version, digest, backendEntry string,
) Extension {
	t.Helper()
	manifest := Manifest{
		ManifestVersion: 3,
		ID:              id, Name: "Transition Fixture", Description: "Plugin runtime transition fixture.",
		URL: "https://example.com/transition-fixture", Author: ManifestAuthor{Name: "SForum"},
		Version: version, Type: TypePlugin, SForumVersion: ">=0.1.0",
	}
	if backendEntry != "" {
		backendDigest := strings.Repeat("e", 64)
		manifest.Backend = ManifestBackend{
			Entry: backendEntry, RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			HostAPIVersion: "sforum.host@2", Digest: backendDigest,
		}
		manifest.PackageFiles = []ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: backendEntry, Digest: backendDigest,
		}}
	}
	return Extension{
		ID: id, Name: manifest.Name, Version: version, Type: TypePlugin,
		Status: StatusEnabled, Manifest: manifest, PackageDigest: digest,
		ActiveVersionID: versionID,
	}
}

func transitionMember(extension Extension) PluginRuntimeMember {
	return PluginRuntimeMember{
		ExtensionID:        extension.ID,
		ExtensionVersionID: extension.ActiveVersionID,
		ExtensionVersion:   extension.Version,
		PackageDigest:      extension.PackageDigest,
	}
}

func assertTransitionMembers(t *testing.T, got []PluginRuntimeMember, want ...PluginRuntimeMember) {
	t.Helper()
	canonicalWant, wantDigest, err := canonicalPluginRuntimeMembers(want)
	if err != nil {
		t.Fatal(err)
	}
	canonicalGot, gotDigest, err := canonicalPluginRuntimeMembers(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest || len(canonicalGot) != len(canonicalWant) {
		t.Fatalf("members=%+v want=%+v", got, want)
	}
	for index := range canonicalWant {
		if canonicalGot[index] != canonicalWant[index] {
			t.Fatalf("member[%d]=%+v want %+v", index, canonicalGot[index], canonicalWant[index])
		}
	}
}
