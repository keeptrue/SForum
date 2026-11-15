package pluginfiles

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Service manages Host-rooted plugin file namespaces.
type Service struct {
	mu sync.Mutex
	// writeMu 串行化「配额校验 → 写临时文件 → rename」整段（RV-53）。
	// 没有它时并发写各自按同一份旧用量判定，合计可以超过配额。
	writeMu    sync.Mutex
	baseDir    string
	namespaces map[string]Namespace
	// quotaCheckHook 仅供测试：在配额校验之后、落盘之前调用。
	// 它用来把「校验与写入不在同一个临界区」的竞态变成可复现的断言；生产为 nil。
	quotaCheckHook func()
}

// New creates a service rooted under baseDir (created if missing).
func New(baseDir string) (*Service, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, ErrInvalid
	}
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return &Service{baseDir: abs, namespaces: make(map[string]Namespace)}, nil
}

// EnsureNamespace provisions or updates quota config for an extension.
func (s *Service) EnsureNamespace(ns Namespace) (Namespace, error) {
	if s == nil {
		return Namespace{}, ErrInvalid
	}
	id := normalizeExtensionID(ns.ExtensionID)
	if id == "" {
		return Namespace{}, ErrInvalid
	}
	root := filepath.Join(s.baseDir, id)
	for _, kind := range []string{KindPrivate, KindTemp, KindStatic, KindUser} {
		if err := os.MkdirAll(filepath.Join(root, kind), 0o750); err != nil {
			return Namespace{}, err
		}
	}
	if ns.PrivateQuotaBytes <= 0 {
		ns.PrivateQuotaBytes = DefaultPrivateQuotaBytes
	}
	if ns.TempQuotaBytes <= 0 {
		ns.TempQuotaBytes = DefaultTempQuotaBytes
	}
	if ns.StaticQuotaBytes <= 0 {
		ns.StaticQuotaBytes = DefaultStaticQuotaBytes
	}
	if ns.UserQuotaBytes <= 0 {
		ns.UserQuotaBytes = DefaultUserQuotaBytes
	}
	ns.ExtensionID = id
	ns.Root = root
	s.mu.Lock()
	s.namespaces[id] = ns
	s.mu.Unlock()
	return ns, nil
}

// Write stores bytes under a namespace-relative path.
func (s *Service) Write(req WriteRequest) (FileInfo, error) {
	if s == nil {
		return FileInfo{}, ErrInvalid
	}
	req.Actor = strings.TrimSpace(req.Actor)
	if req.Actor == "" {
		return FileInfo{}, ErrPermissionDenied
	}
	if len(req.Data) == 0 {
		return FileInfo{}, ErrInvalid
	}
	if int64(len(req.Data)) > MaxWriteBytes {
		return FileInfo{}, ErrTooLarge
	}
	target, ns, err := s.resolvePath(req.ExtensionID, req.Kind, req.RelativePath, req.UserID, true)
	if err != nil {
		return FileInfo{}, err
	}
	// 用量判定到 rename 结束必须是一个临界区：否则两个并发写会按同一份旧用量
	// 各自判定通过，落盘后合计超配额。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	usage, err := s.Usage(req.ExtensionID)
	if err != nil {
		return FileInfo{}, err
	}
	// Account for overwrite: subtract existing size if present.
	var existing int64
	if st, stErr := os.Lstat(target); stErr == nil && st.Mode().IsRegular() {
		existing = st.Size()
	}
	delta := int64(len(req.Data)) - existing
	if delta < 0 {
		delta = 0
	}
	switch req.Kind {
	case KindPrivate:
		if usage.PrivateUsed+delta > ns.PrivateQuotaBytes {
			return FileInfo{}, ErrQuotaExceeded
		}
	case KindTemp:
		if usage.TempUsed+delta > ns.TempQuotaBytes {
			return FileInfo{}, ErrQuotaExceeded
		}
	case KindStatic:
		if usage.StaticUsed+delta > ns.StaticQuotaBytes {
			return FileInfo{}, ErrQuotaExceeded
		}
	case KindUser:
		if usage.UserUsed+delta > ns.UserQuotaBytes {
			return FileInfo{}, ErrQuotaExceeded
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return FileInfo{}, err
	}
	// Reject if target is a symlink (write-through would escape).
	if st, stErr := os.Lstat(target); stErr == nil && st.Mode()&os.ModeSymlink != 0 {
		return FileInfo{}, ErrSymlink
	}
	if s.quotaCheckHook != nil {
		s.quotaCheckHook()
	}
	// 临时名随机 + O_EXCL：固定后缀既可能撞名，也会让 os.WriteFile 跟随攻击者
	// 预先放好的符号链接（写穿到命名空间外）。
	dir := filepath.Dir(target)
	tmpFile, err := os.CreateTemp(dir, ".sforum-write-*")
	if err != nil {
		return FileInfo{}, err
	}
	tmp := tmpFile.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := tmpFile.Write(req.Data); err != nil {
		_ = tmpFile.Close()
		return FileInfo{}, err
	}
	if err := tmpFile.Chmod(0o640); err != nil {
		_ = tmpFile.Close()
		return FileInfo{}, err
	}
	if err := tmpFile.Close(); err != nil {
		return FileInfo{}, err
	}
	if err := os.Rename(tmp, target); err != nil {
		return FileInfo{}, err
	}
	keepTemp = false
	return s.statInfo(req.ExtensionID, req.Kind, req.RelativePath, req.UserID, target)
}

// Read returns file bytes.
func (s *Service) Read(req ReadRequest) ([]byte, FileInfo, error) {
	target, _, err := s.resolvePath(req.ExtensionID, req.Kind, req.RelativePath, req.UserID, false)
	if err != nil {
		return nil, FileInfo{}, err
	}
	if err := rejectSymlink(target); err != nil {
		return nil, FileInfo{}, err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, FileInfo{}, ErrNotFound
		}
		return nil, FileInfo{}, err
	}
	info, err := s.statInfo(req.ExtensionID, req.Kind, req.RelativePath, req.UserID, target)
	return data, info, err
}

// Delete removes a relative path.
func (s *Service) Delete(req DeleteRequest) error {
	if strings.TrimSpace(req.Actor) == "" {
		return ErrPermissionDenied
	}
	target, _, err := s.resolvePath(req.ExtensionID, req.Kind, req.RelativePath, req.UserID, false)
	if err != nil {
		return err
	}
	if err := rejectSymlink(target); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// Usage computes directory sizes for quota accounting.
func (s *Service) Usage(extensionID string) (Usage, error) {
	ns, err := s.namespace(extensionID)
	if err != nil {
		return Usage{}, err
	}
	private, err := dirSize(filepath.Join(ns.Root, KindPrivate))
	if err != nil {
		return Usage{}, err
	}
	temp, err := dirSize(filepath.Join(ns.Root, KindTemp))
	if err != nil {
		return Usage{}, err
	}
	staticUsed, err := dirSize(filepath.Join(ns.Root, KindStatic))
	if err != nil {
		return Usage{}, err
	}
	user, err := dirSize(filepath.Join(ns.Root, KindUser))
	if err != nil {
		return Usage{}, err
	}
	return Usage{
		ExtensionID: ns.ExtensionID,
		PrivateUsed: private, PrivateMax: ns.PrivateQuotaBytes,
		TempUsed: temp, TempMax: ns.TempQuotaBytes,
		StaticUsed: staticUsed, StaticMax: ns.StaticQuotaBytes,
		UserUsed: user, UserMax: ns.UserQuotaBytes,
	}, nil
}

// CleanupNamespace removes files for an extension per uninstall policy.
// Default: delete private/temp/static; retain user-owned data.
func (s *Service) CleanupNamespace(extensionID string) (CleanupResult, error) {
	return s.CleanupNamespaceWithOptions(extensionID, CleanupOptions{})
}

// CleanupNamespaceWithOptions applies explicit retention policy.
func (s *Service) CleanupNamespaceWithOptions(extensionID string, opts CleanupOptions) (CleanupResult, error) {
	ns, err := s.namespace(extensionID)
	if err != nil {
		// If never provisioned, nothing to clean.
		if err == ErrNotFound {
			return CleanupResult{ExtensionID: normalizeExtensionID(extensionID)}, nil
		}
		return CleanupResult{}, err
	}
	var files int
	var bytes int64
	removedKinds := make([]string, 0, 4)
	// temp 总是清理。
	kinds := []struct {
		kind   string
		remove bool
	}{
		{KindTemp, true},
		{KindPrivate, !opts.RetainPrivate},
		{KindStatic, !opts.RetainStatic},
		{KindUser, opts.DeleteUser},
	}
	for _, item := range kinds {
		if !item.remove {
			continue
		}
		dir := filepath.Join(ns.Root, item.kind)
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			if info, infoErr := d.Info(); infoErr == nil {
				bytes += info.Size()
				files++
			}
			return nil
		})
		if remErr := os.RemoveAll(dir); remErr != nil {
			return CleanupResult{}, remErr
		}
		removedKinds = append(removedKinds, item.kind)
	}
	// 若 private/temp/static/user 全删且无保留，移除根目录。
	if !opts.RetainPrivate && !opts.RetainStatic && opts.DeleteUser {
		_ = os.RemoveAll(ns.Root)
		s.mu.Lock()
		delete(s.namespaces, ns.ExtensionID)
		s.mu.Unlock()
	}
	return CleanupResult{
		ExtensionID: ns.ExtensionID, RemovedFiles: files, RemovedBytes: bytes,
		Kinds: removedKinds,
	}, nil
}

// CleanupTemp removes temp files older than maxAge (0 = DefaultTempTTL).
func (s *Service) CleanupTemp(extensionID string, maxAge time.Duration) (int, error) {
	ns, err := s.namespace(extensionID)
	if err != nil {
		return 0, err
	}
	if maxAge <= 0 {
		maxAge = DefaultTempTTL
	}
	cutoff := time.Now().Add(-maxAge)
	tempRoot := filepath.Join(ns.Root, KindTemp)
	removed := 0
	_ = filepath.WalkDir(tempRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	return removed, nil
}

func (s *Service) namespace(extensionID string) (Namespace, error) {
	id := normalizeExtensionID(extensionID)
	if id == "" {
		return Namespace{}, ErrInvalid
	}
	s.mu.Lock()
	ns, ok := s.namespaces[id]
	s.mu.Unlock()
	if !ok {
		// Auto-discover if directory exists from prior process.
		root := filepath.Join(s.baseDir, id)
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return Namespace{}, ErrNotFound
		}
		return s.EnsureNamespace(Namespace{ExtensionID: id})
	}
	return ns, nil
}

func (s *Service) resolvePath(extensionID, kind, rel, userID string, createParents bool) (string, Namespace, error) {
	_ = createParents
	ns, err := s.namespace(extensionID)
	if err != nil {
		// Auto-provision on first write path via EnsureNamespace from caller.
		if err == ErrNotFound {
			ns, err = s.EnsureNamespace(Namespace{ExtensionID: extensionID})
			if err != nil {
				return "", Namespace{}, err
			}
		} else {
			return "", Namespace{}, err
		}
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case KindPrivate, KindTemp, KindStatic, KindUser:
	default:
		return "", Namespace{}, ErrInvalid
	}
	rel, err = normalizeRelativePath(rel)
	if err != nil {
		return "", Namespace{}, err
	}
	base := filepath.Join(ns.Root, kind)
	if kind == KindUser {
		userID = strings.TrimSpace(userID)
		if userID == "" || strings.Contains(userID, "..") || strings.ContainsAny(userID, `/\`) {
			return "", Namespace{}, ErrInvalid
		}
		base = filepath.Join(base, userID)
	}
	target := filepath.Join(base, filepath.FromSlash(rel))
	// Ensure resolved path stays under base (blocks .. and absolute tricks).
	cleanBase := filepath.Clean(base) + string(os.PathSeparator)
	cleanTarget := filepath.Clean(target)
	if cleanTarget != filepath.Clean(base) && !strings.HasPrefix(cleanTarget+string(os.PathSeparator), cleanBase) {
		return "", Namespace{}, ErrTraversal
	}
	// Also ensure under namespace root.
	rootPrefix := filepath.Clean(ns.Root) + string(os.PathSeparator)
	if !strings.HasPrefix(cleanTarget+string(os.PathSeparator), rootPrefix) && cleanTarget != filepath.Clean(ns.Root) {
		return "", Namespace{}, ErrTraversal
	}
	return cleanTarget, ns, nil
}

func (s *Service) statInfo(extensionID, kind, rel, userID, target string) (FileInfo, error) {
	st, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return FileInfo{}, ErrNotFound
		}
		return FileInfo{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return FileInfo{}, ErrSymlink
	}
	return FileInfo{
		SchemaVersion: SchemaVersion,
		ExtensionID:   normalizeExtensionID(extensionID),
		Kind:          kind,
		RelativePath:  rel,
		UserID:        strings.TrimSpace(userID),
		Size:          st.Size(),
		ModTime:       st.ModTime().UTC(),
	}, nil
}

func normalizeExtensionID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) {
		return ""
	}
	return id
}

func normalizeRelativePath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	// Reject absolute and drive-looking inputs before any prefix stripping.
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) ||
		filepath.IsAbs(rel) || len(rel) > MaxRelativePathLen {
		return "", ErrInvalid
	}
	if strings.Contains(rel, "\x00") {
		return "", ErrInvalid
	}
	// Reject absolute and parent segments before join.
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrTraversal
		}
		if strings.Contains(part, `\`) {
			return "", ErrTraversal
		}
	}
	return filepath.ToSlash(rel), nil
}

func rejectSymlink(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	return nil
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		// Do not follow symlinks for accounting.
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return total, nil
}
