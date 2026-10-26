// Command compat-farm executes the V3 P12 compatibility matrix.
// Preferred CI entry: go run ./tests/compat (module at tests/compat).
// This binary is the apps/api module path equivalent.
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
	matrixFlag := flag.String("matrix", "", "path to matrix.yaml")
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
	}
	if !filepath.IsAbs(matrixPath) {
		candidate := filepath.Join(repoRoot, matrixPath)
		if _, err := os.Stat(candidate); err == nil {
			matrixPath = candidate
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := compatfarm.RunMatrix(ctx, matrixPath, compatfarm.RunOptions{RepoRoot: repoRoot})
	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else {
		fmt.Printf("schema: %s\nmatrix: %s\n", result.SchemaVersion, result.MatrixPath)
		for _, cell := range result.Cells {
			fmt.Printf("  [%s] %s status=%s", cell.Outcome, cell.ID, cell.Status)
			if cell.Message != "" {
				fmt.Printf(" — %s", cell.Message)
			}
			fmt.Println()
		}
		fmt.Printf("ok: %v\n", result.OK)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "compat farm error: %v\n", err)
		os.Exit(1)
	}
	if !result.OK {
		fmt.Fprintln(os.Stderr, "compat farm: FAILED")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "compat farm: OK")
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
			return "", fmt.Errorf("could not find repo root from %s", wd)
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
