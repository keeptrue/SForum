package extensions

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// DevAutoRuntime 的行为测试：彻底 0 命令 dev 自动激活的三个核心语义——
// 首次安装自动启用、已启用包热更升级、操作者显式禁用的包不被翻回。

type devAutoFakeIdentity struct {
	byEmail    map[string]identity.CurrentUser
	nextID     int64
	loadCalled int
}

func newDevAutoFakeIdentity() *devAutoFakeIdentity {
	return &devAutoFakeIdentity{byEmail: map[string]identity.CurrentUser{}, nextID: 100}
}

func (s *devAutoFakeIdentity) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	s.loadCalled++
	for _, user := range s.byEmail {
		if user.ID == userID {
			return identity.Actor{
				ID: userID, Status: identity.UserStatusActive,
				RoleKeys: []string{identity.RoleSuperAdmin},
			}, nil
		}
	}
	return identity.Actor{}, identity.ErrUserNotFound
}

func (s *devAutoFakeIdentity) GetCurrentUserByEmail(_ context.Context, email string) (identity.CurrentUser, error) {
	if user, ok := s.byEmail[email]; ok {
		return user, nil
	}
	return identity.CurrentUser{}, identity.ErrUserNotFound
}

func (s *devAutoFakeIdentity) AnyUserExists(context.Context) (bool, error) {
	return len(s.byEmail) > 0, nil
}

func (s *devAutoFakeIdentity) GetCredentialByLogin(context.Context, string) (identity.CredentialUser, error) {
	return identity.CredentialUser{}, identity.ErrCredentialNotFound
}

func (s *devAutoFakeIdentity) WithBootstrapTx(
	ctx context.Context, fn func(context.Context, identity.TxStore) error,
) error {
	// 简化：真实事务语义由 identity store 测试覆盖；这里直接执行（回调用 TxStore，
	// fake 里用同一自身实现 Cast，不真正走 identity.TxStore——为避免依赖其全接口，
	// 该测试只验证 Runtime 行为；创建用户经由注入的创建函数模拟）。
	return fn(ctx, devAutoTxStoreAdapter{s})
}

type devAutoTxStoreAdapter struct{ inner *devAutoFakeIdentity }

func (a devAutoTxStoreAdapter) CreateUser(_ context.Context, input identity.CreateUserInput) (identity.CurrentUser, error) {
	if _, exists := a.inner.byEmail[input.Email]; exists {
		return identity.CurrentUser{}, errors.New("duplicate email")
	}
	a.inner.nextID++
	user := identity.CurrentUser{ID: a.inner.nextID, Username: input.Username, Status: identity.UserStatusActive}
	a.inner.byEmail[input.Email] = user
	return user, nil
}

func (a devAutoTxStoreAdapter) GetRole(_ context.Context, roleKey string) (identity.Role, error) {
	return identity.Role{ID: 1, Key: roleKey}, nil
}

func (a devAutoTxStoreAdapter) AssignRole(_ context.Context, userID int64, _ int64) error {
	// 角色信息在 fake LoadActor 中按 ID 硬编码 super_admin，无需记录。
	return nil
}

type devAutoFakePromoter struct {
	enabledIDs  []string
	upgradedIDs []string
	// upgradedKeys 捕获每次 Upgrade 收到的 IdempotencyKey（dev 通道必须是
	// dev-auto/ 前缀的确定性键；契约版本晋升在真实 Service.Upgrade 内完成）。
	upgradedKeys []string
	// enabledKeys 捕获每次 Enable 收到的 IdempotencyKey。Enable 同样要过
	// lifecycle coordinator，缺键会让新装插件永远停在 installed。
	enabledKeys  []string
	upgradeCalls int
	// firstUpgradeErr 非空时首次 Upgrade 返回该错误（模拟 coordinator 撞上
	// 需要显式 retry 的终态 operation），第二次起正常成功。
	firstUpgradeErr error
	// operations 是 LifecycleOperations 返回的持久操作摘要；
	// recoveredIDs 捕获 RecoverLifecycleOperation 收到的 operation ID。
	operations   []LifecycleOperationSummary
	recoveredIDs []int64
}

func (p *devAutoFakePromoter) Enable(_ context.Context, _ identity.Actor, id string, input EnableInput) (Extension, error) {
	p.enabledIDs = append(p.enabledIDs, id)
	p.enabledKeys = append(p.enabledKeys, input.IdempotencyKey)
	return Extension{ID: id, Status: StatusEnabled}, nil
}

func (p *devAutoFakePromoter) Upgrade(_ context.Context, _ identity.Actor, id string, input UpgradeInput) (Extension, error) {
	p.upgradeCalls++
	if p.upgradeCalls == 1 && p.firstUpgradeErr != nil {
		return Extension{}, p.firstUpgradeErr
	}
	p.upgradedIDs = append(p.upgradedIDs, id)
	if p.upgradedKeys == nil {
		p.upgradedKeys = []string{}
	}
	p.upgradedKeys = append(p.upgradedKeys, input.IdempotencyKey)
	return Extension{ID: id, Status: StatusEnabled}, nil
}

type devAutoFakeGrants struct{ ensured []string }

func (g *devAutoFakeGrants) EnsureLiveGrantForDevAuto(_ context.Context, _ identity.Actor, extension Extension) error {
	g.ensured = append(g.ensured, extension.ID+"@"+extension.PackageDigest)
	return nil
}

func newDevAutoTestRuntime(t *testing.T, items map[string]Extension) (
	*DevAutoRuntime, *devAutoFakePromoter, *devAutoFakeGrants, *devAutoFakeIdentity,
) {
	t.Helper()
	promoter := &devAutoFakePromoter{}
	grants := &devAutoFakeGrants{}
	identities := newDevAutoFakeIdentity()
	base := NewService(&fakeExtensionStore{items: items}, t.TempDir())
	runtime := &DevAutoRuntime{
		service:    base,
		promoter:   promoter,
		identities: identities,
		grants:     grants,
		logger:     discardLogger(),
	}
	return runtime, promoter, grants, identities
}

func TestDevAutoRuntimeEnablesFreshlyInstalledExtension(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusInstalled
	runtime, promoter, grants, _ := newDevAutoTestRuntime(t, map[string]Extension{extension.ID: extension})
	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.enabledIDs) != 1 || promoter.enabledIDs[0] != extension.ID {
		t.Fatalf("enabled = %#v; want [%s]", promoter.enabledIDs, extension.ID)
	}
	if len(grants.ensured) != 1 || !strings.HasPrefix(grants.ensured[0], extension.ID+"@") {
		t.Fatalf("grants = %#v", grants.ensured)
	}
	// Enable 也必须带确定性幂等键：真实 coordinator 缺键会直接拒绝，
	// 结果是新装插件永远停在 installed（只有台账里一条 promote failed）。
	if len(promoter.enabledKeys) != 1 || !strings.HasPrefix(promoter.enabledKeys[0], DevAutoIdempotencyKeyPrefix) {
		t.Fatalf("enable idempotency keys = %#v", promoter.enabledKeys)
	}
}

func TestDevAutoRuntimeUpgradesEnabledExtensionWithStagedDigest(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusEnabled
	extension.Type = TypePlugin
	extension.Manifest.Backend.ProtocolVersion = 2
	extension.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "sforum.lifecycle@1"}
	staged := devTrustTestExtension(t)
	extension.StagedVersion = &ExtensionVersion{
		ID: 77, Version: "2.0.0", PackageDigest: staged.PackageDigest,
		PackagePath: staged.PackagePath, Manifest: staged.Manifest,
	}
	runtime, promoter, grants, _ := newDevAutoTestRuntime(t, map[string]Extension{extension.ID: extension})
	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.upgradedIDs) != 1 || promoter.upgradedIDs[0] != extension.ID {
		t.Fatalf("upgraded = %#v", promoter.upgradedIDs)
	}
	if len(promoter.enabledIDs) != 0 {
		t.Fatalf("enabled unexpectedly: %#v", promoter.enabledIDs)
	}
	if len(grants.ensured) != 1 || grants.ensured[0] != extension.ID+"@"+staged.PackageDigest {
		t.Fatalf("grant must target staged digest: %#v", grants.ensured)
	}
}

func TestDevAutoRuntimeRespectsDisabledState(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusDisabled
	extension.StagedVersion = &ExtensionVersion{ID: 77, Version: "2.0.0", PackageDigest: "abc"}
	runtime, promoter, grants, _ := newDevAutoTestRuntime(t, map[string]Extension{extension.ID: extension})
	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.enabledIDs)+len(promoter.upgradedIDs) != 0 {
		t.Fatalf("disabled extension must not be touched: %#v", promoter)
	}
	if len(grants.ensured) != 0 {
		t.Fatalf("disabled extension must not be granted: %#v", grants.ensured)
	}
}

func TestDevAutoRuntimeCreatesOperatorOnceAndReusesIt(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusInstalled
	runtime, _, _, identities := newDevAutoTestRuntime(t, map[string]Extension{extension.ID: extension})
	runtime.promoteOneForTest(t, extension.ID)
	runtime.promoteOneForTest(t, extension.ID)

	// 两次 promote 共享同一 dev-auto 用户（第二此后 LoadActor 缓存命中）。
	if len(identities.byEmail) != 1 {
		t.Fatalf("identity users = %#v; want exactly one dev-auto account", identities.byEmail)
	}
	if _, ok := identities.byEmail[DevAutoEmail]; !ok {
		t.Fatalf("dev-auto account missing: %#v", identities.byEmail)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// promoteOneForTest 直接驱动单包激活（测试入口；生产入口是 PromoteAll）。
func (r *DevAutoRuntime) promoteOneForTest(t *testing.T, extensionID string) {
	t.Helper()
	_ = r.promoteOne(context.Background(), extensionID)
}

func (a devAutoTxStoreAdapter) AnyUserExists(context.Context) (bool, error) { return false, nil }
func (a devAutoTxStoreAdapter) FindRegistrationConflicts(context.Context, string, string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{}, nil
}
func (a devAutoTxStoreAdapter) CreateCredential(context.Context, int64, string) error { return nil }
func (a devAutoTxStoreAdapter) GetDefaultRole(context.Context) (identity.Role, error) {
	return identity.Role{ID: 2, Key: "member"}, nil
}
func (a devAutoTxStoreAdapter) LoadCurrentUserAccess(context.Context, *identity.CurrentUser) error {
	return nil
}

// dev 通道的组件契约版本自动晋升：staged 组件的 contractVersion 在传给 upgrade
// 的内存副本上被改写为 "dev-<digest8>"，制品与真实版本号不变——registry 因此
// 把每次摘要变化视为新契约发布，作者改 L2 前端不再需要手动 bump。
func TestDevAutoRuntimeRewritesComponentContractVersions(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusEnabled
	extension.Type = TypePlugin
	extension.Manifest.Backend.ProtocolVersion = 2
	extension.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "sforum.lifecycle@1"}
	extension.Manifest.Components = []ManifestComponent{{
		ID: "sforum.oauth2-provider.component.apps", ContractVersion: "3",
	}}
	stagedDigest := strings.Repeat("ab", 32)
	extension.StagedVersion = &ExtensionVersion{
		ID: 77, Version: "2.0.0", PackageDigest: stagedDigest,
	}
	runtime, promoter, _, _ := newDevAutoTestRuntime(t, map[string]Extension{extension.ID: extension})

	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.upgradedIDs) != 1 {
		t.Fatalf("upgrade not called: %#v", promoter)
	}
	// 制品上的真实 manifest 不被改写（晋升发生在 Service.Upgrade 的 target 视图）。
	if extension.Manifest.Components[0].ContractVersion != "3" {
		t.Fatalf("artifact contractVersion mutated: %q", extension.Manifest.Components[0].ContractVersion)
	}
	// dev 通道必须使用确定性 dev-auto/ 键（Service.Upgrade 由此识别并晋升契约版本）。
	want := DevIdempotencyKey(extension.ID, stagedDigest)
	if len(promoter.upgradedKeys) != 1 || promoter.upgradedKeys[0] != want {
		t.Fatalf("keys = %#v; want [%s]", promoter.upgradedKeys, want)
	}
}

// coordinator 对 cancelled 与 failed 终态同等地要求显式 retry;恢复扫描必须
// 同时覆盖两者,否则 cancelled 的升级会让 dev auto 永远撞 RetryRequired。
func TestDevAutoRuntimeRecoversCancelledUpgrade(t *testing.T) {
	extension, staged := devAutoUpgradableExtension(t)
	promoter := &devAutoFakePromoter{
		firstUpgradeErr: ErrLifecycleCoordinatorRetryRequired,
		operations: []LifecycleOperationSummary{{
			ID: 361, ExtensionID: extension.ID,
			Operation: string(LifecycleMachineUpgrade), TerminalResult: LifecycleTerminalCancelled,
			PackageDigest: staged.PackageDigest, AttemptCount: 1,
		}},
	}
	runtime := &DevAutoRuntime{
		service:    NewService(&fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}, t.TempDir()),
		promoter:   promoter,
		identities: newDevAutoFakeIdentity(),
		grants:     &devAutoFakeGrants{},
		logger:     discardLogger(),
	}

	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.recoveredIDs) != 1 || promoter.recoveredIDs[0] != 361 {
		t.Fatalf("recovered = %#v; want [361]", promoter.recoveredIDs)
	}
	if promoter.upgradeCalls != 2 {
		t.Fatalf("upgrade calls = %d; want retry after recovery", promoter.upgradeCalls)
	}
	if len(promoter.upgradedIDs) != 1 || promoter.upgradedIDs[0] != extension.ID {
		t.Fatalf("upgraded = %#v", promoter.upgradedIDs)
	}
}

// 恢复扫描只认 upgrade 操作的失败/取消终态;其他操作或其他终态不得被误恢复。
func TestDevAutoRuntimeSkipsNonRetryableOperations(t *testing.T) {
	extension := devTrustTestExtension(t)
	extension.Status = StatusEnabled
	extension.Type = TypePlugin
	extension.Manifest.Backend.ProtocolVersion = 2
	extension.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "sforum.lifecycle@1"}
	extension.StagedVersion = &ExtensionVersion{ID: 77, Version: "2.0.0", PackageDigest: strings.Repeat("ab", 32)}
	promoter := &devAutoFakePromoter{
		operations: []LifecycleOperationSummary{
			{ID: 1, ExtensionID: extension.ID, Operation: string(LifecycleMachineEnable), TerminalResult: LifecycleTerminalFailed},
			{ID: 2, ExtensionID: extension.ID, Operation: string(LifecycleMachineUpgrade), TerminalResult: LifecycleTerminalSucceeded},
		},
	}
	runtime := &DevAutoRuntime{
		service:    NewService(&fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}, t.TempDir()),
		promoter:   promoter,
		identities: newDevAutoFakeIdentity(),
		grants:     &devAutoFakeGrants{},
		logger:     discardLogger(),
	}

	// 正常 Upgrade 成功路径不进入 recovery 分支;若实现误扫,也无法断言
	// recoveredIDs 非空——用一次 promote 验证零恢复。
	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.recoveredIDs) != 0 {
		t.Fatalf("unexpected recovery: %#v", promoter.recoveredIDs)
	}
	if len(promoter.upgradedIDs) != 1 {
		t.Fatalf("upgraded = %#v", promoter.upgradedIDs)
	}
}

// devAutoUpgradableExtension 构造一个「已启用、声明 lifecycle V2、且带着一份
// 真实可装载 staged 制品」的包。放弃闸门依赖磁盘事实，所以 fixtures 必须是
// 真实临时包：目录在、摘要对得上、backend entry 存在。
func devAutoUpgradableExtension(t *testing.T) (Extension, *ExtensionVersion) {
	t.Helper()
	extension := devTrustTestExtension(t)
	extension.Status = StatusEnabled
	extension.Type = TypePlugin
	extension.Manifest.Backend.ProtocolVersion = 2
	extension.Manifest.Lifecycle = &ManifestLifecycle{ContractVersion: "sforum.lifecycle@1"}
	stagedPackage := devTrustTestExtension(t)
	staged := &ExtensionVersion{
		ID: 77, Version: "2.0.0", PackageDigest: stagedPackage.PackageDigest,
		PackagePath: stagedPackage.PackagePath, Manifest: stagedPackage.Manifest,
	}
	extension.StagedVersion = staged
	return extension, staged
}

func newDevAutoRuntimeWithStore(t *testing.T, extension Extension) (*DevAutoRuntime, *devAutoFakePromoter, *fakeExtensionStore) {
	t.Helper()
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	promoter := &devAutoFakePromoter{}
	runtime := &DevAutoRuntime{
		service:    NewService(store, t.TempDir()),
		promoter:   promoter,
		identities: newDevAutoFakeIdentity(),
		grants:     &devAutoFakeGrants{},
		logger:     discardLogger(),
	}
	return runtime, promoter, store
}

func devAutoSkippedEvents(store *fakeExtensionStore) []ExtensionEvent {
	skipped := make([]ExtensionEvent, 0)
	for _, event := range store.events {
		if event.Action == EventDevAutoSkipped {
			skipped = append(skipped, event)
		}
	}
	return skipped
}

// 冻结制品不可装载（目录被删、摘要漂移、manifest 坏了）时继续重试只会复现同一
// 终态失败——现场曾把 operation 697 推到 attempt=98、97 条恢复决定。闸门必须在
// 恢复之前生效：只写一条放弃台账，不恢复、不二次升级。
func TestDevAutoRuntimeGivesUpWhenStagedArtifactUnloadable(t *testing.T) {
	extension, staged := devAutoUpgradableExtension(t)
	if err := os.RemoveAll(staged.PackagePath); err != nil {
		t.Fatal(err)
	}
	runtime, promoter, store := newDevAutoRuntimeWithStore(t, extension)
	promoter.firstUpgradeErr = ErrLifecycleCoordinatorRetryRequired
	promoter.operations = []LifecycleOperationSummary{{
		ID: 697, ExtensionID: extension.ID,
		Operation: string(LifecycleMachineUpgrade), TerminalResult: LifecycleTerminalFailed,
		PackageDigest: staged.PackageDigest, AttemptCount: 1,
	}}

	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.recoveredIDs) != 0 {
		t.Fatalf("unloadable artifact must not be recovered: %#v", promoter.recoveredIDs)
	}
	if promoter.upgradeCalls != 1 {
		t.Fatalf("upgrade calls = %d; want a single diagnostic attempt", promoter.upgradeCalls)
	}
	skipped := devAutoSkippedEvents(store)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Message, "not loadable") {
		t.Fatalf("expected one give-up event with a loadability reason: %+v", skipped)
	}

	// 下一次文件事件不得重复刷台账（同一冻结摘要只留一条）。
	runtime.promoteOneForTest(t, extension.ID)
	if got := len(devAutoSkippedEvents(store)); got != 1 {
		t.Fatalf("give-up note repeated: %d", got)
	}
}

// 制品可装载但同一摘要已经被自动恢复过上限次数：同样直接放弃。
func TestDevAutoRuntimeGivesUpAfterRepeatedRecoveries(t *testing.T) {
	extension, staged := devAutoUpgradableExtension(t)
	runtime, promoter, store := newDevAutoRuntimeWithStore(t, extension)
	promoter.firstUpgradeErr = ErrLifecycleCoordinatorRetryRequired
	promoter.operations = []LifecycleOperationSummary{{
		ID: 697, ExtensionID: extension.ID,
		Operation: string(LifecycleMachineUpgrade), TerminalResult: LifecycleTerminalFailed,
		PackageDigest: staged.PackageDigest, AttemptCount: devAutoMaxFailedUpgradeAttempts,
	}}

	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.recoveredIDs) != 0 || promoter.upgradeCalls != 1 {
		t.Fatalf("exhausted artifact must give up: recovered=%#v calls=%d", promoter.recoveredIDs, promoter.upgradeCalls)
	}
	skipped := devAutoSkippedEvents(store)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Message, "without success") {
		t.Fatalf("expected one attempt-cap give-up event: %+v", skipped)
	}
}

// 只恢复「当前冻结摘要」的失败操作：回头恢复别的制品的历史失败会污染操作历史。
func TestDevAutoRuntimeIgnoresFailedUpgradeForAnotherDigest(t *testing.T) {
	extension, _ := devAutoUpgradableExtension(t)
	runtime, promoter, _ := newDevAutoRuntimeWithStore(t, extension)
	promoter.firstUpgradeErr = ErrLifecycleCoordinatorRetryRequired
	promoter.operations = []LifecycleOperationSummary{{
		ID: 429, ExtensionID: extension.ID,
		Operation: string(LifecycleMachineUpgrade), TerminalResult: LifecycleTerminalFailed,
		PackageDigest: strings.Repeat("cd", 32), AttemptCount: 1,
	}}

	runtime.promoteOneForTest(t, extension.ID)

	if len(promoter.recoveredIDs) != 0 {
		t.Fatalf("recovered an unrelated digest: %#v", promoter.recoveredIDs)
	}
}

func TestStagedArtifactUsableRejectsUnloadableArtifacts(t *testing.T) {
	extension, _ := devAutoUpgradableExtension(t)
	staged, ok := extension.StagedArtifact()
	if !ok {
		t.Fatal("fixture must stage an artifact")
	}
	if err := stagedArtifactUsable(staged); err != nil {
		t.Fatalf("loadable artifact rejected: %v", err)
	}

	missing := staged
	missing.PackagePath = filepath.Join(t.TempDir(), "gone")
	if err := stagedArtifactUsable(missing); !errors.Is(err, ErrArtifactUnusable) {
		t.Fatalf("missing artifact error=%v", err)
	}

	mismatched := staged
	mismatched.ID = "other.extension"
	if err := stagedArtifactUsable(mismatched); !errors.Is(err, ErrArtifactUnusable) ||
		!strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity mismatch error=%v", err)
	}

	broken, _ := devAutoUpgradableExtension(t)
	brokenStaged, _ := broken.StagedArtifact()
	if err := os.WriteFile(filepath.Join(brokenStaged.PackagePath, extensionmanifest.ManifestFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stagedArtifactUsable(brokenStaged); !errors.Is(err, ErrArtifactUnusable) ||
		!strings.Contains(err.Error(), "manifest") {
		t.Fatalf("unreadable manifest error=%v", err)
	}

	drifted, _ := devAutoUpgradableExtension(t)
	driftedStaged, _ := drifted.StagedArtifact()
	if err := os.WriteFile(filepath.Join(driftedStaged.PackagePath, "drift.txt"), []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stagedArtifactUsable(driftedStaged); !errors.Is(err, ErrArtifactUnusable) ||
		!strings.Contains(err.Error(), "frozen digest") {
		t.Fatalf("digest drift error=%v", err)
	}
}

func (p *devAutoFakePromoter) LifecycleOperations(_ context.Context, _ identity.Actor, _ string, _ int) ([]LifecycleOperationSummary, error) {
	return p.operations, nil
}
func (p *devAutoFakePromoter) RecoverLifecycleOperation(
	_ context.Context, _ identity.Actor, _ string, operationID int64, _ LifecycleRecoveryInput,
) (LifecycleOperationDetail, error) {
	p.recoveredIDs = append(p.recoveredIDs, operationID)
	return LifecycleOperationDetail{}, nil
}
