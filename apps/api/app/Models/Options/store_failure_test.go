package options

import (
	"context"
	"errors"
	"testing"
	"time"
)

// failingOptionStore 只在写路径上失败，读路径返回空表（loadMap 会补齐推荐默认值）。
type failingOptionStore struct {
	upsertErr error
}

func (s *failingOptionStore) List(context.Context) ([]Option, error) { return nil, nil }

func (s *failingOptionStore) InsertMissing(context.Context, UpdateInput) error { return nil }

func (s *failingOptionStore) Upsert(context.Context, UpdateInput) (Option, error) {
	return Option{}, s.upsertErr
}

// TestStoreWriteFailureIsClassifiedAsServerError 锁定 M12-a 的第二条：
// 写库失败不是用户输入错误，必须能被 HTTP 层识别成 500 而不是 422。
func TestStoreWriteFailureIsClassifiedAsServerError(t *testing.T) {
	cause := errors.New("connection refused")
	service := NewServiceWithCacheTTL(&failingOptionStore{upsertErr: cause}, time.Minute)

	_, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameSiteName, Value: "新站名"})
	if !errors.Is(err, ErrStoreFailure) {
		t.Fatalf("写库失败没被标成 ErrStoreFailure：%v", err)
	}
	if errors.Is(err, ErrInvalidOption) {
		t.Fatal("写库失败被报成输入错误（HTTP 层会回 422，运营会去改参数）")
	}
	// 原始 err 必须还在链上：日志要能看到底层原因。
	if !errors.Is(err, cause) {
		t.Fatalf("原始 err 没被包住：%v", err)
	}
}

// TestStoreInputRejectionIsNotServerError 是对照组：store 自己判定的输入类错误
// （例如站点 logo 的附件 id 非法）必须保持 422 语义，不能被升级成 500。
func TestStoreInputRejectionIsNotServerError(t *testing.T) {
	service := NewServiceWithCacheTTL(&failingOptionStore{upsertErr: ErrInvalidOption}, time.Minute)

	_, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameSiteName, Value: "新站名"})
	if errors.Is(err, ErrStoreFailure) {
		t.Fatalf("store 的输入类拒绝被升级成服务端错误：%v", err)
	}
	if !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("store 的输入类拒绝丢了 ErrInvalidOption 语义：%v", err)
	}
}

// TestValidationErrorsAreDistinguishable 锁定 M12-a 的第一条：
// 「值本身不合法」与「值不在允许集合内」必须是两个可区分的错误。
func TestValidationErrorsAreDistinguishable(t *testing.T) {
	service := NewServiceWithCacheTTL(&fakeStore{}, time.Minute)

	_, err := service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameSiteName, Value: ""})
	if !errors.Is(err, ErrInvalidOptionValue) {
		t.Fatalf("站点名为空应是归一化失败（ErrInvalidOptionValue），实际：%v", err)
	}
	if errors.Is(err, ErrInvalidOptionSet) {
		t.Fatal("归一化失败被同时标成值集拒绝（两个 reason 会互相覆盖）")
	}

	// 单值合法但与另一项冲突：默认语言 zh-CN 不在新的受支持语言列表里。
	_, err = service.Update(context.Background(), settingsActor(), UpdateInput{Name: NameSiteSupportedLocales, Value: "en-US"})
	if !errors.Is(err, ErrInvalidOptionSet) {
		t.Fatalf("默认语言不在受支持列表内应是值集拒绝（ErrInvalidOptionSet），实际：%v", err)
	}
	if errors.Is(err, ErrInvalidOptionValue) {
		t.Fatal("值集拒绝被同时标成归一化失败")
	}
}
