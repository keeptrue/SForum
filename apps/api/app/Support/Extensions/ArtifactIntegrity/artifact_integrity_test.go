package artifactintegrity_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
)

const (
	testExtensionID = "sforum-shortcodes"
	testVersion     = "1.3.10"
	testDigest      = "b4f399bdbd8f82304dae57b7615293068c030bf42feed72c54dfd380525d6d74"
	testEntry       = "backend/plugin"
)

// 四坐标必须全部出现在 Error() 里：少任何一个，运维都得回日志里重新拼现场。
func TestErrorRendersFourCoordinates(t *testing.T) {
	err := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, fs.ErrNotExist)
	for _, coordinate := range []string{testExtensionID, testVersion, testDigest, testEntry} {
		if !strings.Contains(err.Error(), coordinate) {
			t.Fatalf("error %q must carry coordinate %q", err.Error(), coordinate)
		}
	}
	if !strings.Contains(err.Error(), "reason=missing") {
		t.Fatalf("error %q must classify the failure as missing", err.Error())
	}
}

func TestErrorIsUnusableSentinel(t *testing.T) {
	err := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, fs.ErrNotExist)
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("typed error must match ErrArtifactUnusable, got %v", err)
	}
	// Models/Extensions 同名再导出的必须是同一个值，否则制品族会出现两套判定。
	if !errors.Is(err, extensions.ErrArtifactUnusable) {
		t.Fatalf("reexported ErrArtifactUnusable must be the same sentinel, got %v", err)
	}
}

// cause 必须保留：路径解析拒绝的现场既要是「制品不可用」，又要让既有调用方的
// errors.Is(err, ErrInvalidManifest) 判断继续成立。
func TestErrorKeepsEveryCauseReachable(t *testing.T) {
	err := artifactintegrity.EntryPathRejected(testExtensionID, testVersion, testDigest, testEntry,
		extensions.ErrInvalidManifest)
	if !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("typed error must match ErrArtifactUnusable, got %v", err)
	}
	if !errors.Is(err, extensions.ErrInvalidManifest) {
		t.Fatalf("typed error must keep ErrInvalidManifest reachable, got %v", err)
	}
	if !strings.Contains(err.Error(), "reason=path rejected") {
		t.Fatalf("error %q must record the rejected path", err.Error())
	}
}

// 缺失与权限不足的运维动作不同，必须归类成不同措辞。
func TestEntryUnavailableClassifiesCause(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "missing", cause: fs.ErrNotExist, want: "reason=missing"},
		{name: "permission", cause: fs.ErrPermission, want: "reason=permission denied"},
		{name: "directory", cause: nil, want: "reason=not a regular file"},
		{name: "other", cause: errors.New("boom"), want: "reason=unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, testCase.cause)
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q must contain %q", err.Error(), testCase.want)
			}
		})
	}
}

// 摘要不符是另一种形态：message 必须能区分它和「缺失」。
func TestEntryDigestMismatchRecordsBothDigests(t *testing.T) {
	err := artifactintegrity.EntryDigestMismatch(testExtensionID, testVersion, testDigest, testEntry,
		"b29b4775581dc792d1135f1bfd8ecf4bc7bb02e6b03f77bd083f9ec26f1726f4",
		"d171bf2d74d7e1c088b1ea9bb46b858cabf7c3e04505282b01131143ab29533d")
	message := err.Error()
	if !strings.Contains(message, "reason=digest mismatch") ||
		!strings.Contains(message, "b29b4775") || !strings.Contains(message, "d171bf2d") {
		t.Fatalf("digest mismatch message must carry both digests, got %q", message)
	}
	if !errors.Is(err, extensions.ErrArtifactUnusable) {
		t.Fatalf("digest mismatch must stay in the artifact family, got %v", err)
	}
}

// 现场错误链是 apply → stage → 装载点三层包装；Coordinates 必须穿过整条链。
func TestCoordinatesSurviveStageAndApplyWrapping(t *testing.T) {
	leaf := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, fs.ErrNotExist)
	staged := fmt.Errorf("stage %s@%s: %w", testExtensionID, testVersion, leaf)
	applied := fmt.Errorf("apply plugin runtime revision %d attempt %d: %w", 1540, 1, staged)

	coordinates, ok := artifactintegrity.Coordinates(applied)
	if !ok {
		t.Fatalf("coordinates must survive wrapping, got %v", applied)
	}
	if coordinates.ExtensionID != testExtensionID || coordinates.Version != testVersion ||
		coordinates.Digest != testDigest || coordinates.Path != testEntry {
		t.Fatalf("coordinates=%#v", coordinates)
	}
	if !errors.Is(applied, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("wrapped chain must match ErrArtifactUnusable, got %v", applied)
	}
}

// message 进未认证的 /api/v1/ready 响应：os 错误原文（含绝对路径）不得出现。
func TestErrorOmitsCauseText(t *testing.T) {
	cause := fmt.Errorf("stat /home/operator/storage/extensions/x/backend/plugin: %w", fs.ErrNotExist)
	err := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, cause)
	if strings.Contains(err.Error(), "/home/operator") {
		t.Fatalf("error must not render the os cause text, got %q", err.Error())
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cause must stay reachable through errors.Is, got %v", err)
	}
}

func TestCoordinatesAbsentOnUnrelatedError(t *testing.T) {
	if _, ok := artifactintegrity.Coordinates(errors.New("something else")); ok {
		t.Fatal("unrelated error must not yield artifact coordinates")
	}
	if _, ok := artifactintegrity.Coordinates(nil); ok {
		t.Fatal("nil error must not yield artifact coordinates")
	}
}

// Error() 只从坐标拼 message，绝不渲染 causes 原文：cause 里是 os.Stat 的
// *fs.PathError，含宿主绝对路径，而这条错误会进入未认证的 /api/v1/ready 响应。
//
// 变异即红：把 Error() 改成 `%v` 渲染 causes，下面的「同坐标不同 cause ⇒ 同一句话」
// 与「不含绝对路径」两条断言都会失败（P3-A：原实现正确，但没有任何测试钉住它）。
func TestErrorNeverRendersCauseText(t *testing.T) {
	const absolute = "/srv/sforum/storage/extensions/sforum-shortcodes/1.3.10/b4f399bd/backend/plugin"
	cause := &fs.PathError{Op: "stat", Path: absolute, Err: fs.ErrNotExist}
	err := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry, cause)
	message := err.Error()
	if strings.Contains(message, absolute) || strings.Contains(message, "/srv/") {
		t.Fatalf("Error() leaked an absolute path: %q", message)
	}
	if strings.Contains(message, "stat ") {
		t.Fatalf("Error() rendered the cause text instead of the classified reason: %q", message)
	}
	if !strings.Contains(message, "reason=missing") {
		t.Fatalf("Error() must keep the classified reason, got %q", message)
	}

	// 同一组坐标、不同 cause（不同绝对路径）必须渲染成完全相同的一句话：
	// 这是「message 只由坐标构成」的可执行定义。
	other := artifactintegrity.EntryUnavailable(testExtensionID, testVersion, testDigest, testEntry,
		&fs.PathError{Op: "stat", Path: "/home/other-user/sforum/backend/plugin", Err: fs.ErrNotExist})
	if other.Error() != message {
		t.Fatalf("message must depend on coordinates only:\n first=%q\nsecond=%q", message, other.Error())
	}
}
