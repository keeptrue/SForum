package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// memoryRedis 极简 INCR/EVAL/EXISTS/SET/DEL 模拟，避免为测试引入 miniredis。
// Eval 只实现计数自增：TTL 语义不在这里假装，由
// TestLoginBumpScriptRepairsKeysWithoutTTL（脚本静态守卫）与
// TestLoginLockoutTTLAgainstRedis（真实 Redis）覆盖。
type memoryRedis struct {
	redis.Scripter
	mu     sync.Mutex
	vals   map[string]string
	counts map[string]int64
	err    error
}

func (m *memoryRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return redis.NewIntResult(0, m.err)
	}
	var count int64
	for _, key := range keys {
		if _, ok := m.vals[key]; ok {
			count++
		}
	}
	return redis.NewIntResult(count, nil)
}

func (m *memoryRedis) Eval(_ context.Context, _ string, keys []string, _ ...any) *redis.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return redis.NewCmdResult(int64(0), m.err)
	}
	key := keys[0]
	m.counts[key]++
	return redis.NewCmdResult(m.counts[key], nil)
}

// EvalSha 返回 NOSCRIPT，让 redis.Script.Run 回落到 EVAL（真实 Redis 首次执行同路）。
func (m *memoryRedis) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(int64(0), redis.ErrNoScript)
}

func (m *memoryRedis) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return redis.NewStatusResult("", m.err)
	}
	m.vals[key] = fmt.Sprint(value)
	return redis.NewStatusResult("OK", nil)
}

func (m *memoryRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return redis.NewIntResult(0, m.err)
	}
	var count int64
	for _, key := range keys {
		if _, ok := m.vals[key]; ok {
			delete(m.vals, key)
			count++
		}
		delete(m.counts, key)
	}
	return redis.NewIntResult(count, nil)
}

func newMemoryRedis() *memoryRedis {
	return &memoryRedis{vals: map[string]string{}, counts: map[string]int64{}}
}

// 通过 redis.NewClient 无法注入；改为测试可观测的哈希与阈值逻辑，以及
// 用 LoginLockout 的公开 helper 验证 key 不含明文。

func TestHashIDDoesNotLeakRawEmail(t *testing.T) {
	email := "User@Example.com"
	h := hashID(email)
	if h == "" || strings.Contains(h, "user") || strings.Contains(strings.ToLower(h), "example") {
		t.Fatalf("bad hash %q", h)
	}
	// 稳定
	if hashID(email) != h {
		t.Fatal("hash not stable")
	}
	// 与 sha256 前缀一致
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	want := hex.EncodeToString(sum[:16])
	if h != want {
		t.Fatalf("got %s want %s", h, want)
	}
}

func TestRedisKeyShapesUseHashesOnly(t *testing.T) {
	login := "victim@example.com"
	ip := "203.0.113.10"
	lh, ih := hashID(login), hashID(normalizeIP(ip))
	keys := []string{
		pairFailKey(lh, ih), pairLockKey(lh, ih),
		ipFailKey(ih), ipLockKey(ih),
		accountFailKey(lh), accountVerificationKey(lh),
	}
	for _, k := range keys {
		if strings.Contains(k, login) || strings.Contains(k, "victim") || strings.Contains(k, "@") {
			t.Fatalf("key leaks identity: %s", k)
		}
		if !strings.HasPrefix(k, "sforum:login_") {
			t.Fatalf("unexpected prefix: %s", k)
		}
	}
}

func TestThresholdPolicyAccountVerificationHigherThanPair(t *testing.T) {
	// 账号阈值只触发验证，不产生 account lock。
	max := 5
	pairMax := max
	ipMax := max * 5
	if ipMax < 20 {
		ipMax = 20
	}
	accountMax := max * 3
	if accountMax < pairMax+2 {
		accountMax = pairMax + 2
	}
	if !(pairMax < accountMax && pairMax < ipMax) {
		t.Fatalf("pair=%d account=%d ip=%d", pairMax, accountMax, ipMax)
	}
}

func TestDistributedFailuresRequireVerificationWithoutAccountLock(t *testing.T) {
	client := newMemoryRedis()
	lockout := &LoginLockout{client: client}
	for i := 0; i < 6; i++ {
		ip := fmt.Sprintf("203.0.113.%d", i+1)
		if err := lockout.RecordFailure(context.Background(), "victim@example.com", ip, 2, 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	locked, err := lockout.IsLocked(context.Background(), "victim@example.com", "198.51.100.10")
	if err != nil || locked {
		t.Fatalf("fresh source must not be account-locked: locked=%v err=%v", locked, err)
	}
	required, err := lockout.RequiresVerification(context.Background(), "victim@example.com")
	if err != nil || !required {
		t.Fatalf("distributed failures must require verification: required=%v err=%v", required, err)
	}
}

func TestPairAndIPLocksRemainEffective(t *testing.T) {
	client := newMemoryRedis()
	lockout := &LoginLockout{client: client}
	for i := 0; i < 2; i++ {
		_ = lockout.RecordFailure(context.Background(), "victim@example.com", "203.0.113.10", 2, time.Minute)
	}
	locked, err := lockout.IsLocked(context.Background(), "victim@example.com", "203.0.113.10")
	if err != nil || !locked {
		t.Fatalf("pair must lock: locked=%v err=%v", locked, err)
	}
}

func TestLoginLockoutRedisFailureFailsOpen(t *testing.T) {
	lockout := &LoginLockout{client: &memoryRedis{vals: map[string]string{}, counts: map[string]int64{}, err: errors.New("redis down")}}
	locked, err := lockout.IsLocked(context.Background(), "victim", "203.0.113.10")
	if err != nil || locked {
		t.Fatalf("redis failure must fail open: locked=%v err=%v", locked, err)
	}
	required, err := lockout.RequiresVerification(context.Background(), "victim")
	if err != nil || required {
		t.Fatalf("verification state must fail open: required=%v err=%v", required, err)
	}
	if err := lockout.RecordFailure(context.Background(), "victim", "203.0.113.10", 2, time.Minute); err != nil {
		t.Fatalf("record failure must fail open: %v", err)
	}
}

func TestLoginLockoutNilFailOpen(t *testing.T) {
	var l *LoginLockout
	locked, err := l.IsLocked(context.Background(), "x", "1.1.1.1")
	if err != nil || locked {
		t.Fatalf("nil: locked=%v err=%v", locked, err)
	}
	if err := l.RecordFailure(context.Background(), "x", "1.1.1.1", 3, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := l.ClearFailures(context.Background(), "x", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
}

func TestNewLoginLockoutNilClient(t *testing.T) {
	if NewLoginLockout(nil) != nil {
		t.Fatal("expected nil")
	}
}

// recordingLockoutRedis 记录命令序列与 EVAL 载荷，用来区分「一次原子脚本」
// 与「INCR + EXPIRE 两条命令」——与 HumanVerify/redis_store_rate_test.go 同款。
type recordingLockoutRedis struct {
	redis.Scripter
	commands []string
	evals    []recordedBump
	counts   map[string]int64
	vals     map[string]string
	err      error
}

type recordedBump struct {
	script string
	key    string
	args   []any
}

func newRecordingLockoutRedis() *recordingLockoutRedis {
	return &recordingLockoutRedis{counts: map[string]int64{}, vals: map[string]string{}}
}

func (c *recordingLockoutRedis) Eval(_ context.Context, script string, keys []string, args ...any) *redis.Cmd {
	c.commands = append(c.commands, "eval")
	c.evals = append(c.evals, recordedBump{script: script, key: keys[0], args: args})
	if c.err != nil {
		return redis.NewCmdResult(int64(0), c.err)
	}
	c.counts[keys[0]]++
	return redis.NewCmdResult(c.counts[keys[0]], nil)
}

// EvalSha 返回 NOSCRIPT，让 redis.Script.Run 回落到 EVAL（真实 Redis 首次执行同路）。
func (c *recordingLockoutRedis) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(int64(0), redis.ErrNoScript)
}

func (c *recordingLockoutRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	var count int64
	for _, key := range keys {
		if _, ok := c.vals[key]; ok {
			count++
		}
	}
	return redis.NewIntResult(count, c.err)
}

func (c *recordingLockoutRedis) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	c.commands = append(c.commands, "set")
	if c.err != nil {
		return redis.NewStatusResult("", c.err)
	}
	c.vals[key] = fmt.Sprint(value)
	return redis.NewStatusResult("OK", nil)
}

func (c *recordingLockoutRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	c.commands = append(c.commands, "del")
	var count int64
	for _, key := range keys {
		if _, ok := c.vals[key]; ok {
			delete(c.vals, key)
			count++
		}
		delete(c.counts, key)
	}
	return redis.NewIntResult(count, c.err)
}

// ① 首次失败就要有 TTL：计数与过期必须是同一次 EVAL，且 TTL 以毫秒传入。
//
// 注意这个 fake 的 Eval **不执行脚本**，它只把 EVAL 调用记下来并做计数自增：
// 本用例证明的是「一次 EVAL + 毫秒级 ARGV + 正确的键」，不证明脚本里的过期
// 分支真的会跑。脚本行为由 TestLoginBumpScriptRepairsKeysWithoutTTL（形状）与
// TestLoginLockoutTTLAgainstRedis（真实 PTTL）负责，三者缺一不可。
func TestRecordFailureBumpIsSingleAtomicEval(t *testing.T) {
	client := newRecordingLockoutRedis()
	lockout := &LoginLockout{client: client}
	login, ip := "victim@example.com", "203.0.113.10"

	if err := lockout.RecordFailure(context.Background(), login, ip, 5, 15*time.Minute); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	for _, command := range client.commands {
		if command != "eval" {
			t.Fatalf("commands = %v; count and TTL must be one atomic EVAL, saw %q", client.commands, command)
		}
	}
	if len(client.evals) != 3 {
		t.Fatalf("eval count = %d, want 3 (pair, ip, account)", len(client.evals))
	}

	lh, ih := hashID(login), hashID(normalizeIP(ip))
	want := []struct {
		key    string
		ttlMS  int64
		reason string
	}{
		{pairFailKey(lh, ih), (15 * time.Minute).Milliseconds(), "pair"},
		{ipFailKey(ih), (15 * time.Minute).Milliseconds(), "ip"},
		{accountFailKey(lh), (15 * time.Minute / 2).Milliseconds(), "account (half ttl)"},
	}
	for i, expect := range want {
		got := client.evals[i]
		if got.key != expect.key {
			t.Fatalf("%s key = %q, want %q", expect.reason, got.key, expect.key)
		}
		if len(got.args) != 1 || got.args[0] != expect.ttlMS {
			t.Fatalf("%s ttl arg = %v, want [%d] ms", expect.reason, got.args, expect.ttlMS)
		}
	}
}

// ② 键已存在但没有 TTL（历史遗留 / 上一版 INCR+EXPIRE 之间断连）时，
// 下一次失败必须把 TTL 补上：脚本里的 `PTTL < 0` 分支就是这件事。
// 这里断言脚本形状，真实 PTTL 行为见 TestLoginLockoutTTLAgainstRedis。
func TestLoginBumpScriptRepairsKeysWithoutTTL(t *testing.T) {
	client := newRecordingLockoutRedis()
	lockout := &LoginLockout{client: client}
	if err := lockout.RecordFailure(context.Background(), "victim@example.com", "203.0.113.10", 5, time.Minute); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	script := client.evals[0].script
	for _, want := range []string{
		"redis.call('INCR', KEYS[1])",
		"count == 1",
		"redis.call('PTTL', KEYS[1]) < 0",
		"redis.call('PEXPIRE', KEYS[1], ARGV[1])",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bump script lost %q; keys without TTL would never expire:\n%s", want, script)
		}
	}
}

// ③ 阈值仍触发锁定（不回归）：达到 max 后写 lockKey 并清掉计数键。
func TestRecordFailureStillLocksAtThreshold(t *testing.T) {
	client := newRecordingLockoutRedis()
	lockout := &LoginLockout{client: client}
	login, ip := "victim@example.com", "203.0.113.10"
	lh, ih := hashID(login), hashID(normalizeIP(ip))

	for i := 0; i < 2; i++ {
		if err := lockout.RecordFailure(context.Background(), login, ip, 2, time.Minute); err != nil {
			t.Fatalf("record failure %d: %v", i+1, err)
		}
	}
	if _, ok := client.vals[pairLockKey(lh, ih)]; !ok {
		t.Fatalf("pair lock key not written at threshold: %v", client.vals)
	}
	if _, ok := client.counts[pairFailKey(lh, ih)]; ok {
		t.Fatal("pair fail counter must be cleared once the lock is set")
	}
	locked, err := lockout.IsLocked(context.Background(), login, ip)
	if err != nil || !locked {
		t.Fatalf("pair must be locked: locked=%v err=%v", locked, err)
	}
}

// TestLoginLockoutTTLAgainstRedis 是 PTTL 的唯一真实证据（单元测试只能断言脚本
// 形状，没有 Lua 引擎）。
//
// 没有 SFORUM_TEST_REDIS_ADDR 时**显式失败**而不是 t.Skip：`go test` 在非 -v
// 输出里对 skip 只打一行 `ok`，等于这条用例从不运行。实测后果是「把 Lua 改成
// 语法错误」也能全绿上线（P3-2 复审抓到的回归洞）。一次性 Redis 由
// scripts/test-api-isolated.sh 提供；手工跑请先起一个 disposable Redis 并
// export SFORUM_TEST_REDIS_ADDR。
func TestLoginLockoutTTLAgainstRedis(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_ADDR"))
	if addr == "" {
		t.Fatalf("SFORUM_TEST_REDIS_ADDR is required: without a disposable Redis the lockout TTL behaviour has no evidence at all (run ./scripts/test-api-isolated.sh, or export SFORUM_TEST_REDIS_ADDR for a throwaway Redis)")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping disposable redis: %v", err)
	}
	lockout := &LoginLockout{client: client}

	t.Run("first failure sets ttl", func(t *testing.T) {
		login, ip := integrationLogin(t), "203.0.113.21"
		lh, ih := hashID(login), hashID(normalizeIP(ip))
		t.Cleanup(func() {
			client.Del(ctx, pairFailKey(lh, ih), ipFailKey(ih), accountFailKey(lh))
		})
		if err := lockout.RecordFailure(ctx, login, ip, 5, time.Minute); err != nil {
			t.Fatalf("record failure: %v", err)
		}
		if ttl := client.PTTL(ctx, pairFailKey(lh, ih)).Val(); ttl <= 0 || ttl > time.Minute {
			t.Fatalf("pair fail key pttl = %v, want (0, 1m]", ttl)
		}
	})

	t.Run("legacy key without ttl is repaired", func(t *testing.T) {
		login, ip := integrationLogin(t), "203.0.113.22"
		lh, ih := hashID(login), hashID(normalizeIP(ip))
		key := accountFailKey(lh)
		t.Cleanup(func() {
			client.Del(ctx, pairFailKey(lh, ih), ipFailKey(ih), key, accountVerificationKey(lh))
		})
		// 模拟历史遗留：键已存在，但没有 TTL。
		if err := client.Set(ctx, key, 1, 0).Err(); err != nil {
			t.Fatalf("seed legacy key: %v", err)
		}
		if ttl := client.PTTL(ctx, key).Val(); ttl != -1 {
			t.Fatalf("precondition: legacy key pttl = %v, want -1 (no ttl)", ttl)
		}
		if err := lockout.RecordFailure(ctx, login, ip, 5, time.Minute); err != nil {
			t.Fatalf("record failure: %v", err)
		}
		if ttl := client.PTTL(ctx, key).Val(); ttl <= 0 || ttl > time.Minute {
			t.Fatalf("legacy key not repaired: pttl = %v, want (0, 1m]", ttl)
		}
		if got := client.Get(ctx, key).Val(); got != "2" {
			t.Fatalf("legacy counter = %q, want \"2\" (repair must not reset the count)", got)
		}
	})

	t.Run("threshold still locks", func(t *testing.T) {
		login, ip := integrationLogin(t), "203.0.113.23"
		lh, ih := hashID(login), hashID(normalizeIP(ip))
		t.Cleanup(func() {
			client.Del(ctx, pairFailKey(lh, ih), pairLockKey(lh, ih), ipFailKey(ih), ipLockKey(ih), accountFailKey(lh))
		})
		for i := 0; i < 2; i++ {
			if err := lockout.RecordFailure(ctx, login, ip, 2, time.Minute); err != nil {
				t.Fatalf("record failure %d: %v", i+1, err)
			}
		}
		locked, err := lockout.IsLocked(ctx, login, ip)
		if err != nil || !locked {
			t.Fatalf("pair must be locked: locked=%v err=%v", locked, err)
		}
		if ttl := client.PTTL(ctx, pairLockKey(lh, ih)).Val(); ttl <= 0 || ttl > time.Minute {
			t.Fatalf("pair lock pttl = %v, want (0, 1m]", ttl)
		}
		if exists := client.Exists(ctx, pairFailKey(lh, ih)).Val(); exists != 0 {
			t.Fatalf("pair fail counter must be cleared once locked, exists = %d", exists)
		}
	})
}

// integrationLogin 给每个子用例独立身份，避免共用 Redis 时互相污染计数。
func integrationLogin(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("lockout-it-%s-%d@example.com", t.Name(), time.Now().UnixNano())
}
