package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

type productionLifecyclePackageArtifact struct {
	root     string
	path     string
	relative string
	digest   string
	present  bool
}

func inspectProductionLifecyclePackage(
	extensionRoot string,
	packagePath string,
	packageDigest string,
) (productionLifecyclePackageArtifact, error) {
	extensionRoot = strings.TrimSpace(extensionRoot)
	if extensionRoot == "" {
		return productionLifecyclePackageArtifact{}, errProductionLifecycleDependency
	}
	root, err := filepath.Abs(extensionRoot)
	if err != nil {
		return productionLifecyclePackageArtifact{}, fmt.Errorf("resolve lifecycle package root: %w", err)
	}
	if packagePath == "" || packagePath != strings.TrimSpace(packagePath) {
		return productionLifecyclePackageArtifact{}, errProductionLifecycleCleanupConflict
	}
	path, err := filepath.Abs(packagePath)
	if err != nil {
		return productionLifecyclePackageArtifact{}, fmt.Errorf("resolve lifecycle package path: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return productionLifecyclePackageArtifact{}, errProductionLifecycleCleanupConflict
	}
	artifact := productionLifecyclePackageArtifact{
		root: root, path: path, relative: relative, digest: packageDigest,
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return artifact, nil
	} else if err != nil {
		return productionLifecyclePackageArtifact{}, fmt.Errorf("inspect lifecycle package: %w", err)
	}
	actualDigest, err := extensionpackage.DigestTree(path)
	if err != nil {
		return productionLifecyclePackageArtifact{}, fmt.Errorf("digest lifecycle package: %w", err)
	}
	if actualDigest != packageDigest {
		return productionLifecyclePackageArtifact{}, errProductionLifecycleCleanupConflict
	}
	artifact.present = true
	return artifact, nil
}

func (a productionLifecyclePackageArtifact) purge() error {
	current, err := inspectProductionLifecyclePackage(a.root, a.path, a.digest)
	if err != nil {
		return err
	}
	if !current.present {
		return nil
	}
	root, err := os.OpenRoot(a.root)
	if err != nil {
		return fmt.Errorf("open lifecycle package root: %w", err)
	}
	defer root.Close()
	if err := root.RemoveAll(current.relative); err != nil {
		return fmt.Errorf("remove exact lifecycle package: %w", err)
	}
	if _, err := os.Lstat(current.path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errProductionLifecycleCleanupConflict
		}
		return fmt.Errorf("verify lifecycle package removal: %w", err)
	}
	return nil
}
