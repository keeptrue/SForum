package bootstrap

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
)

const identityUserFieldDigestKeyDomain = "sforum/identity/user-field-value-digest/v1"

var errIdentityUserFieldDigestKey = errors.New("bootstrap: identity user-field digest key is invalid")

// deriveIdentityUserFieldDigestKey binds value digests to one installation
// without reusing the session hash key directly.
func deriveIdentityUserFieldDigestKey(sessionHashSecret, installationID string) ([]byte, error) {
	if sessionHashSecret == "" || sessionHashSecret != strings.TrimSpace(sessionHashSecret) ||
		!installationidentity.Valid(installationID) {
		return nil, errIdentityUserFieldDigestKey
	}
	salt, err := hex.DecodeString(installationID)
	if err != nil {
		return nil, errIdentityUserFieldDigestKey
	}
	key, err := hkdf.Key(sha256.New, []byte(sessionHashSecret), salt, identityUserFieldDigestKeyDomain, 32)
	if err != nil || len(key) != 32 {
		return nil, errIdentityUserFieldDigestKey
	}
	return key, nil
}
