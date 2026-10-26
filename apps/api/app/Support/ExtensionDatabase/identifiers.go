package extensiondatabase

import (
	"errors"
	"regexp"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

var postgresIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

var ErrIdentifier = errors.New("extension database identifier input is invalid")

// Identifiers are Host-owned physical PostgreSQL names. Manifests cannot
// select Core or another extension's physical database identity.
type Identifiers struct {
	Schema      string
	OwnerRole   string
	RuntimeRole string
	LockKey     int64
}

// Valid reports whether the three physical identities are distinct, safe
// PostgreSQL identifiers. It deliberately does not accept manifest-provided
// names; callers must obtain the value through ResolveIdentifiers.
func (i Identifiers) Valid() bool {
	return i.Schema != i.OwnerRole && i.Schema != i.RuntimeRole && i.OwnerRole != i.RuntimeRole &&
		postgresIdentifierPattern.MatchString(i.Schema) &&
		postgresIdentifierPattern.MatchString(i.OwnerRole) &&
		postgresIdentifierPattern.MatchString(i.RuntimeRole)
}

func ResolveIdentifiers(extensionID string) (Identifiers, error) {
	shared, err := coreauthority.ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		return Identifiers{}, ErrIdentifier
	}
	return Identifiers{
		Schema: shared.Schema, OwnerRole: shared.OwnerRole,
		RuntimeRole: shared.RuntimeRole, LockKey: shared.LockKey,
	}, nil
}

func ResolveMigrationRole(extensionID, planDigest string) (string, error) {
	name, err := coreauthority.ExtensionDatabaseMigrationRoleFor(extensionID, planDigest)
	if err != nil {
		return "", ErrIdentifier
	}
	return name, nil
}

func ResolveRuntimeLeaseRole(extensionID, runtimeInstanceID, leaseID string) (string, error) {
	name, err := coreauthority.ExtensionDatabaseRuntimeLeaseRoleFor(extensionID, runtimeInstanceID, leaseID)
	if err != nil {
		return "", ErrIdentifier
	}
	return name, nil
}
