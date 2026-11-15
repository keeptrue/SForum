package humanverify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 限流窗口的「自增」与「设置过期」必须是同一次原子操作（RV-52）。
//
// 旧实现是 INCR 再 EXPIRE 两条命令：两步之间进程崩溃/连接中断就留下一个没有
// TTL 的计数键，该 subject 会被永久限流（key 永不过期，计数只增不减）。
func TestIncrementRateUsesSingleAtomicExpiringCommand(t *testing.T) {
	client := &recordingRateClient{count: 3}
	store := &RedisStore{client: client, prefix: "humanverify:"}

	limited, err := store.IncrementRate(context.Background(), "login:42", time.Minute, 5)
	if err != nil {
		t.Fatalf("increment rate: %v", err)
	}
	if limited {
		t.Fatalf("count 3 against limit 5 was reported as limited")
	}
	if len(client.commands) != 1 || client.commands[0] != "eval" {
		t.Fatalf("rate limit issued %v; INCR and its expiry must be one atomic script", client.commands)
	}
	if !strings.Contains(client.script, "INCR") || !strings.Contains(client.script, "PEXPIRE") {
		t.Fatalf("rate limit script does not both count and expire in one shot: %q", client.script)
	}
	if len(client.args) != 1 || client.args[0] != int64(60000) {
		t.Fatalf("rate limit TTL arg = %v, want one window in milliseconds (60000)", client.args)
	}
	if client.keys[0] != "humanverify:rate:login:42" {
		t.Fatalf("rate limit key = %q", client.keys[0])
	}
}

// 超过上限时仍返回 limited=true；判定必须基于自增后的计数。
func TestIncrementRateFlagsOverLimit(t *testing.T) {
	client := &recordingRateClient{count: 6}
	store := &RedisStore{client: client, prefix: "humanverify:"}
	limited, err := store.IncrementRate(context.Background(), "login:42", time.Minute, 5)
	if err != nil {
		t.Fatalf("increment rate: %v", err)
	}
	if !limited {
		t.Fatalf("count 6 against limit 5 was not reported as limited")
	}
}

// recordingRateClient 记录命令序列，用来区分「一次 EVAL」与「INCR + EXPIRE」。
type recordingRateClient struct {
	redis.Scripter
	commands []string
	script   string
	keys     []string
	args     []any
	count    int64
}

func (c *recordingRateClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	c.commands = append(c.commands, "eval")
	c.script = script
	c.keys = keys
	c.args = args
	cmd := redis.NewCmd(ctx)
	cmd.SetVal(c.count)
	return cmd
}

// EvalSha 返回 NOSCRIPT，让 redis.Script.Run 回落到 EVAL（真实 Redis 首次执行也是这条路）。
func (c *recordingRateClient) EvalSha(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	cmd.SetErr(redis.ErrNoScript)
	return cmd
}

func (c *recordingRateClient) Close() error { return nil }

func (c *recordingRateClient) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd {
	c.commands = append(c.commands, "setnx")
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}

func (c *recordingRateClient) Incr(ctx context.Context, key string) *redis.IntCmd {
	c.commands = append(c.commands, "incr")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(c.count)
	return cmd
}

func (c *recordingRateClient) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	c.commands = append(c.commands, "expire")
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}
