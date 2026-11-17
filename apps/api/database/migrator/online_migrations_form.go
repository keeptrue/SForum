package migrator

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// OnlineSafeForm 是单条迁移的「在线安全声明形态」。
//
// 三态是刻意的：**未声明是合法的**（离线迁移本来就走停机窗口），只有「声明了却
// 形态不合格」才是错误。旧实现把两件事揉进一个 (bool, error)，于是只能回答
// 「这条能不能在线迁」，回答不了「整个迁移集的形态如何」；而它又是 CheckOnlineSafe
// 的内部函数，必须先连上数据库才走得到——离线环境里完全无法判断形态。
type OnlineSafeForm string

const (
	// OnlineSafeFormDeclared：声明了 directive，且事务性 + 两条 SET LOCAL 齐全。
	OnlineSafeFormDeclared OnlineSafeForm = "declared"
	// OnlineSafeFormUndeclared：没有声明。合法，只报告。
	OnlineSafeFormUndeclared OnlineSafeForm = "undeclared"
	// OnlineSafeFormMalformed：声明了但形态不合格（NO TRANSACTION / 缺 SET LOCAL）。
	OnlineSafeFormMalformed OnlineSafeForm = "malformed"
)

// OnlineSafeDeclaration 是单条迁移的形态结论。
type OnlineSafeDeclaration struct {
	Path   string
	Form   OnlineSafeForm
	Reason string // 仅 malformed 有值
}

// OnlineSafeFormReport 是全部迁移的形态汇总。
type OnlineSafeFormReport struct {
	Declarations []OnlineSafeDeclaration
}

// Counts 返回 declared / undeclared / malformed 三个计数。
func (report OnlineSafeFormReport) Counts() (declared, undeclared, malformed int) {
	for _, declaration := range report.Declarations {
		switch declaration.Form {
		case OnlineSafeFormDeclared:
			declared++
		case OnlineSafeFormUndeclared:
			undeclared++
		case OnlineSafeFormMalformed:
			malformed++
		}
	}
	return declared, undeclared, malformed
}

// Malformed 只返回不合格的声明（按路径排序，输出稳定）。
func (report OnlineSafeFormReport) Malformed() []OnlineSafeDeclaration {
	malformed := make([]OnlineSafeDeclaration, 0, len(report.Declarations))
	for _, declaration := range report.Declarations {
		if declaration.Form == OnlineSafeFormMalformed {
			malformed = append(malformed, declaration)
		}
	}
	sort.Slice(malformed, func(left, right int) bool { return malformed[left].Path < malformed[right].Path })
	return malformed
}

// CheckOnlineSafeForm 遍历 fsys 里的全部 SQL 迁移并分类。
//
// 这是**离线**判定：只读嵌入式迁移文件，不打开数据库、不读配置。它回答的是
// 「声明形态对不对」，不回答「当前库有哪些待迁」——后者才需要数据库。
func CheckOnlineSafeForm(fsys fs.FS) (OnlineSafeFormReport, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return OnlineSafeFormReport{}, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	report := OnlineSafeFormReport{Declarations: make([]OnlineSafeDeclaration, 0, len(names))}
	for _, name := range names {
		declaration, err := classifyOnlineSafeForm(fsys, name)
		if err != nil {
			return OnlineSafeFormReport{}, fmt.Errorf("classify %s: %w", name, err)
		}
		report.Declarations = append(report.Declarations, declaration)
	}
	return report, nil
}

// classifyOnlineSafeForm 是纯函数：只读 fsys 里的那一条迁移，不碰数据库。
//
// 判定规则（与线上门完全一致，改一处必须改两处）：
//   - 只看 Up 段：`-- +goose Down` 之后出现 directive 不算声明；
//   - 声明了就必须事务执行（不允许 `-- +goose NO TRANSACTION`）；
//   - 声明了就必须自带锁/语句超时（`SET LOCAL lock_timeout` / `statement_timeout`）；
//   - 声明了就不许在 Up 段做收缩型 DDL，除非逐条登记在 registeredShrinkDDL
//     （见 online_migrations_shrink.go 与 2026-11-15 的决定记录）。
func classifyOnlineSafeForm(fsys fs.FS, path string) (OnlineSafeDeclaration, error) {
	return classifyOnlineSafeFormWithRegistry(fsys, path, registeredShrinkDDL)
}

// classifyOnlineSafeFormWithRegistry 与 classifyOnlineSafeForm 同源，只是把收缩型 DDL 的
// 登记表作为参数传入 —— 反例测试要在不改进程级 registeredShrinkDDL 的前提下验证
// 「登记了就放行 / 没登记就红」两个方向。
func classifyOnlineSafeFormWithRegistry(
	fsys fs.FS,
	path string,
	registry []ShrinkDDLRegistration,
) (OnlineSafeDeclaration, error) {
	body, err := fs.ReadFile(fsys, path)
	if err != nil {
		return OnlineSafeDeclaration{}, err
	}
	upLines := make([]string, 0)
	declared := false
	for _, line := range strings.Split(string(body), "\n") {
		directive := strings.TrimSpace(line)
		if directive == "-- +goose Down" {
			break
		}
		upLines = append(upLines, line)
		if directive == onlineSafeDirective {
			declared = true
		}
	}
	if !declared {
		return OnlineSafeDeclaration{Path: path, Form: OnlineSafeFormUndeclared}, nil
	}
	upBody := strings.Join(upLines, "\n")
	if strings.Contains(upBody, "-- +goose NO TRANSACTION") {
		return OnlineSafeDeclaration{
			Path: path, Form: OnlineSafeFormMalformed, Reason: "online migrations must run transactionally",
		}, nil
	}
	for _, required := range []string{"SET LOCAL lock_timeout", "SET LOCAL statement_timeout"} {
		if !strings.Contains(upBody, required) {
			return OnlineSafeDeclaration{
				Path: path, Form: OnlineSafeFormMalformed, Reason: "missing " + required,
			}, nil
		}
	}
	// 收缩型 DDL 棘轮：声明了在线安全却在 Up 段删列/改名/收窄类型，必须显式登记。
	// 未声明 OnlineSafe 的迁移走不到这里 —— 它们本来就走停机窗口，收缩是合法的。
	if pending := unregisteredShrinkDDL(path, upBody, registry); len(pending) > 0 {
		return OnlineSafeDeclaration{
			Path: path, Form: OnlineSafeFormMalformed, Reason: shrinkDDLReason(pending),
		}, nil
	}
	return OnlineSafeDeclaration{Path: path, Form: OnlineSafeFormDeclared}, nil
}
