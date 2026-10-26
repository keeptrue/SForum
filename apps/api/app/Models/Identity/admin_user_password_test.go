package identity

import (
	"strings"
	"testing"
)

func TestGenerateCompliantPasswordHonorsPolicy(t *testing.T) {
	t.Parallel()

	policies := []PasswordPolicy{
		RecommendedPasswordPolicy(),
		{MinLength: 12, MaxLength: 12},
		{
			MinLength: 20, MaxLength: 64,
			RequireLowercase: true, RequireUppercase: true,
			RequireNumber: true, RequireSymbol: true,
		},
		{MinLength: 8, MaxLength: 8, RequireSymbol: true},
	}

	for _, policy := range policies {
		password, err := GenerateCompliantPassword(policy)
		if err != nil {
			t.Fatalf("policy %+v: %v", policy, err)
		}
		if fields := policy.Validate(password); len(fields) > 0 {
			t.Fatalf("policy %+v rejected generated password %q: %+v", policy, password, fields)
		}
		normalized := policy.Normalized()
		if length := len([]rune(password)); length < normalized.MinLength || length > normalized.MaxLength {
			t.Fatalf("password length %d outside policy %+v", length, normalized)
		}
	}
}

func TestGenerateCompliantPasswordIncludesRequiredClasses(t *testing.T) {
	t.Parallel()
	policy := PasswordPolicy{
		MinLength: 16, MaxLength: 128,
		RequireLowercase: true, RequireUppercase: true,
		RequireNumber: true, RequireSymbol: true,
	}
	for attempt := 0; attempt < 20; attempt++ {
		password, err := GenerateCompliantPassword(policy)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if !strings.ContainsAny(password, passwordLowerAlphabet) ||
			!strings.ContainsAny(password, passwordUpperAlphabet) ||
			!strings.ContainsAny(password, passwordNumberAlphabet) ||
			!strings.ContainsAny(password, passwordSymbolAlphabet) {
			t.Fatalf("generated password misses a required class: %q", password)
		}
	}
}
