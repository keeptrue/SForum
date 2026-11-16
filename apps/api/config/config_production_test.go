package config

import (
	"fmt"
	"strings"
	"testing"
)

// R7-OPS-02：生产 DATABASE_URL 校验失败时，错误文本会被 validateProductionSecrets
// 原样 panic 到 stderr，最终落进容器日志。所以 error 文本与 panic 文本都只能回显
// 脱敏后的连接串（`scheme://user:***@host[:port]/db`）：口令不许出现，query 整段
// 丢弃（`?password=…` 一样能把口令带进日志）。
//
// 这里用带 `@` 与 `-` 的口令，钉住两件事：
//  1. `@` 让 net/url 按「最后一个 @」切 userinfo，按第一个 @ 拼字符串的脱敏会漏出尾巴；
//  2. 口令子串不在用户名、主机名、库名里出现，断言才真的证明是口令被抹掉。
const (
	redactionTestUser   = "app"
	redactionTestSecret = "s3cr3t-p@ssw0rd"
)

// assertNoSecret 断言文本里不含口令；失败时打印原文，便于看到漏在哪里。
func assertNoSecret(t *testing.T, label, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(text, secret) {
			t.Fatalf("%s %q 回显了口令 %q", label, text, secret)
		}
		// 百分号编码形态同样是口令（url.Parse 会解码 userinfo，日志里可能存编码态）。
		if encoded := strings.ReplaceAll(secret, "@", "%40"); encoded != secret && strings.Contains(text, encoded) {
			t.Fatalf("%s %q 回显了编码后的口令 %q", label, text, encoded)
		}
	}
}

// TestProductionDatabaseURLErrorsDoNotLeakCredentials 覆盖四条失败分支：不可解析 URL、
// 缺 host、仓库默认值、占位口令。每条都断言 error 文本既不含口令、也保留了脱敏骨架。
func TestProductionDatabaseURLErrorsDoNotLeakCredentials(t *testing.T) {
	mustContain := func(t *testing.T, text string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Fatalf("脱敏文本 %q 缺少 %q", text, want)
			}
		}
	}

	t.Run("unparsable url", func(t *testing.T) {
		// 非法端口：url.Parse 直接报错，此时 userinfo 边界不可知，整体必须是 ***。
		raw := "postgres://" + redactionTestUser + ":" + redactionTestSecret + "@db.internal:badport/sforum"
		err := ValidateProductionDatabaseURL(raw)
		if err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
		text := err.Error()
		assertNoSecret(t, "error", text, redactionTestSecret)
		mustContain(t, text, "must be a valid URL in production", "(got \"***\")")
	})

	t.Run("missing host", func(t *testing.T) {
		// userinfo 完整、主机为空：只回显 scheme，userinfo 段整体不进文本。
		raw := "postgres://" + redactionTestUser + ":" + redactionTestSecret + "@/sforum"
		err := ValidateProductionDatabaseURL(raw)
		if err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
		text := err.Error()
		assertNoSecret(t, "error", text, redactionTestSecret)
		mustContain(t, text, "must include a host in production", "(got \"postgres://***\")")
	})

	t.Run("repository default", func(t *testing.T) {
		raw := "postgres://sforum:sforum@postgres:5432/sforum?sslmode=disable"
		err := ValidateProductionDatabaseURL(raw)
		if err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
		text := err.Error()
		mustContain(t, text,
			"must not use the repository default in production",
			"(got \"postgres://sforum:***@postgres:5432/sforum\")",
		)
		// query 必须整段消失：原文的 sslmode 与任何 password= 键都不该出现。
		if strings.Contains(text, "?") || strings.Contains(text, "sslmode") || strings.Contains(text, "password=") {
			t.Fatalf("error %q 回显了 query", text)
		}
		if strings.Contains(text, raw) {
			t.Fatalf("error %q 回显了原始连接串", text)
		}
	})

	t.Run("placeholder password", func(t *testing.T) {
		// 同时把口令放进 userinfo 与 query：占位口令分支只说「用了占位口令」，
		// 两处都不许回显。
		raw := "postgres://" + redactionTestUser + ":change-me@db.internal:5432/sforum?sslmode=require&password=change-me"
		err := ValidateProductionDatabaseURL(raw)
		if err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
		text := err.Error()
		assertNoSecret(t, "error", text, "change-me")
		mustContain(t, text,
			"must not use a placeholder password in production",
			"(got \"postgres://app:***@db.internal:5432/sforum\")",
		)
	})

	t.Run("accepted production url", func(t *testing.T) {
		raw := "postgres://" + redactionTestUser + ":" + redactionTestSecret + "@db.internal:5432/sforum?sslmode=require"
		if err := ValidateProductionDatabaseURL(raw); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", raw, err)
		}
	})
}

// TestProductionDatabaseURLPanicDoesNotLeakCredentials 走真实 Load() 的生产路径：
// panic 文本与 error 文本同源，日志里也不许出现口令。
func TestProductionDatabaseURLPanicDoesNotLeakCredentials(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want []string
	}{
		{
			// 不可解析：整体脱敏，连 scheme 之外的信息都没有。
			name: "unparsable url",
			url:  "postgres://app:" + redactionTestSecret + "@db.internal:badport/sforum",
			want: []string{"config: DATABASE_URL", "must be a valid URL in production", "(got \"***\")"},
		},
		{
			name: "placeholder password",
			url:  "postgres://app:change-me@db.internal:5432/sforum?sslmode=require&password=change-me",
			want: []string{"config: DATABASE_URL", "must not use a placeholder password in production", "postgres://app:***@db.internal:5432/sforum"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			setValidProductionSecrets(t)
			t.Setenv("APP_ENV", "production")
			t.Setenv("DATABASE_URL", testCase.url)

			recovered := captureProductionPanic(t)
			assertNoSecret(t, "panic", recovered, redactionTestSecret, "change-me")
			for _, want := range testCase.want {
				if !strings.Contains(recovered, want) {
					t.Fatalf("panic %q 缺少 %q", recovered, want)
				}
			}
		})
	}
}

// TestRedactDatabaseURLShapes 单独钉住脱敏骨架本身：调用点改了也不能让形态漂掉。
func TestRedactDatabaseURLShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"user with password", "postgres://app:s3cret@db.internal:5432/sforum?sslmode=require", "postgres://app:***@db.internal:5432/sforum"},
		{"postgresql scheme keeps its name", "postgresql://app:s3cret@db.internal/sforum", "postgresql://app:***@db.internal/sforum"},
		{"passwordless peer auth", "postgres://app@db.internal:5432/sforum", "postgres://app@db.internal:5432/sforum"},
		{"password containing at signs", "postgres://app:s3cr3t-p@ssw0rd@db.internal:5432/sforum", "postgres://app:***@db.internal:5432/sforum"},
		{"empty username keeps a marker", "postgres://:s3cret@db.internal/x", "postgres://***:***@db.internal/x"},
		{"query carrying a password is dropped", "postgres://app:s3cret@db.internal/sforum?password=s3cret&sslmode=require#frag", "postgres://app:***@db.internal/sforum"},
		{"no host falls back to scheme", "postgres:user:s3cret@host/db", "postgres://***"},
		{"unparsable url", "postgres://app:s3cret@db.internal:badport/sforum", "***"},
		{"no scheme", "s3cret-without-scheme", "***"},
		{"empty", "   ", "***"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := redactDatabaseURL(testCase.raw)
			if got != testCase.want {
				t.Fatalf("redactDatabaseURL(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
			assertNoSecret(t, "redacted", got, "s3cret")
		})
	}
}

// captureProductionPanic 跑一次 Load() 并返回 panic 文本；没有 panic 直接失败。
func captureProductionPanic(t *testing.T) (recoveredText string) {
	t.Helper()
	defer func() {
		value := recover()
		if value == nil {
			t.Fatal("expected Load to panic on a rejected DATABASE_URL, got no panic")
		}
		recoveredText = fmt.Sprint(value)
	}()
	Load()
	return ""
}
