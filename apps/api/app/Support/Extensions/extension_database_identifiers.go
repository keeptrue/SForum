package extensionsruntime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"

	extensiondatabase "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionDatabase"
)

const (
	extensionDatabaseNamespace       = "sforum_ext"
	extensionDatabaseSlugBytes       = 24
	extensionDatabaseHashHexBytes    = 20
	postgresIdentifierMaximumBytes   = 63
	extensionDatabaseLockKeyDomain   = "sforum:extension-database:"
	extensionDatabasePlanRoleDomain  = "sforum:extension-database:migration-role:"
	extensionDatabaseLeaseRoleDomain = "sforum:extension-database:runtime-lease-role:"
)

var (
	ErrExtensionDatabaseIdentifier = extensiondatabase.ErrIdentifier
	extensionDatabaseIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	postgresSafeIdentifierPattern  = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

// ExtensionDatabaseIdentifiers are Host-owned physical PostgreSQL names. A
// manifest may describe a logical schema or role, but it cannot select a core
// or another extension's physical identity.
type ExtensionDatabaseIdentifiers = extensiondatabase.Identifiers

func ExtensionDatabaseIdentifiersFor(extensionID string) (ExtensionDatabaseIdentifiers, error) {
	identifiers, err := extensiondatabase.ResolveIdentifiers(extensionID)
	if err != nil {
		return ExtensionDatabaseIdentifiers{}, ErrExtensionDatabaseIdentifier
	}
	if !identifiers.Valid() {
		return ExtensionDatabaseIdentifiers{}, ErrExtensionDatabaseIdentifier
	}
	return identifiers, nil
}

func ExtensionDatabaseMigrationRoleFor(extensionID string, planDigest string) (string, error) {
	name, err := extensiondatabase.ResolveMigrationRole(extensionID, planDigest)
	if err != nil {
		return "", ErrExtensionDatabaseIdentifier
	}
	return name, nil
}

func ExtensionDatabaseRuntimeLeaseRoleFor(extensionID string, runtimeInstanceID string, leaseID string) (string, error) {
	name, err := extensiondatabase.ResolveRuntimeLeaseRole(extensionID, runtimeInstanceID, leaseID)
	if err != nil {
		return "", ErrExtensionDatabaseIdentifier
	}
	return name, nil
}

func extensionDatabasePhysicalName(kind string, slug string, hash string) string {
	return extensionDatabaseNamespace + "_" + kind + "_" + slug + "_" + hash[:extensionDatabaseHashHexBytes]
}

func extensionDatabaseSlug(extensionID string, maximum int) string {
	var builder strings.Builder
	builder.Grow(len(extensionID))
	previousSeparator := false
	for _, char := range extensionID {
		separator := char == '.' || char == '-' || char == '_'
		if separator {
			if builder.Len() > 0 && !previousSeparator {
				builder.WriteByte('_')
			}
			previousSeparator = true
			continue
		}
		builder.WriteRune(char)
		previousSeparator = false
	}
	value := strings.Trim(builder.String(), "_")
	if len(value) > maximum {
		value = strings.TrimRight(value[:maximum], "_")
	}
	if value == "" {
		return "extension"
	}
	return value
}

func extensionDatabaseHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func extensionDatabaseAdvisoryKey(extensionID string) int64 {
	digest := sha256.Sum256([]byte(extensionDatabaseLockKeyDomain + extensionID))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func validPostgresIdentifier(value string) bool {
	return len(value) <= postgresIdentifierMaximumBytes && postgresSafeIdentifierPattern.MatchString(value)
}

func validPostgresCatalogName(value string) bool {
	return value != "" && len(value) <= postgresIdentifierMaximumBytes && !strings.ContainsRune(value, '\x00')
}
