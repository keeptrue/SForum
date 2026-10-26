package bootstrap

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
)

const queryRegistryCursorKeyDomain = "sforum/query-registry/cursor-hmac/v1"

var errProductionQueryRegistrySecret = errors.New("bootstrap: Query Registry cursor secret is invalid")

// deriveQueryRegistryCursorSecret binds portable cursors to one installation
// without reusing the session hash key directly or logging either input.
func deriveQueryRegistryCursorSecret(sessionHashSecret, installationID string) ([]byte, error) {
	if sessionHashSecret == "" || sessionHashSecret != strings.TrimSpace(sessionHashSecret) ||
		!installationidentity.Valid(installationID) {
		return nil, errProductionQueryRegistrySecret
	}
	salt, err := hex.DecodeString(installationID)
	if err != nil {
		return nil, errProductionQueryRegistrySecret
	}
	key, err := hkdf.Key(sha256.New, []byte(sessionHashSecret), salt, queryRegistryCursorKeyDomain, 32)
	if err != nil || len(key) != 32 {
		return nil, errProductionQueryRegistrySecret
	}
	return key, nil
}
