package badges

import (
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type fakeBackfillRuns struct {
	items []BackfillRunStatus
	err   error
	calls int
}

func (f *fakeBackfillRuns) ListLatestBackfillRuns(context.Context) ([]BackfillRunStatus, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// TestBackfillStatusRequiresBadgeView 断言读面沿用 badge.view。
//
// 路由守卫（生成的 core catalog）已经声明了 badge.view，这里是第二道：
// 守卫被改坏或有人直接调服务层时，仍然不允许看不到徽章的人看运维进度。
func TestBackfillStatusRequiresBadgeView(t *testing.T) {
	service := NewAdminService(nil)
	runs := &fakeBackfillRuns{}
	service.SetBackfillRuns(runs)

	if _, err := service.BackfillStatus(context.Background(), identity.Actor{ID: 9}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("无权限必须拒绝，得到 %v", err)
	}
	if runs.calls != 0 {
		t.Fatalf("被拒绝的请求不该触达 store，得到 %d 次调用", runs.calls)
	}
	if _, err := service.BackfillStatus(context.Background(), viewActor()); err != nil {
		t.Fatalf("badge.view 必须允许，得到 %v", err)
	}
}

// TestBackfillStatusEmptyIsEmptySlice 断言「还没跑过」是空切片而不是 nil。
//
// nil 会被 JSON 序列化成 null，前端 `.length` 或 `.map` 直接炸；而空 items 是
// 200 正常结果（不是 404），契约里写的就是 `{"items":[]}`。
func TestBackfillStatusEmptyIsEmptySlice(t *testing.T) {
	service := NewAdminService(nil)
	service.SetBackfillRuns(&fakeBackfillRuns{items: nil})

	items, err := service.BackfillStatus(context.Background(), viewActor())
	if err != nil {
		t.Fatal(err)
	}
	if items == nil {
		t.Fatal("无 run 时必须返回空切片（nil 会被序列化成 null）")
	}
	if len(items) != 0 {
		t.Fatalf("期望空，得到 %+v", items)
	}

	// 未装配读口同样是空结果，而不是「端点坏了」。
	unwired := NewAdminService(nil)
	items, err = unwired.BackfillStatus(context.Background(), viewActor())
	if err != nil || items == nil {
		t.Fatalf("未装配读口应回空切片，得到 %+v / %v", items, err)
	}
}

// TestBackfillStatusPropagatesStoreError 断言读库失败上抛（500），
// 不能吞成空 items——那会告诉运营「没人点过重算」。
func TestBackfillStatusPropagatesStoreError(t *testing.T) {
	service := NewAdminService(nil)
	sentinel := errors.New("db down")
	service.SetBackfillRuns(&fakeBackfillRuns{err: sentinel})
	if _, err := service.BackfillStatus(context.Background(), viewActor()); !errors.Is(err, sentinel) {
		t.Fatalf("store 错误必须上抛，得到 %v", err)
	}
}

// TestTruncateBackfillErrorClampsToRunes 断言按字符截断且不超过上限。
//
// 按字节截断会把中文错误切出半个字符（非法 UTF-8），Postgres 的 TEXT 能存，
// 但读端点的 JSON 会变成替换字符——排查时最想看的那句话正好糊掉。
func TestTruncateBackfillErrorClampsToRunes(t *testing.T) {
	long := strings.Repeat("错", MaxBackfillErrorLength+50)
	got := truncateBackfillError(long)
	if len([]rune(got)) != MaxBackfillErrorLength {
		t.Fatalf("截断后长度 = %d，want %d", len([]rune(got)), MaxBackfillErrorLength)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("截断产生了非法 UTF-8 替换字符")
	}
	if short := truncateBackfillError("  boom \n"); short != "boom" {
		t.Fatalf("短错误应只 trim，得到 %q", short)
	}
}
