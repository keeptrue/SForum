package bootstrap

import (
	"bytes"
	"testing"

	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
)

func TestDeriveIdentityUserFieldDigestKey(t *testing.T) {
	installationID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if !installationidentity.Valid(installationID) {
		t.Fatal("fixture installation id must be valid")
	}
	first, err := deriveIdentityUserFieldDigestKey("session-secret", installationID)
	if err != nil || len(first) != 32 {
		t.Fatalf("first key=%x err=%v", first, err)
	}
	second, err := deriveIdentityUserFieldDigestKey("session-secret", installationID)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("key must be deterministic: %x vs %x err=%v", first, second, err)
	}
	other, err := deriveIdentityUserFieldDigestKey("other-secret", installationID)
	if err != nil || bytes.Equal(first, other) {
		t.Fatalf("different secret must change key")
	}
	if _, err := deriveIdentityUserFieldDigestKey("", installationID); err == nil {
		t.Fatal("empty secret must fail")
	}
	if _, err := deriveIdentityUserFieldDigestKey("session-secret", "not-hex"); err == nil {
		t.Fatal("invalid installation id must fail")
	}
}
