package appearance_test

import (
	"testing"

	appearance "github.com/keeptrue/sforum/apps/api/app/Support/Appearance"
)

func TestNormalizeTheme(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{" Ocean_Blue ", "ocean_blue", true},
		{"custom:#4F46E5", "custom:#4f46e5", true},
		{"custom:not-a-color", "", false},
		{"neon", "", false},
	}
	for _, test := range tests {
		got, ok := appearance.NormalizeTheme(test.input)
		if got != test.want || ok != test.ok {
			t.Fatalf("NormalizeTheme(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestNormalizeLightBackground(t *testing.T) {
	t.Parallel()
	if got, ok := appearance.NormalizeLightBackground(" Morning_Apricot "); !ok || got != "morning_apricot" {
		t.Fatalf("unexpected normalized background %q, %v", got, ok)
	}
	if _, ok := appearance.NormalizeLightBackground("night_blue"); ok {
		t.Fatal("unsupported background must be rejected")
	}
}
