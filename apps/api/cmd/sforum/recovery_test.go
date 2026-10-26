package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestRootCommandIncludesOutOfBandRecoveryCommands(t *testing.T) {
	root := newRootCommand()
	for _, path := range []string{"extension list", "extension disable", "extension disable-all", "extension quarantine"} {
		command, _, err := root.Find(strings.Fields(path))
		if err != nil || command == nil {
			t.Fatalf("find %q: command=%v err=%v", path, command, err)
		}
	}
}

func TestProtectedQuarantineRequiresExactArtifactFlags(t *testing.T) {
	command := newExtensionRecoveryQuarantineCommand()
	command.SetArgs([]string{"sforum.auth-github"})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "--expect-version and --expect-digest are required") {
		t.Fatalf("quarantine flags error=%v", err)
	}
}

func TestProtectedQuarantineRejectsNonCanonicalDigestBeforeDatabaseAccess(t *testing.T) {
	command := newExtensionRecoveryQuarantineCommand()
	command.SetArgs([]string{
		"sforum.auth-github",
		"--expect-version", "1.0.0",
		"--expect-digest", strings.Repeat("A", 64),
	})
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "64-character lowercase SHA-256") {
		t.Fatalf("quarantine digest error=%v", err)
	}
}

func TestRecoveryRequiresOnlyExplicitDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	called := false
	err := withRecoveryRepository(context.Background(), "", func(recoveryRepository) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "database url is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("recovery callback ran without a database connection")
	}
}

func TestCommitRecoveryPublicationRecoversUnknownCommitByExactRevision(t *testing.T) {
	commitErr := errors.New("commit transport failed")
	expected := recoveryPublicationFixture()
	committer := &fakeRecoveryCommitter{err: commitErr}
	reader := &fakeRecoveryPublicationReader{publication: expected}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := commitRecoveryPublication(ctx, committer, reader, expected); err != nil {
		t.Fatal(err)
	}
	if committer.calls != 1 || reader.calls != 1 || reader.revision != expected.Revision {
		t.Fatalf("committer=%+v reader=%+v", committer, reader)
	}
}

func TestCommitRecoveryPublicationReportsRollbackAndEvidenceMismatch(t *testing.T) {
	commitErr := errors.New("commit rolled back")
	expected := recoveryPublicationFixture()

	t.Run("missing revision", func(t *testing.T) {
		missingErr := extensions.ErrPluginRuntimePublicationNotFound
		err := commitRecoveryPublication(
			t.Context(),
			&fakeRecoveryCommitter{err: commitErr},
			&fakeRecoveryPublicationReader{err: missingErr},
			expected,
		)
		if !errors.Is(err, commitErr) || !errors.Is(err, missingErr) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("different immutable evidence", func(t *testing.T) {
		different := expected
		different.MembersDigest = strings.Repeat("f", 64)
		err := commitRecoveryPublication(
			t.Context(),
			&fakeRecoveryCommitter{err: commitErr},
			&fakeRecoveryPublicationReader{publication: different},
			expected,
		)
		if !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "differs") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestCommitRecoveryPublicationSkipsReadbackAfterCertainCommit(t *testing.T) {
	reader := &fakeRecoveryPublicationReader{err: errors.New("must not read")}
	if err := commitRecoveryPublication(
		t.Context(), &fakeRecoveryCommitter{}, reader, recoveryPublicationFixture(),
	); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls=%d", reader.calls)
	}
}

func recoveryPublicationFixture() extensions.PluginRuntimePublication {
	member := extensions.PluginRuntimeMember{
		ExtensionID: "fixture.plugin", ExtensionVersionID: 41,
		ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
	}
	digest, err := extensions.PluginRuntimeMembersDigest([]extensions.PluginRuntimeMember{member})
	if err != nil {
		panic(err)
	}
	return extensions.PluginRuntimePublication{
		Revision: 9, MemberCount: 1, MembersDigest: digest,
		Members: []extensions.PluginRuntimeMember{member},
		Reason:  extensions.PluginRuntimePublicationRecovery, CreatedAt: time.Unix(100, 0),
	}
}

type fakeRecoveryCommitter struct {
	err   error
	calls int
}

func (f *fakeRecoveryCommitter) Commit(context.Context) error {
	f.calls++
	return f.err
}

type fakeRecoveryPublicationReader struct {
	publication extensions.PluginRuntimePublication
	err         error
	calls       int
	revision    int64
}

func (f *fakeRecoveryPublicationReader) PluginRuntimePublicationByRevision(
	ctx context.Context,
	revision int64,
) (extensions.PluginRuntimePublication, error) {
	f.calls++
	f.revision = revision
	if ctx.Err() != nil {
		return extensions.PluginRuntimePublication{}, ctx.Err()
	}
	return f.publication, f.err
}
