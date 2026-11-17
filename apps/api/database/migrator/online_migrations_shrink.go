package migrator

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 收缩型 DDL 的静态棘轮。
//
// 背景：`-- +sforum OnlineSafe` 是作者对「新旧二进制都能工作」的**书面承诺**，
// 而自动侧原来只校验声明形态（事务性 + 两条 SET LOCAL），不校验 Up 段到底做了什么 ——
// 一条 `DROP COLUMN` 照样能通过（见 knowledge/decisions/2026-11-15-online-safe-migration-human-contract.md）。
//
// 这里补的是该决定记录里点名的窄形态检查：**列收缩 / 类型收窄必须显式登记才允许**。
// 它不试图证明兼容性（静态 SQL 分析判不准，假阴性会挡住合法发布、假阳性给虚假安全感），
// 只做一件能判准的事：声明了 OnlineSafe 却在 Up 段收缩 schema，就必须有人在
// registeredShrinkDDL 里写下这条语句并说明为什么旧二进制仍然安全。
//
// 刻意只认三种形态（其余扩张型写法一律放行，不做泛化）：
//   - DROP COLUMN         列消失，旧代码的 SELECT/INSERT 立刻报错
//   - RENAME COLUMN       旧代码按旧列名读不到
//   - ALTER COLUMN TYPE   类型收窄（如 text→varchar(n)、int8→int4）会截断/溢出
//
// 未声明 OnlineSafe 的迁移**不参与**这条检查：它们本来就走 deploy.sh 的停机窗口，
// 收缩是合法的。棘轮只约束「声明了在线安全」的那些。

// ShrinkDDLStatement 是 Up 段里被识别出的一条收缩语句。
type ShrinkDDLStatement struct {
	// Kind 是形态标签：drop column / rename column / alter column type。
	Kind string
	// Statement 是空白归一化后的语句文本，同时是登记表的匹配键。
	Statement string
}

// ShrinkDDLRegistration 是「这条收缩语句已人工复核」的登记项。
//
// Migration 与 Statement 必须同时对上（同一条语句出现在另一条迁移里不算已登记）。
// Reason 必须写清楚为什么旧二进制仍然安全 —— 与 parity 门基线同一个纪律：
// 没有理由的登记等于关掉这条棘轮。
type ShrinkDDLRegistration struct {
	Migration string // 迁移文件名，例如 202611160002_user_trust_levels_revision.sql
	Statement string // ShrinkDDLStatement.Statement 的原值
	Reason    string // 中文：为什么这条收缩对回滚窗口内的旧二进制仍然安全
}

// registeredShrinkDDL 是收缩型 DDL 的**人工**登记表。
//
// 当前为空，这是有意义的结论：嵌入式迁移集里所有声明了 OnlineSafe 的迁移，Up 段
// 都只做扩张。TestEmbeddedOnlineSafeFormsAreWellFormed 会持续钉住这一点 ——
// 新增一条收缩语句而没在这里登记，形态判定立刻变 malformed，CI 的
// `migrate online-safe-form` 以退出码 3 失败。
//
// 新增登记的做法：先确认「先扩后缩」的收缩步骤确实不在本次回滚窗口里（见决定记录
// 第 5 条），再把语句原文（空白归一化后）与理由一起写进来。不要用通配或前缀匹配。
var registeredShrinkDDL = []ShrinkDDLRegistration{}

// shrinkDDLMatchers 是形态识别表。顺序即输出顺序，便于稳定报错。
var shrinkDDLMatchers = []struct {
	kind   string
	regexp *regexp.Regexp
}{
	{"drop column", regexp.MustCompile(`\bDROP\s+COLUMN\b`)},
	{"rename column", regexp.MustCompile(`\bRENAME\s+COLUMN\b`)},
	{"alter column type", regexp.MustCompile(`\bALTER\s+COLUMN\s+\S+\s+(?:SET\s+DATA\s+)?TYPE\b`)},
}

// detectShrinkDDL 在 Up 段里找出全部收缩语句（按语句切分，命中形态即收）。
//
// 匹配只发生在「代码文本」上：注释、单引号字符串、美元引用块（函数体）都被替换成
// 空格，避免 `-- 不要 DROP COLUMN` 这类说明文字被当成真语句。
func detectShrinkDDL(upBody string) []ShrinkDDLStatement {
	code := stripSQLNonCode(upBody)
	found := make([]ShrinkDDLStatement, 0)
	for _, statement := range splitSQLStatements(code) {
		normalized := normalizeSQLWhitespace(statement)
		if normalized == "" {
			continue
		}
		for _, matcher := range shrinkDDLMatchers {
			if !matcher.regexp.MatchString(normalized) {
				continue
			}
			found = append(found, ShrinkDDLStatement{Kind: matcher.kind, Statement: normalized})
			break
		}
	}
	return found
}

// unregisteredShrinkDDL 返回未被登记的收缩语句（按语句文本排序，输出稳定）。
func unregisteredShrinkDDL(migration string, upBody string, registry []ShrinkDDLRegistration) []ShrinkDDLStatement {
	registered := make(map[string]struct{}, len(registry))
	for _, entry := range registry {
		if entry.Migration == migration {
			registered[entry.Statement] = struct{}{}
		}
	}
	pending := make([]ShrinkDDLStatement, 0)
	for _, statement := range detectShrinkDDL(upBody) {
		if _, ok := registered[statement.Statement]; ok {
			continue
		}
		pending = append(pending, statement)
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].Statement < pending[right].Statement })
	return pending
}

// shrinkDDLReason 拼出可操作的失败原因：逐条列出未登记的语句。
func shrinkDDLReason(pending []ShrinkDDLStatement) string {
	parts := make([]string, 0, len(pending))
	for _, statement := range pending {
		parts = append(parts, fmt.Sprintf("%s: %s", statement.Kind, statement.Statement))
	}
	return "shrink DDL requires explicit registration in registeredShrinkDDL (schema 收缩不在 OnlineSafe 承诺内): " +
		strings.Join(parts, " | ")
}

// stripSQLNonCode 把注释、单引号字符串、美元引用块替换成空格，保留换行与其余代码。
//
// 只服务「找形态」这件事，不追求完整 SQL 词法：够挡住说明文字与函数体里的假命中即可。
func stripSQLNonCode(body string) string {
	out := []byte(body)
	length := len(out)
	blank := func(from, to int) {
		for i := from; i < to && i < length; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < length; i++ {
		switch {
		case out[i] == '/' && i+1 < length && out[i+1] == '*':
			end := strings.Index(body[i+2:], "*/")
			if end < 0 {
				blank(i, length)
				i = length
				continue
			}
			blank(i, i+2+end+2)
			i += 2 + end + 1
		case out[i] == '-' && i+1 < length && out[i+1] == '-':
			end := strings.IndexByte(body[i:], '\n')
			if end < 0 {
				end = length - i
			}
			blank(i, i+end)
			i += end - 1
		case out[i] == '\'':
			j := i + 1
			for j < length {
				if body[j] == '\'' {
					if j+1 < length && body[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			blank(i, j+1)
			i = j
		case out[i] == '$':
			tag := dollarTag(body[i:])
			if tag == "" {
				continue
			}
			closeAt := strings.Index(body[i+len(tag):], tag)
			if closeAt < 0 {
				blank(i, length)
				i = length
				continue
			}
			end := i + len(tag) + closeAt + len(tag)
			blank(i, end)
			i = end - 1
		}
	}
	return string(out)
}

// dollarTag 识别 `$$` 或 `$name$` 形式的美元引用起始标记，非标记返回空串。
func dollarTag(rest string) string {
	if len(rest) < 2 || rest[0] != '$' {
		return ""
	}
	if rest[1] == '$' {
		return "$$"
	}
	end := strings.IndexByte(rest[1:], '$')
	if end < 0 {
		return ""
	}
	tag := rest[:end+2]
	for _, r := range tag[1 : len(tag)-1] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return tag
}

// splitSQLStatements 按分号切语句（此时字符串与美元引用块已被清空，分号只可能是真分隔符）。
func splitSQLStatements(code string) []string {
	parts := strings.Split(code, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statements = append(statements, part)
	}
	return statements
}

// normalizeSQLWhitespace 把连续空白折成单个空格，去首尾，用于稳定匹配与报错。
func normalizeSQLWhitespace(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}
