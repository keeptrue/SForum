package rawcore_test

import (
	"regexp"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/rawcore"
)

var postgresIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// 白名单是「默认拒绝」的另一半：空列表会让 raw_core 静默退化成零权限，而重复项
// 会让集合比切片小，签发与校验两侧对不上。
func TestAllowlistIsWellFormed(t *testing.T) {
	tables := rawcore.WritableTables()
	sequences := rawcore.WritableSequences()
	if len(tables) == 0 || len(sequences) == 0 {
		t.Fatalf("raw_core allowlist must not be empty: tables=%v sequences=%v", tables, sequences)
	}
	seen := map[string]bool{}
	for _, name := range append(append([]string(nil), tables...), sequences...) {
		if !postgresIdentifier.MatchString(name) {
			t.Fatalf("%q is not a plain PostgreSQL identifier", name)
		}
		if seen[name] {
			t.Fatalf("%q appears twice in the raw_core allowlist", name)
		}
		seen[name] = true
	}
}

// RelationNames 必须与两个切片严格一致（它就是调用点唯一该用的形态）。
func TestRelationNamesMatchesAllowlist(t *testing.T) {
	tables, sequences := rawcore.RelationNames()
	wantTables := rawcore.WritableTables()
	wantSequences := rawcore.WritableSequences()
	if len(tables) != len(wantTables) || len(sequences) != len(wantSequences) {
		t.Fatalf("set sizes = %d/%d, want %d/%d", len(tables), len(sequences), len(wantTables), len(wantSequences))
	}
	for _, name := range wantTables {
		if _, ok := tables[name]; !ok {
			t.Fatalf("table %q missing from RelationNames", name)
		}
	}
	for _, name := range wantSequences {
		if _, ok := sequences[name]; !ok {
			t.Fatalf("sequence %q missing from RelationNames", name)
		}
	}
}

// 策略是只读数据：调用方改了返回值不能污染下一次读取。
func TestAllowlistReturnsCopies(t *testing.T) {
	first := rawcore.WritableTables()
	first[0] = "tampered"
	if second := rawcore.WritableTables(); second[0] == "tampered" {
		t.Fatal("WritableTables exposed the package-level slice")
	}
	sequences := rawcore.WritableSequences()
	sequences[0] = "tampered"
	if again := rawcore.WritableSequences(); again[0] == "tampered" {
		t.Fatal("WritableSequences exposed the package-level slice")
	}
}
