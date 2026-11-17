// Package artifactintegrity 是「已发布插件制品在盘上无法装载」这一族失败的唯一
// 构造入口与坐标载体。
//
// 它回答一个问题：哪一份制品的哪个文件不可用、是缺失还是不符。它不做「制品是否
// 可用」的完整判定——目录级判据仍是 Models/Extensions 的 artifactState，入口级
// 判据是装载点的 InstalledFilePathForRuntime + os.Stat；本包只负责把结论变成
// errors.Is / errors.As 稳定可识别、且带齐坐标的值。
//
// 包内不 import 任何项目内包：装载点在 Support/Extensions，构造点也在
// Models/Extensions，两侧共用同一套坐标，任何单向依赖都会成环。
package artifactintegrity

import (
	"errors"
	"fmt"
	"io/fs"
)

// ErrArtifactUnusable 表示一份冻结制品当前无法被装载。
//
// 它是制品族的唯一哨兵：Models/Extensions 以同名变量再导出，启动期装载、发布期
// 校验、恢复码映射都判断这一个值，不新增第二套「制品是否可用」的词汇。
var ErrArtifactUnusable = errors.New("extensions: artifact is not loadable")

// Error 携带定位一份坏制品所需的坐标。
//
// Path 永远是相对包根的坐标（例如 backend/plugin），绝不是宿主绝对路径：该错误
// 会进入未认证的 /api/v1/ready 响应，绝对路径属于私有诊断信息。同理 Reason 是
// 归类后的固定措辞，绝不渲染 os 错误的原文（原文含绝对路径）。
type Error struct {
	ExtensionID string
	Version     string
	Digest      string
	Path        string
	Reason      string

	causes []error
}

// New 构造带坐标的制品完整性错误。
//
// causes 参与 errors.Is 链路：制品族失败必须同时匹配 ErrArtifactUnusable；路径
// 解析拒绝还要保留 ErrInvalidManifest，否则既有调用方的判断会失效。
func New(extensionID, version, digest, path, reason string, causes ...error) error {
	return &Error{
		ExtensionID: extensionID,
		Version:     version,
		Digest:      digest,
		Path:        path,
		Reason:      reason,
		causes:      causes,
	}
}

// EntryUnavailable 报告 manifest 声明的 backend 入口在制品里不可用。
//
// cause 是 os.Stat 的原始错误：缺失与权限不足的运维动作不同（补制品 vs 修权限），
// 因此归类成不同的 Reason，但原始错误只进 errors.Is 链路、不进 message。
func EntryUnavailable(extensionID, version, digest, entry string, cause error) error {
	return New(extensionID, version, digest, entry, entryUnavailableReason(cause), cause, ErrArtifactUnusable)
}

// EntryDigestMismatch 报告入口文件在位但摘要与 manifest 声明不符。
//
// 这是制品损坏里最隐蔽的一种：文件名对、文件也在，只 stat 的判据会放行。exec
// 路径（OpenVerifiedRuntimeExecutable）与发布期校验（Verify）都归到这一句，
// 保证同一个故障在两条链路上有同一种说法、同一组 want/got。
//
// causes 允许调用方追加既有的语义哨兵（例如 ErrFrontendPackageChanged），
// 让本函数的返回值同时是「制品损坏」和调用方原有的分类。
func EntryDigestMismatch(extensionID, version, digest, entry, want, got string, causes ...error) error {
	return New(extensionID, version, digest, entry,
		fmt.Sprintf("digest mismatch: want %s got %s", want, got),
		append([]error{ErrArtifactUnusable}, causes...)...)
}

// EntryPathRejected 报告入口路径被解析拒绝（越界或非法相对路径）。
func EntryPathRejected(extensionID, version, digest, entry string, causes ...error) error {
	return New(extensionID, version, digest, entry, "path rejected",
		append([]error{ErrArtifactUnusable}, causes...)...)
}

// entryUnavailableReason 把 os.Stat 的失败归类成固定措辞。cause 为 nil 只可能
// 来自「路径存在但是目录」那一支。
func entryUnavailableReason(cause error) string {
	switch {
	case cause == nil:
		return "not a regular file"
	case errors.Is(cause, fs.ErrNotExist):
		return "missing"
	case errors.Is(cause, fs.ErrPermission):
		return "permission denied"
	default:
		return "unavailable"
	}
}

func (e *Error) Error() string {
	if e == nil {
		return "plugin artifact is incomplete"
	}
	return fmt.Sprintf("plugin artifact is incomplete: extensionId=%s version=%s digest=%s path=%s reason=%s",
		e.ExtensionID, e.Version, e.Digest, e.Path, e.Reason)
}

// Unwrap 暴露全部 cause，使 errors.Is 对每一个都成立。
func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	return e.causes
}

// Coordinates 从错误链里取出第一个坐标载体，供恢复码映射渲染 message。
func Coordinates(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}
