package extensionsruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type coreCompatibilityTestQueryer struct {
	current string
	target  string
	status  string
	err     error
}

func (q coreCompatibilityTestQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return coreCompatibilityTestRow(q)
}

type coreCompatibilityTestRow coreCompatibilityTestQueryer

func (r coreCompatibilityTestRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*destinations[0].(*string) = r.current
	*destinations[1].(*string) = r.target
	*destinations[2].(*string) = r.status
	return nil
}

func TestExtensionDatabaseCoreCompatibilityRequiresReadyMatchingHostAndConstraint(t *testing.T) {
	highRisk := []string{extensionmanifest.DatabaseGrantRawCore}
	declaration := extensions.ManifestDatabase{CoreCompatibility: ">=1.0.0 <2.0.0"}
	tests := []struct {
		name        string
		state       coreCompatibilityTestQueryer
		host        string
		declaration extensions.ManifestDatabase
		wantError   bool
	}{
		{
			name:  "ready exact host and compatible declaration",
			state: coreCompatibilityTestQueryer{current: "1.0.0", target: "1.0.0", status: "ready"},
			host:  "1.0.0", declaration: declaration,
		},
		{
			name:  "migration in progress",
			state: coreCompatibilityTestQueryer{current: "1.0.0", target: "2.0.0", status: "migrating"},
			host:  "1.0.0", declaration: declaration, wantError: true,
		},
		{
			name:  "older rolling node",
			state: coreCompatibilityTestQueryer{current: "2.0.0", target: "2.0.0", status: "ready"},
			host:  "1.0.0", declaration: extensions.ManifestDatabase{CoreCompatibility: ">=1.0.0 <3.0.0"}, wantError: true,
		},
		{
			name:  "incompatible declaration",
			state: coreCompatibilityTestQueryer{current: "1.0.0", target: "1.0.0", status: "ready"},
			host:  "1.0.0", declaration: extensions.ManifestDatabase{CoreCompatibility: ">=2.0.0 <3.0.0"}, wantError: true,
		},
		{
			name:  "missing durable state",
			state: coreCompatibilityTestQueryer{err: pgx.ErrNoRows},
			host:  "1.0.0", declaration: declaration, wantError: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateExtensionDatabaseCoreCompatibilityForHost(
				context.Background(), testCase.state, testCase.declaration, highRisk, testCase.host,
			)
			if testCase.wantError && !errors.Is(err, ErrExtensionDatabaseCoreIncompatible) {
				t.Fatalf("compatibility error = %v", err)
			}
			if !testCase.wantError && err != nil {
				t.Fatalf("compatible authority rejected: %v", err)
			}
		})
	}
}

func TestExtensionDatabaseCoreCompatibilityDoesNotGateScopedPowers(t *testing.T) {
	err := validateExtensionDatabaseCoreCompatibilityForHost(
		context.Background(), coreCompatibilityTestQueryer{err: errors.New("must not query")},
		extensions.ManifestDatabase{}, []string{extensionmanifest.DatabaseGrantOwnSchema}, "1.0.0",
	)
	if err != nil {
		t.Fatalf("scoped database power used Core compatibility fence: %v", err)
	}
}
