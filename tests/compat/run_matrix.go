// Compatibility farm matrix executor (V3 P12).
//
// Required cells must pass. Missing matrix, skip, or fail exits non-zero so
// CI and ./scripts/test.sh fail closed.
//
// Usage (from repo root):
//
//	go run ./tests/compat/run_matrix.go
//	go run ./tests/compat/run_matrix.go -matrix tests/compat/matrix.yaml
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	compatfarm "github.com/keeptrue/sforum/apps/api/app/Support/CompatFarm"
)

func main() {
	matrixFlag := flag.String("matrix", "", "path to matrix.yaml (default: <repo>/tests/compat/matrix.yaml)")
	jsonFlag := flag.Bool("json", false, "print JSON report")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall farm timeout")
	flag.Parse()

	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat farm: %v\n", err)
		os.Exit(2)
	}
	matrixPath := *matrixFlag
	if matrixPath == "" {
		matrixPath = filepath.Join(repoRoot, "tests", "compat", "matrix.yaml")
	} else if !filepath.IsAbs(matrixPath) {
		// Prefer cwd-relative, then repo-relative, then tests/compat basename.
		if abs, err := filepath.Abs(matrixPath); err == nil {
			if _, statErr := os.Stat(abs); statErr == nil {
				matrixPath = abs
			} else if _, statErr := os.Stat(filepath.Join(repoRoot, matrixPath)); statErr == nil {
				matrixPath = filepath.Join(repoRoot, matrixPath)
			} else {
				matrixPath = filepath.Join(repoRoot, "tests", "compat", filepath.Base(matrixPath))
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := compatfarm.RunMatrix(ctx, matrixPath, compatfarm.RunOptions{
		RepoRoot:    repoRoot,
		DatabaseURL: os.Getenv("SFORUM_COMPAT_DATABASE_URL"),
	})
	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else {
		printHuman(result)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat farm error: %v\n", err)
		os.Exit(1)
	}
	if !result.OK {
		fmt.Fprintf(os.Stderr, "compat farm: FAILED (required/deprecated cells must pass; skip/missing fail the gate)\n")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "compat farm: OK")
}

func printHuman(result compatfarm.RunResult) {
	fmt.Printf("schema: %s\n", result.SchemaVersion)
	fmt.Printf("matrix: %s\n", result.MatrixPath)
	for _, cell := range result.Cells {
		line := fmt.Sprintf("  [%s] %s status=%s", cell.Outcome, cell.ID, cell.Status)
		if cell.Message != "" {
			line += " — " + cell.Message
		}
		if cell.ShimCalls > 0 {
			line += fmt.Sprintf(" (shimCalls=%d)", cell.ShimCalls)
		}
		fmt.Println(line)
	}
	fmt.Printf("ok: %v\n", result.OK)
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if fileExists(filepath.Join(dir, "tests", "compat", "matrix.yaml")) &&
			fileExists(filepath.Join(dir, "apps", "api", "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repo root from %s (need tests/compat/matrix.yaml)", wd)
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
