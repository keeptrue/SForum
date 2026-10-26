package extensionpackage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	ErrInvalidManifest         = extensionmanifest.ErrInvalidManifest
	ErrSnapshotConflict        = errors.New("extension package: immutable snapshot conflict")
	ErrSnapshotReleased        = errors.New("extension package: owned snapshot already released")
	ErrSnapshotLockUnsupported = errors.New("extension package: advisory snapshot locks are unsupported")
	uploadedSnapshotMu         sync.Mutex
)

type Snapshot struct {
	Root     string
	Manifest string
	Digest   string
}

// OwnedSnapshot keeps this process and cooperating processes serialized for one
// uploaded digest until the caller either persists or safely discards it.
// Lock files are intentionally retained so a waiter never switches to a new inode.
type OwnedSnapshot struct {
	Snapshot

	mu       sync.Mutex
	lockFile *os.File
	root     string
	digest   string
	created  bool
	released bool
	local    bool
}

type File struct {
	Path string
	Mode fs.FileMode
	Body []byte
}

type snapshotFile struct {
	path string
	mode fs.FileMode
	body []byte
}

// SnapshotUploaded canonicalizes an uploaded package and atomically publishes an immutable version snapshot.
//
// manifestBody 是包根 sforum.extension.json 原文；files 为其余文件（可含 includes partials）。
// 合并校验后，快照内写入规范合并后的入口 manifest（无 includes），并原样保留其它文件，
// 以便内容寻址 digest 对「仅空白/字段顺序不同」的单文件包保持稳定，同时多文件 partials 仍落盘可审计。
// Snapshot.Manifest 为合并后的规范 JSON，供数据库与 API 消费。
func SnapshotUploaded(destinationRoot string, manifestBody []byte, files []File) (Snapshot, error) {
	owned, err := SnapshotUploadedOwned(destinationRoot, manifestBody, files)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := owned.Snapshot
	if err := owned.Release(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// SnapshotUploadedOwned publishes an uploaded snapshot while retaining a
// per-digest advisory lock. The caller must call Release. Created reports
// whether this call published the final digest directory instead of reusing it.
func SnapshotUploadedOwned(destinationRoot string, manifestBody []byte, files []File) (*OwnedSnapshot, error) {
	manifest, canonicalManifest, err := loadMergedManifest(manifestBody, files)
	if err != nil {
		return nil, err
	}
	normalizedFiles, err := normalizeSnapshotFiles(files)
	if err != nil {
		return nil, err
	}
	normalizedFiles = append(normalizedFiles, snapshotFile{
		path: extensionmanifest.ManifestFileName,
		mode: 0o600,
		body: []byte(canonicalManifest),
	})
	sort.Slice(normalizedFiles, func(i, j int) bool { return normalizedFiles[i].path < normalizedFiles[j].path })

	destinationRoot = strings.TrimSpace(destinationRoot)
	if destinationRoot == "" {
		return nil, fmt.Errorf("%w: empty destination root", ErrInvalidPath)
	}
	versionRoot := filepath.Join(destinationRoot, manifest.ID, manifest.Version)
	if err := os.MkdirAll(versionRoot, 0o755); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(versionRoot, ".snapshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)

	for _, file := range normalizedFiles {
		target := filepath.Join(staging, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := writeSyncedFile(target, file.body, file.mode); err != nil {
			return nil, err
		}
	}
	if err := syncDirectoryTree(staging); err != nil {
		return nil, err
	}

	digest, err := DigestTree(staging)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(versionRoot, digest)
	snapshot := Snapshot{Root: target, Manifest: canonicalManifest, Digest: digest}

	// flock may not serialize independent descriptors inside every supported
	// Unix implementation, so keep a process-local gate in the same lock order.
	uploadedSnapshotMu.Lock()
	lockFile, err := lockUploadedSnapshot(versionRoot, digest)
	if err != nil {
		uploadedSnapshotMu.Unlock()
		return nil, err
	}
	owned := &OwnedSnapshot{
		Snapshot: snapshot,
		lockFile: lockFile,
		root:     target,
		digest:   digest,
		local:    true,
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			_ = owned.Release()
		}
	}()

	if exists, err := verifyExistingSnapshot(target, digest); err != nil {
		return nil, err
	} else if exists {
		releaseOnError = false
		return owned, nil
	}

	if err := syncDirectory(versionRoot); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, target); err != nil {
		if exists, verifyErr := verifyExistingSnapshot(target, digest); exists {
			if verifyErr != nil {
				return nil, verifyErr
			}
			releaseOnError = false
			return owned, nil
		}
		return nil, err
	}
	owned.created = true
	if err := syncDirectory(versionRoot); err != nil {
		cleanupErr := owned.RemoveIfCreated()
		return nil, errors.Join(err, cleanupErr)
	}
	releaseOnError = false
	return owned, nil
}

// Created reports whether this call published the immutable target directory.
func (s *OwnedSnapshot) Created() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

// RemoveIfCreated removes only the target published by this owned handle while
// its advisory lock is still held. Reused or drifted targets are retained.
func (s *OwnedSnapshot) RemoveIfCreated() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return ErrSnapshotReleased
	}
	if !s.created {
		return nil
	}
	exists, err := verifyExistingSnapshot(s.root, s.digest)
	if err != nil {
		return err
	}
	if !exists {
		s.created = false
		return nil
	}
	parent := filepath.Dir(s.root)
	quarantine, err := os.MkdirTemp(parent, ".discard-")
	if err != nil {
		return err
	}
	if err := os.Remove(quarantine); err != nil {
		return err
	}
	if err := os.Rename(s.root, quarantine); err != nil {
		return err
	}
	s.created = false
	if err := syncDirectory(parent); err != nil {
		return err
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	return nil
}

// Release unlocks the digest and is idempotent. The lock file itself must not
// be removed because another process may already be waiting on that inode.
func (s *OwnedSnapshot) Release() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return nil
	}
	s.released = true
	lockFile := s.lockFile
	local := s.local
	s.lockFile = nil
	s.local = false
	s.mu.Unlock()

	var unlockErr, closeErr error
	if lockFile != nil {
		unlockErr = unlockUploadedSnapshotFile(lockFile)
		closeErr = lockFile.Close()
	}
	if local {
		uploadedSnapshotMu.Unlock()
	}
	return errors.Join(unlockErr, closeErr)
}

func lockUploadedSnapshot(versionRoot string, digest string) (*os.File, error) {
	lockRoot := filepath.Join(versionRoot, ".locks")
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(filepath.Join(lockRoot, digest+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockUploadedSnapshotFile(lockFile); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return lockFile, nil
}

// SnapshotBuiltin copies a built-in package through the same canonical snapshot path as uploads.
func SnapshotBuiltin(sourceRoot string, destinationRoot string) (Snapshot, error) {
	manifestBody, files, err := readPackageDirectory(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	return SnapshotUploaded(destinationRoot, manifestBody, files)
}

// SnapshotExternalSourceOwned 静态读取外部源码包并发布 uploaded 不可变快照。
// 调用方必须 Release；持锁期间可以在持久化失败时安全移除本次新建快照。
func SnapshotExternalSourceOwned(sourceRoot string, destinationRoot string) (*OwnedSnapshot, error) {
	manifestBody, files, err := readPackageDirectory(sourceRoot)
	if err != nil {
		return nil, err
	}
	return SnapshotUploadedOwned(destinationRoot, manifestBody, files)
}

func readPackageDirectory(sourceRoot string) ([]byte, []File, error) {
	type sourceFile struct {
		path string
	}

	rootInfo, err := os.Lstat(sourceRoot)
	if err != nil {
		return nil, nil, err
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrSymlink, sourceRoot)
	}
	if !rootInfo.IsDir() {
		return nil, nil, fmt.Errorf("%w: package root %s", ErrNonRegular, sourceRoot)
	}
	packageRoot, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, nil, err
	}
	defer packageRoot.Close()
	openedRootInfo, err := packageRoot.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(rootInfo, openedRootInfo) {
		return nil, nil, fmt.Errorf("%w: package root changed while opening %s", ErrNonRegular, sourceRoot)
	}

	validated := make([]sourceFile, 0)
	seen := make(map[string]struct{})
	err = fs.WalkDir(packageRoot.FS(), ".", func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, filepath.Join(sourceRoot, filepath.FromSlash(current)))
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrNonRegular, current)
		}
		normalized, ok := canonicalRelativePath(current)
		if !ok {
			return fmt.Errorf("%w: %s", ErrInvalidPath, current)
		}
		if _, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("%w: duplicate normalized path %s", ErrInvalidPath, normalized)
		}
		seen[normalized] = struct{}{}
		validated = append(validated, sourceFile{path: normalized})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	var manifestBody []byte
	files := make([]File, 0, len(validated))
	for _, source := range validated {
		body, mode, err := readRootRegularFile(packageRoot, source.path)
		if err != nil {
			return nil, nil, err
		}
		if source.path == extensionmanifest.ManifestFileName {
			manifestBody = body
			continue
		}
		files = append(files, File{Path: source.path, Mode: mode, Body: body})
	}
	if manifestBody == nil {
		return nil, nil, fmt.Errorf("%w: missing %s", ErrInvalidManifest, extensionmanifest.ManifestFileName)
	}
	return manifestBody, files, nil
}

func readRootRegularFile(root *os.Root, name string) ([]byte, fs.FileMode, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, 0, err
	}
	if before.Mode()&fs.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("%w: %s", ErrSymlink, name)
	}
	if !before.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s", ErrNonRegular, name)
	}

	handle, err := root.Open(name)
	if err != nil {
		return nil, 0, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, 0, fmt.Errorf("%w: file changed while opening %s", ErrNonRegular, name)
	}
	body, err := io.ReadAll(handle)
	if err != nil {
		return nil, 0, err
	}
	after, err := handle.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !os.SameFile(opened, after) || int64(len(body)) != after.Size() {
		return nil, 0, fmt.Errorf("%w: file changed while reading %s", ErrNonRegular, name)
	}
	return body, after.Mode(), nil
}

// loadMergedManifest 用入口 JSON + 包内文件解析 includes，返回合并后的 Manifest 与规范 JSON。
func loadMergedManifest(rootBody []byte, files []File) (extensionmanifest.Manifest, string, error) {
	pkg := extensionmanifest.FileMapFS{
		extensionmanifest.ManifestFileName: append([]byte(nil), rootBody...),
	}
	for _, file := range files {
		raw := strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/"))
		for _, segment := range strings.Split(raw, "/") {
			if segment == ".." {
				return extensionmanifest.Manifest{}, "", fmt.Errorf("%w: %s", ErrInvalidPath, file.Path)
			}
		}
		clean, ok := extensionmanifest.SafeArchivePath(raw)
		if !ok || clean == extensionmanifest.ManifestFileName {
			// 与 normalizeSnapshotFiles 对齐：非法路径或重复入口稍后统一处理；此处跳过坏键避免污染 FS。
			if !ok {
				return extensionmanifest.Manifest{}, "", fmt.Errorf("%w: %s", ErrInvalidPath, file.Path)
			}
			continue
		}
		pkg[clean] = file.Body
	}
	manifest, err := extensionmanifest.LoadRootBytes(rootBody, pkg)
	if err != nil {
		return extensionmanifest.Manifest{}, "", fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return extensionmanifest.Manifest{}, "", fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	return manifest, string(canonical), nil
}

func normalizeSnapshotFiles(files []File) ([]snapshotFile, error) {
	normalized := make([]snapshotFile, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.Mode&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, file.Path)
		}
		if file.Mode.Type() != 0 {
			return nil, fmt.Errorf("%w: %s", ErrNonRegular, file.Path)
		}
		raw := strings.TrimSpace(strings.ReplaceAll(file.Path, "\\", "/"))
		for _, segment := range strings.Split(raw, "/") {
			if segment == ".." {
				return nil, fmt.Errorf("%w: %s", ErrInvalidPath, file.Path)
			}
		}
		path, ok := canonicalRelativePath(raw)
		if !ok || path == extensionmanifest.ManifestFileName {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPath, file.Path)
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("%w: duplicate normalized path %s", ErrInvalidPath, path)
		}
		seen[path] = struct{}{}
		normalized = append(normalized, snapshotFile{path: path, mode: file.Mode.Perm(), body: file.Body})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].path < normalized[j].path })
	for _, file := range normalized {
		segments := strings.Split(file.path, "/")
		for index := 1; index < len(segments); index++ {
			parent := strings.Join(segments[:index], "/")
			if _, collision := seen[parent]; collision {
				return nil, fmt.Errorf("%w: file and directory collide at %s", ErrInvalidPath, parent)
			}
		}
	}
	return normalized, nil
}

func writeSyncedFile(path string, body []byte, mode fs.FileMode) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := handle.Write(body); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Chmod(mode.Perm()); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

func verifyExistingSnapshot(root string, expectedDigest string) (bool, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("%w: %v", ErrSnapshotConflict, err)
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return true, fmt.Errorf("%w: %s is not a snapshot directory", ErrSnapshotConflict, root)
	}
	// 快照目录名就是它自己的内容摘要（内容寻址），因此同一路径的内容不可变：
	// 复用记忆化校验，避免每次重新读取整棵快照树。摘要不符仍然报错。
	actual, err := VerifiedTreeDigest(root)
	if err != nil {
		return true, fmt.Errorf("%w: %v", ErrSnapshotConflict, err)
	}
	if actual != expectedDigest {
		return true, fmt.Errorf("%w: expected %s, found %s", ErrSnapshotConflict, expectedDigest, actual)
	}
	return true, nil
}

func syncDirectoryTree(root string) error {
	directories := make([]string, 0)
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, current)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	err = handle.Sync()
	closeErr := handle.Close()
	// Some filesystems do not support fsync on directories; file fsync and atomic rename still apply.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		err = nil
	}
	if err != nil {
		return err
	}
	return closeErr
}
