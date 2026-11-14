package trustlevels_test

import (
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestLoaderLatencyAB 是**可选段**（`SFORUM_TRUST_PERF=1`）：10k 夹具，只做分位与
// 5ms 预算。它不是反 stub 判据——那条判据在常开段
// （loader_queries_postgres_integration_test.go），这里只补规模与分布。
//
// 口径：本测试量的是**装载路径**（GetCurrentUser 全链）的 A/B 增量；规则单因子成本
// 见 rule_eval 的 TestRuleEvaluationLatency，两者不可互换。
//
// 读法按「机制 + 区间」，不写点值：
//  1. 机制：B 相对 A 只多 **1 条主键点查**（规则侧 SELECT users.created_at），
//     与「一处主键索引读」同量级；
//  2. 区间：本单 + 小审共 9 次独立全量运行，ΔP95 ∈ [-0.41ms, +1.0ms]；最坏仍 ≪ 5ms
//     预算，所以「装载路径 ΔP95 ≤ 5ms」可标已证；
//  3. 单次运行的 ΔP95 会换号（尾部由夹具机抖动主导，两侧都出现过 ~5ms 的单轮 P95），
//     所以任何一次运行的点值都不作为结论。
//
// 运行（显式路径，脚本不分片）：
//
//	SFORUM_TRUST_PERF=1 ./scripts/test-api-isolated.sh ./app/Models/TrustLevels/... \
//	    -run TestLoaderLatencyAB -v
//
// 可选环境变量：SFORUM_TRUST_PERF_USERS（默认 10000）、
// SFORUM_TRUST_PERF_SAMPLE（默认 1000）、SFORUM_TRUST_PERF_ROUNDS（默认 4）。
func TestLoaderLatencyAB(t *testing.T) {
	if os.Getenv("SFORUM_TRUST_PERF") != "1" {
		t.Skip("set SFORUM_TRUST_PERF=1 to run the 10k loader-path A/B latency fixture")
	}
	config := loadLoaderPerfConfig(t)
	fixture := newLoaderFixture(t, config.users)
	policyA, policyB, _ := loaderPolicies(t)
	ids := fixture.sampleIDs(t, config.sample)

	// 采样：轮次交替 A/B，逐轮翻转顺序，抵消时间漂移。
	samplesA := make([]time.Duration, 0, config.sample*config.rounds)
	samplesB := make([]time.Duration, 0, config.sample*config.rounds)
	for round := 0; round < config.rounds; round++ {
		first, second := "A", "B"
		if round%2 == 1 {
			first, second = "B", "A"
		}
		roundA := make([]time.Duration, 0, config.sample)
		roundB := make([]time.Duration, 0, config.sample)
		for _, side := range []string{first, second} {
			policy := policyA
			if side == "B" {
				policy = policyB
			}
			fixture.setPolicy(t, policy)
			fixture.load(t, ids[0]) // warmup：重建 options 缓存 + 复用已建连接
			for _, id := range ids {
				elapsed := fixture.load(t, id)
				if side == "A" {
					roundA = append(roundA, elapsed)
				} else {
					roundB = append(roundB, elapsed)
				}
			}
		}
		samplesA = append(samplesA, roundA...)
		samplesB = append(samplesB, roundB...)
		logLoaderRound(t, round, first, second, roundA, roundB)
	}

	sort.Slice(samplesA, func(i, j int) bool { return samplesA[i] < samplesA[j] })
	sort.Slice(samplesB, func(i, j int) bool { return samplesB[i] < samplesB[j] })

	p50A, p95A, p99A := loaderPercentile(samplesA, 0.50), loaderPercentile(samplesA, 0.95), loaderPercentile(samplesA, 0.99)
	p50B, p95B, p99B := loaderPercentile(samplesB, 0.50), loaderPercentile(samplesB, 0.95), loaderPercentile(samplesB, 0.99)

	t.Logf("夹具：users=%d 采样=%d 轮次=%d 每侧样本=%d（轮次交替换序，每相位先 warmup 1 次不计入）",
		config.users, len(ids), config.rounds, len(samplesA))
	t.Logf("装载路径 A(无规则)      P50=%s P95=%s P99=%s", p50A, p95A, p99A)
	t.Logf("装载路径 B(档位带规则)  P50=%s P95=%s P99=%s", p50B, p95B, p99B)
	t.Logf("本次运行增量 ΔP50=%s ΔP95=%s ΔP99=%s（预算 ΔP95 ≤ 5ms；单次点值不作结论，见文件头区间）",
		p50B-p50A, p95B-p95A, p99B-p99A)
	t.Logf("口径：本次测的是装载路径增量；规则单因子成本见 rule_eval 的 TestRuleEvaluationLatency，两者不可互换")

	if delta := p95B - p95A; delta > 5*time.Millisecond {
		t.Fatalf("装载路径 P95 增量 %s 超过 5ms 预算：先 EXPLAIN (ANALYZE, BUFFERS) 找缺失索引，不要加缓存绕过", delta)
	}
}

// loaderPerfConfig 是可选段的规模参数。
type loaderPerfConfig struct {
	users  int
	sample int
	rounds int
}

func loadLoaderPerfConfig(t *testing.T) loaderPerfConfig {
	t.Helper()
	config := loaderPerfConfig{users: 10000, sample: 1000, rounds: 4}
	readInt := func(env string, target *int) {
		raw := os.Getenv(env)
		if raw == "" {
			return
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("%s 非法：%q", env, raw)
		}
		*target = parsed
	}
	readInt("SFORUM_TRUST_PERF_USERS", &config.users)
	readInt("SFORUM_TRUST_PERF_SAMPLE", &config.sample)
	readInt("SFORUM_TRUST_PERF_ROUNDS", &config.rounds)
	if config.sample > config.users {
		config.sample = config.users
	}
	return config
}

// logLoaderRound 打一轮的 A/B 分布（入参切片会被就地排序，只用于展示）。
func logLoaderRound(t *testing.T, round int, first, second string, a, b []time.Duration) {
	t.Helper()
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	t.Logf("round %d：先 %s 后 %s  A P50=%s P95=%s  B P50=%s P95=%s",
		round, first, second,
		loaderPercentile(a, 0.50), loaderPercentile(a, 0.95),
		loaderPercentile(b, 0.50), loaderPercentile(b, 0.95))
}

// loaderPercentile 用最近秩（nearest-rank）取分位，避免插值掩盖尾部分布。
func loaderPercentile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}
