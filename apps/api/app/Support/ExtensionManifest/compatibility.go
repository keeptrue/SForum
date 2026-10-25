package extensionmanifest

import (
	"errors"
	"fmt"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

// 清单兼容求值的两个失败面必须分开：
//   - Incompatible：约束合法，但当前 core 不在范围内 —— 这是可判定的拒绝。
//   - Unverifiable：core 版本标签本身不是严格 semver —— 这是构建缺陷，
//     调用方应跳过兜底并告警，而不是把一次打包失误变成「所有安装都失败」。
var (
	ErrSForumVersionIncompatible = errors.New("extensionmanifest: manifest sforumVersion excludes the running core")
	ErrSForumVersionUnverifiable = errors.New("extensionmanifest: core version is not strict semver")
)

// SForumVersionIncompatibleError 携带声明范围与实际 core 版本，供 HTTP 层
// 回传结构化字段，避免管理员只能看到一个笼统的「不兼容」。
type SForumVersionIncompatibleError struct {
	Required    string
	CoreVersion string
}

func (e *SForumVersionIncompatibleError) Error() string {
	return fmt.Sprintf("%v: required=%q core=%q", ErrSForumVersionIncompatible, e.Required, e.CoreVersion)
}

func (e *SForumVersionIncompatibleError) Unwrap() error { return ErrSForumVersionIncompatible }

// CheckSForumVersion 用严格 semver 的 coreVersion 求值清单声明的约束。
//
// 只接受一个版本输入：BuildVersion（dev-abc12 之类的展示标签）永远不参与
// 这里，否则同一份包会因为构建标签不同而得到不同结论。
func CheckSForumVersion(manifest Manifest, coreVersion string) error {
	constraintText := strings.TrimSpace(manifest.SForumVersion)
	core, err := semver.StrictNewVersion(strings.TrimSpace(coreVersion))
	if err != nil {
		return fmt.Errorf("%w: core=%q", ErrSForumVersionUnverifiable, coreVersion)
	}
	constraint, err := semver.NewConstraint(constraintText)
	if err != nil {
		// 清单校验已经拒绝过非法约束；走到这里说明调用方绕过了 validate。
		return fmt.Errorf("%w: sforumVersion=%q", ErrSForumVersionUnverifiable, constraintText)
	}
	if constraint.Check(core) {
		return nil
	}
	// Masterminds 的约束默认不匹配预发布版本：core 是 3.0.13-alpha.1 时，
	// 连 >=0.1.0 都会被判 false。发行版里 alpha/beta 是常态，若照字面拒绝，
	// 每次预发布都会让所有第三方包无法安装。规则：预发布 core 允许用
	// 「同一发行基线」（3.0.13）再求值一次；不会放宽到别的版本线。
	if base := releaseBase(core); base != nil && constraint.Check(base) {
		return nil
	}
	return &SForumVersionIncompatibleError{Required: constraintText, CoreVersion: core.String()}
}

// releaseBase 返回去掉预发布与构建元数据后的发行基线；没有预发布时返回 nil，
// 调用方因此只会对预发布 core 多求值一次。
func releaseBase(version *semver.Version) *semver.Version {
	if version.Prerelease() == "" {
		return nil
	}
	base, err := version.SetPrerelease("")
	if err != nil {
		return nil
	}
	return &base
}
