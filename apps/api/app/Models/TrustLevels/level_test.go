package trustlevels_test

import (
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// thresholdTargets 列出策略里所有「非零阈值」的 (等级, 指标, 阈值) 组合。
//
// 不能在单一一档上测全部指标：默认策略里 TL2 只要求四个指标，like_given /
// bookmark 的阈值出现在更高档位。按实际配置枚举才不会漏测。
type thresholdTarget struct {
	level     trustlevels.Level
	metric    string
	threshold int64
}

func thresholdTargets(policy trustlevels.Policy) []thresholdTarget {
	var targets []thresholdTarget
	for level := trustlevels.LevelMember; level < trustlevels.LevelCount; level++ {
		for _, metric := range trustlevels.Metrics {
			threshold, ok := policy.Levels[level].Value(metric)
			if !ok {
				panic("metric " + metric + " 不在 Requirements 里")
			}
			if threshold <= 0 {
				continue
			}
			targets = append(targets, thresholdTarget{level: level, metric: metric, threshold: threshold})
		}
	}
	return targets
}

// TestLevelBoundaries 是 C 组阈值边界的表驱动断言：
// 每个非零阈值在 threshold-1 / threshold / threshold+1 三点各验一次。
//
// 单点断言（只在 threshold 上验一次）无法区分「>= 写成 >」这类 off-by-one，
// 所以每个阈值都必须有三点。
func TestLevelBoundaries(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	targets := thresholdTargets(policy)
	if len(targets) < 6 {
		t.Fatalf("只枚举出 %d 个阈值目标，六个指标应至少各有一个非零阈值", len(targets))
	}
	for _, target := range targets {
		// TL1 是「过了新人窗口」的地板，验证它只能在新人窗口内做（掉档到 TL0）；
		// TL2+ 的掉档在窗口外验证，避免账龄闸掩盖活动闸。
		age := days(policy.NewUserDays + 1)
		if target.level == trustlevels.LevelMember {
			age = 0
		}
		for _, delta := range []int64{-1, 0, 1} {
			activity := activityMeeting(policy, target.level)
			value := target.threshold + delta
			if value < 0 {
				value = 0
			}
			setActivityMetric(&activity, target.metric, value)
			level := trustlevels.LevelOf(activity, age, policy)

			if delta < 0 {
				if level >= target.level {
					t.Fatalf("TL%d 的 %s=%d（阈值 %d 以下）仍被判为 TL%d",
						target.level, target.metric, value, target.threshold, level)
				}
				continue
			}
			if level != target.level {
				t.Fatalf("TL%d 的 %s=%d（阈值 %d 及以上）被判为 TL%d",
					target.level, target.metric, value, target.threshold, level)
			}
		}
	}
}

// TestLevelAgeGate 是 D 组的账龄闸断言：trust.new_user_days 表示
// 「最多当多久新成员」——窗口内最高 TL1，窗口外至少 TL1。
func TestLevelAgeGate(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	full := activityMeeting(policy, trustlevels.LevelLeader)

	cases := []struct {
		name     string
		activity trustlevels.Activity
		age      time.Duration
		want     trustlevels.Level
	}{
		{"窗口内零活动 = TL0", trustlevels.Activity{}, days(policy.NewUserDays) - time.Second, trustlevels.LevelNewcomer},
		{"窗口内满活动最高 TL1", full, days(policy.NewUserDays) - time.Second, trustlevels.LevelMember},
		{"窗口边界整点满活动", full, days(policy.NewUserDays), trustlevels.LevelLeader},
		{"窗口外零活动至少 TL1", trustlevels.Activity{}, days(policy.NewUserDays), trustlevels.LevelMember},
		{"NewUserDays=0 时不设闸", trustlevels.Activity{}, 0, trustlevels.LevelMember},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			effective := policy
			if testCase.name == "NewUserDays=0 时不设闸" {
				effective.NewUserDays = 0
			}
			if got := trustlevels.LevelOf(testCase.activity, testCase.age, effective); got != testCase.want {
				t.Fatalf("Level() = TL%d，期望 TL%d", got, testCase.want)
			}
		})
	}
}

// TestLevelNonMonotonicPolicyKeepsHighest 锁定「配置手滑不锁死人」的语义：
// 低档有门槛、高档无门槛时，用户应该拿到高档，而不是被卡在低档。
func TestLevelNonMonotonicPolicyKeepsHighest(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	policy.Levels[trustlevels.LevelRegular] = trustlevels.Requirements{CommentCount: 999}
	policy.Levels[trustlevels.LevelVeteran] = trustlevels.Requirements{}

	got := trustlevels.LevelOf(trustlevels.Activity{}, days(policy.NewUserDays+1), policy)
	if got != trustlevels.LevelVeteran {
		t.Fatalf("非单调策略下 Level() = TL%d，期望取被满足的最高档 TL3", got)
	}
}

// TestLevelZeroCreatedAtIsStrict 证明「读不到注册时间」不会被当成老用户。
func TestLevelZeroCreatedAtIsStrict(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	full := activityMeeting(policy, trustlevels.LevelLeader)
	got := trustlevels.LevelOfAt(full, time.Time{}, time.Now(), policy)
	if got != trustlevels.LevelMember {
		t.Fatalf("零值 createdAt 下 LevelOfAt() = TL%d，期望被账龄闸压到 TL1", got)
	}
}

// TestLevelReverseCountsDemote 是 C 组升降级断言的纯函数部分：
// 可逆计数回退（赞/收藏/访问天数下降）立即降级。
func TestLevelReverseCountsDemote(t *testing.T) {
	policy := trustlevels.DefaultPolicy()
	age := days(policy.NewUserDays + 1)
	activity := activityMeeting(policy, trustlevels.LevelRegular)
	if got := trustlevels.LevelOf(activity, age, policy); got != trustlevels.LevelRegular {
		t.Fatalf("前置条件失败：期望 TL2，得到 TL%d", got)
	}
	activity.LikeReceivedCount--
	if got := trustlevels.LevelOf(activity, age, policy); got >= trustlevels.LevelRegular {
		t.Fatalf("like_received_count 回退后仍为 TL%d，期望降级", got)
	}
}

// TestLevelMetricsAreIndependentlyRead 是 T9 要求的变异靶点：
// 六个指标必须各读各的字段，不能互相顶替。
func TestLevelMetricsAreIndependentlyRead(t *testing.T) {
	policy := trustlevels.DefaultPolicy()

	covered := map[string]bool{}
	for _, target := range thresholdTargets(policy) {
		covered[target.metric] = true
		age := days(policy.NewUserDays + 1)
		if target.level == trustlevels.LevelMember {
			age = 0
		}
		activity := activityMeeting(policy, target.level)
		setActivityMetric(&activity, target.metric, 0)
		if got := trustlevels.LevelOf(activity, age, policy); got >= target.level {
			t.Fatalf("把 TL%d 的 %s 清零后仍为 TL%d：该指标没有被独立读取",
				target.level, target.metric, got)
		}
	}
	if len(covered) != len(trustlevels.Metrics) {
		t.Fatalf("只覆盖了 %d/%d 个指标：%v", len(covered), len(trustlevels.Metrics), covered)
	}
}

// activityMeeting 构造一份刚好满足某档全部阈值的统计。
func activityMeeting(policy trustlevels.Policy, level trustlevels.Level) trustlevels.Activity {
	requirement := policy.Levels[level]
	return trustlevels.Activity{
		TopicCount:        requirement.TopicCount,
		CommentCount:      requirement.CommentCount,
		LikeReceivedCount: requirement.LikeReceivedCount,
		LikeGivenCount:    requirement.LikeGivenCount,
		BookmarkCount:     requirement.BookmarkCount,
		DaysVisited:       requirement.DaysVisited,
	}
}

func setActivityMetric(activity *trustlevels.Activity, metric string, value int64) {
	switch metric {
	case "days_visited":
		activity.DaysVisited = value
	case "topic_count":
		activity.TopicCount = value
	case "comment_count":
		activity.CommentCount = value
	case "like_received_count":
		activity.LikeReceivedCount = value
	case "like_given_count":
		activity.LikeGivenCount = value
	case "bookmark_count":
		activity.BookmarkCount = value
	default:
		panic("unknown metric " + metric)
	}
}
