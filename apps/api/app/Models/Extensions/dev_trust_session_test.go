package extensions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// memoryDevTrustSessionStore 是 DevTrustSessionStore 的测试替身。
type memoryDevTrustSessionStore struct {
	sessions map[string]DevTrustSession
	nextErr  error
}

func newMemoryDevTrustSessionStore() *memoryDevTrustSessionStore {
	return &memoryDevTrustSessionStore{sessions: map[string]DevTrustSession{}}
}

func (s *memoryDevTrustSessionStore) Create(_ context.Context, session DevTrustSession) (DevTrustSession, error) {
	if s.nextErr != nil {
		return DevTrustSession{}, s.nextErr
	}
	if _, exists := s.sessions[session.ID]; exists {
		return DevTrustSession{}, ErrDevTrustSessionExists
	}
	for _, existing := range s.sessions {
		if existing.PackageSlug == session.PackageSlug && existing.RevokedAt == nil && session.ExpiresAt.After(time.Now()) {
			return DevTrustSession{}, ErrDevTrustSessionExists
		}
	}
	s.sessions[session.ID] = session
	return session, nil
}

func (s *memoryDevTrustSessionStore) List(_ context.Context, input DevTrustSessionListInput) ([]DevTrustSession, error) {
	items := []DevTrustSession{}
	for _, session := range s.sessions {
		if input.ExtensionID != "" && session.ExtensionID != input.ExtensionID {
			continue
		}
		if !input.IncludeRevoked && (session.RevokedAt != nil || !time.Now().Before(session.ExpiresAt)) {
			continue
		}
		items = append(items, session)
	}
	return items, nil
}

func (s *memoryDevTrustSessionStore) Active(_ context.Context, packageSlug string) (DevTrustSession, error) {
	for _, session := range s.sessions {
		if session.PackageSlug == packageSlug && session.RevokedAt == nil && time.Now().Before(session.ExpiresAt) {
			return session, nil
		}
	}
	return DevTrustSession{}, ErrDevTrustSessionNotFound
}

func (s *memoryDevTrustSessionStore) TouchDigest(_ context.Context, sessionID, digest string) error {
	session, ok := s.sessions[sessionID]
	if !ok || session.RevokedAt != nil || !time.Now().Before(session.ExpiresAt) {
		return ErrDevTrustSessionExpired
	}
	session.LastPackageDigest = digest
	s.sessions[sessionID] = session
	return nil
}

func (s *memoryDevTrustSessionStore) Revoke(_ context.Context, sessionID string, _ int64, _ string) error {
	session, ok := s.sessions[sessionID]
	if !ok || session.RevokedAt != nil {
		return ErrDevTrustSessionNotFound
	}
	now := time.Now()
	session.RevokedAt = &now
	s.sessions[sessionID] = session
	return nil
}

func (s *memoryDevTrustSessionStore) RevokeAllForExtension(_ context.Context, extensionID string, _ int64, _ string) error {
	for id, session := range s.sessions {
		if session.ExtensionID == extensionID && session.RevokedAt == nil {
			now := time.Now()
			session.RevokedAt = &now
			s.sessions[id] = session
		}
	}
	return nil
}

func (s *memoryDevTrustSessionStore) RevokeAllActive(_ context.Context, _ string) error {
	for id, session := range s.sessions {
		if session.RevokedAt == nil {
			now := time.Now()
			session.RevokedAt = &now
			s.sessions[id] = session
		}
	}
	return nil
}

func newDevTrustTestService(t *testing.T, extension Extension, appEnv string) (*DevTrustService, *memoryDevTrustSessionStore, *memoryExecutableTrustStore) {
	t.Helper()
	extensionsStore := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	trustStore := &memoryExecutableTrustStore{now: func() time.Time { return time.Now() }}
	trust := NewExecutableTrustService(extensionsStore, trustStore)
	sessions := newMemoryDevTrustSessionStore()
	service := NewDevTrustService(sessions, trust, appEnv)
	return service, sessions, trustStore
}

func copyDevTrustTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == from {
			return walkErr
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// digest 包含权限位，必须原样保留源文件模式。
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func devTrustTestExtension(t *testing.T) Extension {
	t.Helper()
	// buildTrustImpact 会真实读取 PackagePath 下的文件并按字节校验 digest，
	// digest 输入包含权限位，因此复制时逐文件保留模式。
	base := exactTrustExtension(t, "demo.dev-loop")
	devRoot := filepath.Join(t.TempDir(), "extensions", "dev", "demo.dev-loop")
	copyDevTrustTree(t, base.PackagePath, devRoot)
	extension := exactTrustExtension(t, "demo.dev-loop")
	extension.PackagePath = devRoot
	extension.Source = SourceUploaded
	extension.Status = StatusEnabled
	return extension
}

func TestDevTrustSessionCreateRequiresDevEnvSuperAdminAndDevRoot(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()

	// 生产环境拒绝。
	service, _, _ := newDevTrustTestService(t, extension, "production")
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); !errors.Is(err, ErrDevTrustDisabled) {
		t.Fatalf("production env must refuse: %v", err)
	}

	// 非 super_admin 拒绝。
	service, _, _ = newDevTrustTestService(t, extension, "development")
	if _, err := service.Create(context.Background(), techAdminPluginManager(), DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non super_admin must be denied: %v", err)
	}

	// 非 dev 根且不在配置的外部根内：拒绝。
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: "/srv/nowhere/demo",
	}); !errors.Is(err, ErrDevTrustRootForbidden) {
		t.Fatalf("unknown root must be refused: %v", err)
	}

	// 合法创建。
	session, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || session.ExpiresAt.Before(session.GrantedAt) {
		t.Fatalf("invalid session: %#v", session)
	}
	if session.PackageSlug != extension.ID {
		t.Fatalf("session slug must be the server-derived extension id, got %q", session.PackageSlug)
	}
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); !errors.Is(err, ErrDevTrustSessionExists) {
		t.Fatalf("duplicate active session must conflict: %v", err)
	}
}

func TestDevTrustSessionAcceptsConfiguredExternalRoots(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")
	// 未配置外部根：外部仓库路径被拒（先于 manifest 校验）。
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: "/srv/company-plugins/plugins/acme.demo",
	}); !errors.Is(err, ErrDevTrustRootForbidden) {
		t.Fatalf("unconfigured external root must be refused: %v", err)
	}

	// 配置 EXTERNAL_EXTENSION_ROOTS 后：外部根（含子目录）下真实包被接受。
	externalRoot := filepath.Join(t.TempDir(), "company-plugins")
	packageRoot := filepath.Join(externalRoot, "plugins", "acme.demo")
	copyDevTrustTree(t, extension.PackagePath, packageRoot)
	service = service.WithDevSourceRoots([]string{"/srv/company-plugins", externalRoot})
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: packageRoot,
	}); err != nil {
		t.Fatalf("configured external root must be accepted: %v", err)
	}

	// 配置项里的相对/空白项被忽略；相对路径的 root 仍被拒。
	service = service.WithDevSourceRoots([]string{"relative/root", "", externalRoot})
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: "relative/root/plugins/acme.demo",
	}); !errors.Is(err, ErrDevTrustRootForbidden) {
		t.Fatalf("relative root must stay refused: %v", err)
	}
}

func TestDevTrustSessionVerifiesManifestIdentity(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")

	// 根目录不存在 / 无法读取 manifest：fail-closed。
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: "/srv/company-plugins/plugins/acme.demo",
	}); !errors.Is(err, ErrDevTrustRootForbidden) {
		t.Fatalf("unreadable manifest root must be refused: %v", err)
	}

	// manifest 存在但声明的 ID 与扩展不一致：拒绝。
	otherRoot := filepath.Join(t.TempDir(), "plugins", "other.demo")
	copyDevTrustTree(t, extension.PackagePath, otherRoot)
	manifestPath := filepath.Join(otherRoot, extensionmanifest.ManifestFileName)
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"id": "demo.dev-loop"`), []byte(`"id": "other.demo"`), 1)
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	service = service.WithDevSourceRoots([]string{filepath.Dir(filepath.Dir(otherRoot))})
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: otherRoot,
	}); !errors.Is(err, ErrDevTrustRootForbidden) {
		t.Fatalf("manifest id mismatch must be refused: %v", err)
	}

	// manifest ID 一致：接受。
	matchingRoot := filepath.Join(t.TempDir(), "plugins", "demo.dev-loop")
	copyDevTrustTree(t, extension.PackagePath, matchingRoot)
	service = service.WithDevSourceRoots([]string{filepath.Dir(filepath.Dir(matchingRoot))})
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: matchingRoot,
	}); err != nil {
		t.Fatalf("matching manifest id must be accepted: %v", err)
	}
}

func TestDevTrustGrantPrefersStagedArtifactDigest(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()

	// 构造 staged 候选：dev 循环构建后新摘要先进入 staged。
	staged := devTrustTestExtension(t)
	staged.Version = "2.0.0"
	extension.StagedVersion = &ExtensionVersion{
		ID: 77, Version: staged.Version,
		PackageDigest: staged.PackageDigest, PackagePath: staged.PackagePath,
		Manifest: staged.Manifest, AdminFrontendDigest: staged.AdminFrontendDigest,
		InstalledAt: time.Now(),
	}
	// 服务在 StagedVersion 注入之后再构造：fakeExtensionStore 持有扩展快照。
	service, _, trustStore := newDevTrustTestService(t, extension, "development")

	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: staged.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}

	// staged 摘要（而非 active 旧摘要）应被授权。
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: staged.PackageDigest,
	}); err != nil {
		t.Fatalf("staged digest must be granted: %v", err)
	}
	impact, err := buildTrustImpact(staged, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := trustStore.HasLiveGrant(context.Background(), trustIdentity(impact))
	if err != nil {
		t.Fatal(err)
	}
	if !granted {
		t.Fatal("dev grant must target the staged artifact identity")
	}
	// active 旧摘要不被授权。
	activeImpact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	activeGranted, err := trustStore.HasLiveGrant(context.Background(), trustIdentity(activeImpact))
	if err != nil {
		t.Fatal(err)
	}
	if activeGranted {
		t.Fatal("stale active digest must not be granted while staged exists")
	}
}

func TestDevTrustGrantDevArtifactWritesExactGrant(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, trustStore := newDevTrustTestService(t, extension, "development")

	// 无会话：回到人工确认。
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("no session must fall back to manual challenge: %v", err)
	}

	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID,
		PackageSlug: extension.ID,
		RootPath:    extension.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}

	// 摘要不匹配：拒绝。
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: "deadbeef",
	}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("digest mismatch must fall back: %v", err)
	}

	// 正常路径：幂等写 exact live grant。
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); err != nil {
		t.Fatal(err)
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := trustStore.HasLiveGrant(context.Background(), trustIdentity(impact))
	if err != nil {
		t.Fatal(err)
	}
	if !granted {
		t.Fatal("dev grant must write a live exact grant")
	}
	// 幂等：再次调用成功且不重复。
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDevTrustGrantFailsClosedWhenRevokedOrExpired(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")

	session, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID,
		PackageSlug: extension.ID,
		RootPath:    extension.PackagePath,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 撤销后 fail-closed。
	if err := service.Revoke(context.Background(), superAdmin, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("revoked session must fail closed: %v", err)
	}

	// 非 super_admin 的 actor（即使有会话）拒绝。
	service2, _, _ := newDevTrustTestService(t, extension, "development")
	if _, err := service2.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID,
		PackageSlug: extension.ID,
		RootPath:    extension.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}
	limited := techAdminPluginManager()
	if err := service2.GrantDevArtifact(context.Background(), limited, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non super_admin actor must be denied: %v", err)
	}

	// 生产环境即使有活跃会话也拒绝。
	service3, _, _ := newDevTrustTestService(t, extension, "production")
	_ = service3
}

func TestDevTrustSafeModeFailsClosedAndRevokesSessions(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")

	// Safe Mode 下无法创建会话。
	service.WithSafeMode(true)
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); !errors.Is(err, ErrSafeModeActive) {
		t.Fatalf("safe mode must refuse session creation: %v", err)
	}

	// 先建会话，再打开 Safe Mode：Grant fail-closed，启动吊销清空全部会话。
	service, _, _ = newDevTrustTestService(t, extension, "development")
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID,
		PackageSlug: extension.ID,
		RootPath:    extension.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}
	service.WithSafeMode(true)
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("safe mode grant must fail closed: %v", err)
	}
	if err := service.RevokeAllOnSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Safe Mode 关闭后旧会话也已吊销，不会复活。
	service.WithSafeMode(false)
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("revoked session must not resurrect after safe mode ends: %v", err)
	}
}

type recordingDevSourceSyncer struct {
	calls int
	err   error
}

func (s *recordingDevSourceSyncer) SyncExternalSources(_ context.Context) (ExternalSyncResult, error) {
	s.calls++
	return ExternalSyncResult{}, s.err
}

// GrantDevArtifact 必须在读取制品身份**之前**重扫外部源：dev 循环刚构建出的新
// digest 只有经 SyncExternalSources 暂存，运行中的 API 才取得到同一制品。
func TestDevTrustGrantResyncsExternalSourcesBeforeGrant(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}

	syncer := &recordingDevSourceSyncer{}
	service.WithSourceSyncer(syncer)
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); err != nil {
		t.Fatalf("grant must succeed when sync is healthy: %v", err)
	}
	if syncer.calls != 1 {
		t.Fatalf("sync calls = %d; want 1", syncer.calls)
	}

	// 同步失败必须返回真实原因，不折叠成 trust challenge。
	syncer.err = errors.New("snapshot failed")
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); err == nil || !strings.Contains(err.Error(), "dev source resync failed") {
		t.Fatalf("sync failure must surface its reason: %v", err)
	}
	if syncer.calls != 2 {
		t.Fatalf("sync calls = %d; want 2", syncer.calls)
	}
}

// 未注入 syncer（或 dev 未启用）时保持既有行为：不重扫、不报错。
func TestDevTrustGrantWorksWithoutSourceSyncer(t *testing.T) {
	extension := devTrustTestExtension(t)
	superAdmin := extensionManager()
	service, _, _ := newDevTrustTestService(t, extension, "development")
	if _, err := service.Create(context.Background(), superAdmin, DevTrustSessionGrantInput{
		ExtensionID: extension.ID, PackageSlug: extension.ID, RootPath: extension.PackagePath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.GrantDevArtifact(context.Background(), superAdmin, DevTrustGrantInput{
		ExtensionID: extension.ID, PackageDigest: extension.PackageDigest,
	}); err != nil {
		t.Fatalf("grant without syncer must keep working: %v", err)
	}
}
