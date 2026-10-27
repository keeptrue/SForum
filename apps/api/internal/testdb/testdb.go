// Package testdb 是数据库测试唯一允许的连接来源解析器。
//
// 规则只有一条：数据库测试只认 SFORUM_TEST_DATABASE_URL，永不继承
// DATABASE_URL。历史上测试直接读 DATABASE_URL，在 source 过 .env 的 shell 里
// 跑 go test 就把测试扩展写进了开发库并启用，导致 plugin runtime 收敛失败、
// 全站 503。因此本包对"看起来像开发库"的连接串一律 fail closed：
//
//   - 连接串为空：跳过（没人要求跑数据库测试）；
//   - 连不上：拒绝（无法验证就不跑）；
//   - 命中开发库哨兵表 public.sforum_dev_guard：拒绝；
//   - 与 DATABASE_URL 完全相同：拒绝，除非集群带一次性测试集群标记；
//   - 集群里存在 sforum 开发库：拒绝。
//
// 正规入口是 scripts/test-api-isolated.sh，它建一次性集群、写集群标记，并同时
// 导出 DATABASE_URL 与 SFORUM_TEST_DATABASE_URL 指向该集群。
package testdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb/guard"
)

const (
	// EnvTestDatabaseURL 是数据库测试唯一读取的环境变量。
	EnvTestDatabaseURL = "SFORUM_TEST_DATABASE_URL"
	// EnvDevelopmentDatabaseURL 是开发/生产连接串，测试永不读取它的值。
	EnvDevelopmentDatabaseURL = "DATABASE_URL"

	probeTimeout = 10 * time.Second
)

// Outcome 是隔离判定的结果。
type Outcome int

const (
	// Allow 表示连接串可以用于数据库测试。
	Allow Outcome = iota
	// Skip 表示没有要求跑数据库测试。
	Skip
	// Refuse 表示连接串不安全，必须中止测试。
	Refuse
)

// Decision 是判定结果与理由。
type Decision struct {
	Outcome Outcome
	Reason  string
}

// Decide 是纯策略：不做任何 I/O，因此可以完整覆盖允许与拒绝两侧。
// state 与 probeErr 是真实探测的产物；testURL 与 developmentURL 是环境变量值。
func Decide(testURL, developmentURL string, state guard.State, probeErr error) Decision {
	if testURL == "" {
		return Decision{
			Outcome: Skip,
			Reason:  EnvTestDatabaseURL + " is not set; run database tests through ./scripts/test-api-isolated.sh",
		}
	}
	if probeErr != nil {
		return Decision{
			Outcome: Refuse,
			Reason: fmt.Sprintf(
				"cannot verify %s %s is an isolated test database: %v",
				EnvTestDatabaseURL, guard.Redact(testURL), probeErr,
			),
		}
	}
	if state.Sentinel {
		return Decision{
			Outcome: Refuse,
			Reason: fmt.Sprintf(
				"refusing to run tests against the development database: %s exists on %s; use ./scripts/test-api-isolated.sh",
				guard.SentinelTable, guard.Redact(testURL),
			),
		}
	}
	if state.Disposable {
		return Decision{Outcome: Allow}
	}
	if developmentURL != "" && developmentURL == testURL {
		return Decision{
			Outcome: Refuse,
			Reason: fmt.Sprintf(
				"%s and %s point at the same database (%s); database tests need their own disposable cluster, use ./scripts/test-api-isolated.sh",
				EnvTestDatabaseURL, EnvDevelopmentDatabaseURL, guard.Redact(testURL),
			),
		}
	}
	if state.DevelopmentDatabase {
		return Decision{
			Outcome: Refuse,
			Reason: fmt.Sprintf(
				"refusing to run tests against a cluster that hosts the %q development database; use ./scripts/test-api-isolated.sh",
				guard.DevelopmentDatabaseName,
			),
		}
	}
	return Decision{Outcome: Allow}
}

// IsolatedURL 返回数据库测试可用的连接串；没有配置时跳过测试，不安全时中止。
func IsolatedURL(t testing.TB) string {
	t.Helper()
	return resolve(t, false)
}

// RequireIsolatedURL 与 IsolatedURL 相同，但没有配置时也中止测试。
//
// 供被显式开关（例如 SFORUM_PUBLIC_L2_E2E=1）打开、且子进程会复用的场景使用：
// 那里静默跳过会让父测试误判为通过。
func RequireIsolatedURL(t testing.TB) string {
	t.Helper()
	return resolve(t, true)
}

func resolve(t testing.TB, required bool) string {
	t.Helper()
	testURL := strings.TrimSpace(os.Getenv(EnvTestDatabaseURL))
	if testURL == "" && required {
		t.Fatalf("%s is required but not set; use ./scripts/test-api-isolated.sh", EnvTestDatabaseURL)
	}
	return ValidatedURL(t, testURL)
}

// ValidatedURL 用与 IsolatedURL 完全相同的探测与判定，校验调用方自己解析出来的
// 连接串（例如 CI 的 SFORUM_COMPAT_DATABASE_URL）。没有这条，换一个变量名就能
// 绕过整套守卫——变量名不是安全边界。
func ValidatedURL(t testing.TB, raw string) string {
	t.Helper()
	testURL := strings.TrimSpace(raw)
	developmentURL := strings.TrimSpace(os.Getenv(EnvDevelopmentDatabaseURL))
	decision := Decide(testURL, developmentURL, probe(testURL), probeError(testURL))
	switch decision.Outcome {
	case Skip:
		t.Skip(decision.Reason)
	case Refuse:
		t.Fatalf("%s", decision.Reason)
	}
	return testURL
}

// probeCache 缓存按连接串的探测结果：同一进程里所有测试连的是同一个集群，
// 状态不会在一次运行中改变，重复连接只会拖慢测试。
var probeCache sync.Map // url -> probeResult

type probeResult struct {
	state guard.State
	err   error
}

func probe(url string) guard.State {
	return probeOnce(url).state
}

func probeError(url string) error {
	return probeOnce(url).err
}

func probeOnce(url string) probeResult {
	if url == "" {
		return probeResult{}
	}
	if cached, ok := probeCache.Load(url); ok {
		return cached.(probeResult)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	db, err := guard.Open(ctx, url)
	if err != nil {
		result := probeResult{err: err}
		probeCache.Store(url, result)
		return result
	}
	defer db.Close()
	state, err := guard.Inspect(ctx, db)
	result := probeResult{state: state, err: err}
	probeCache.Store(url, result)
	return result
}
