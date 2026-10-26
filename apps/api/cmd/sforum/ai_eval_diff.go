package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// writeEvalSnapshot 把一条快照写进基线目录。
//
// 两个稳定化处理是「基线可跨机器比较」的前提：JSON 带缩进便于人工 triage，末尾
// 补一个换行让 diff 工具不报 "no newline at end of file"。
func writeEvalSnapshot(dir string, snapshot evalSnapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create baseline dir %s: %w", dir, err)
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode snapshot %s: %w", snapshot.ID, err)
	}
	path := filepath.Join(dir, snapshot.ID+".json")
	if err := os.WriteFile(path, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("write snapshot %s: %w", path, err)
	}
	return nil
}

// readEvalSnapshot 读一条已提交的基线。
func readEvalSnapshot(path string) (evalSnapshot, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return evalSnapshot{}, err
	}
	var snapshot evalSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return evalSnapshot{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return snapshot, nil
}

// evalFieldChange 是一个字段的变化。它只描述「变了什么」，不判断「变好还是变坏」：
// eval 的职责是让退化可见，不是替人做 triage。
type evalFieldChange struct {
	Field  string
	Before string
	After  string
}

// evalReport 是单条 fixture 的比对结论。
type evalReport struct {
	ID      string
	Changed []evalFieldChange
	// MissingBaseline 表示基线里没有这条 fixture（新增用例），不是退化。
	MissingBaseline bool
	// MissingCurrent 表示基线里有但这次没跑（用例被删除或改名）。
	MissingCurrent bool
	// PromptShifted 表示 prompt 指纹变了：这一次的差异是「改动生效」，可信。
	PromptShifted bool
	// SampledOnly 表示 prompt 指纹未变但产出文本变了：这是 provider 采样噪声。
	SampledOnly bool
}

func (r evalReport) changed() bool { return len(r.Changed) > 0 }

// diffEvalSnapshot 逐字段比较基线与当前快照。
//
// 刻意不做「整体 JSON 字符串比较」：那会把无关的字段顺序或缩进差异报成变化，
// 而真正想看见的是「回答变短了 14 字」「少了一次 forum-search」。字段级比较也让
// 输出能直接告诉运营者该看哪一项。
func diffEvalSnapshot(baseline, current evalSnapshot) []evalFieldChange {
	changes := make([]evalFieldChange, 0, 4)
	if baseline.Text != current.Text {
		changes = append(changes, evalFieldChange{
			Field:  "text",
			Before: fmt.Sprintf("%d chars", len([]rune(baseline.Text))),
			After:  fmt.Sprintf("%d chars", len([]rune(current.Text))),
		})
	}
	if baseline.StopReason != current.StopReason {
		changes = append(changes, evalFieldChange{
			Field:  "stopReason",
			Before: orNone(baseline.StopReason),
			After:  orNone(current.StopReason),
		})
	}
	if tools := diffToolTrace(baseline.ToolTrace, current.ToolTrace); tools != "" {
		changes = append(changes, evalFieldChange{
			Field:  "toolTrace",
			Before: fmt.Sprintf("%d calls", len(baseline.ToolTrace)),
			After:  fmt.Sprintf("%d calls (%s)", len(current.ToolTrace), tools),
		})
	}
	if baseline.ModelCalls != current.ModelCalls {
		changes = append(changes, evalFieldChange{
			Field:  "modelCalls",
			Before: fmt.Sprint(baseline.ModelCalls),
			After:  fmt.Sprint(current.ModelCalls),
		})
	}
	if baseline.Usage.InputTokens != current.Usage.InputTokens {
		changes = append(changes, evalFieldChange{
			Field:  "usage.inputTokens",
			Before: fmt.Sprint(baseline.Usage.InputTokens),
			After:  fmt.Sprint(current.Usage.InputTokens),
		})
	}
	if baseline.Usage.OutputTokens != current.Usage.OutputTokens {
		changes = append(changes, evalFieldChange{
			Field:  "usage.outputTokens",
			Before: fmt.Sprint(baseline.Usage.OutputTokens),
			After:  fmt.Sprint(current.Usage.OutputTokens),
		})
	}
	if baseline.Error != current.Error {
		changes = append(changes, evalFieldChange{
			Field:  "error",
			Before: orNone(baseline.Error),
			After:  orNone(current.Error),
		})
	}
	return changes
}

// diffToolTrace 用「调用名序列」概括轨迹变化。
//
// 名字序列而不是条目全字段：延迟与字节数天然抖动，把它们报成变化会让 diff 永远
// 不安静。真正要看见的是「哪个工具的哪一步消失了」。
func diffToolTrace(baseline, current []supportai.ToolTraceEntry) string {
	before := toolNames(baseline)
	after := toolNames(current)
	if strings.Join(before, ",") == strings.Join(after, ",") {
		return ""
	}
	remaining := map[string]int{}
	for _, name := range after {
		remaining[name]++
	}
	dropped := make([]string, 0, len(before))
	for _, name := range before {
		if remaining[name] > 0 {
			remaining[name]--
			continue
		}
		dropped = append(dropped, name)
	}
	if len(dropped) == 0 {
		return "added " + strings.Join(missingNames(after, before), ", ")
	}
	return strings.Join(dropped, ", ") + " dropped"
}

func toolNames(trace []supportai.ToolTraceEntry) []string {
	out := make([]string, 0, len(trace))
	for _, entry := range trace {
		out = append(out, entry.Tool)
	}
	return out
}

func missingNames(current, baseline []string) []string {
	remaining := map[string]int{}
	for _, name := range baseline {
		remaining[name]++
	}
	out := make([]string, 0, len(current))
	for _, name := range current {
		if remaining[name] > 0 {
			remaining[name]--
			continue
		}
		out = append(out, name)
	}
	return out
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// printEvalDiff 按任务书的形状打印比对结果：逐条 changed 在前，末行汇总 unchanged。
//
// changed 在前是刻意的：退化是需要行动的部分，不该被埋在一长串 unchanged 之后。
func printEvalDiff(out io.Writer, reports []evalReport) {
	unchanged := 0
	missing := 0
	sampled := make([]evalReport, 0, len(reports))
	changed := 0
	for _, report := range reports {
		switch {
		case report.MissingCurrent:
			missing++
			fmt.Fprintf(out, "[missing] %s\n  基线里有这条 fixture，本次没有跑到（用例被删除或改名？）\n", report.ID)
		case report.MissingBaseline:
			unchanged++
			fmt.Fprintf(out, "[new] %s\n  基线里没有这条 fixture，已按当前结果新增\n", report.ID)
		case report.PromptShifted:
			// 指纹变了 = 这次改动真的到了模型输入。这是唯一算「生效」的情形。
			changed++
			fmt.Fprintf(out, "[changed] %s\n", report.ID)
			for _, change := range report.Changed {
				fmt.Fprintf(out, "  %s: %s → %s\n", change.Field, change.Before, change.After)
			}
		case report.changed():
			// 指纹相同却产出不同：provider 采样。列出来供 triage，但不计入退化。
			sampled = append(sampled, report)
			unchanged++
		default:
			unchanged++
		}
	}
	if changed > 0 {
		fmt.Fprintf(out, "[changed] %d fixtures 的 prompt 指纹发生变化\n", changed)
	}
	if unchanged > 0 {
		fmt.Fprintf(out, "[unchanged] %d fixtures\n", unchanged)
	}
	if len(sampled) > 0 {
		fmt.Fprintf(out, "[sampled] %d fixtures: prompt 指纹未变，产出文本因采样而不同（不是退化，仅供 triage）\n", len(sampled))
		for _, report := range sampled {
			fmt.Fprintf(out, "  %s: %d fields (%s)\n", report.ID, len(report.Changed), changeFields(report.Changed))
		}
	}
	if missing > 0 {
		fmt.Fprintf(out, "[missing] %d fixtures 只在基线里存在\n", missing)
	}
}

// changeFields 把变化字段名压成一行，供 [sampled] 的紧凑输出使用。
func changeFields(changes []evalFieldChange) string {
	names := make([]string, 0, len(changes))
	for _, change := range changes {
		names = append(names, change.Field)
	}
	return strings.Join(names, ", ")
}

// buildEvalReports 把「基线目录」与「本次结果」对齐成一份可打印的报告。
//
// 两侧都按 id 排序，因此输出顺序与目录遍历顺序无关——这是 diff 能被 CI 或另一个
// 人复核的同一理由。
func buildEvalReports(baseline map[string]evalSnapshot, current map[string]evalSnapshot) []evalReport {
	ids := make([]string, 0, len(baseline)+len(current))
	seen := map[string]struct{}{}
	for id := range baseline {
		ids = append(ids, id)
		seen[id] = struct{}{}
	}
	for id := range current {
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	reports := make([]evalReport, 0, len(ids))
	for _, id := range ids {
		base, inBaseline := baseline[id]
		now, inCurrent := current[id]
		switch {
		case inBaseline && inCurrent:
			report := evalReport{ID: id, Changed: diffEvalSnapshot(base, now)}
			// 指纹是「输入有没有变」的权威判据；文本差异只是结果。两者分开报，
			// 否则一次采样抖动会被读成一次退化，门禁就不可用了。
			report.PromptShifted = base.PromptDigest != now.PromptDigest
			report.SampledOnly = !report.PromptShifted && report.changed()
			reports = append(reports, report)
		case inCurrent:
			reports = append(reports, evalReport{ID: id, MissingBaseline: true})
		default:
			reports = append(reports, evalReport{ID: id, MissingCurrent: true})
		}
	}
	return reports
}

// loadEvalBaseline 读取基线目录下全部 *.json。
//
// 目录不存在时返回空 map 而不是错误：首次生成基线时它本来就不存在，那不是问题。
func loadEvalBaseline(dir string) (map[string]evalSnapshot, error) {
	snapshots := map[string]evalSnapshot{}
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return snapshots, nil
	}
	entries, err := os.ReadDir(trimmed)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshots, nil
		}
		return nil, fmt.Errorf("read baseline dir %s: %w", trimmed, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		snapshot, err := readEvalSnapshot(filepath.Join(trimmed, entry.Name()))
		if err != nil {
			return nil, err
		}
		id := strings.TrimSpace(snapshot.ID)
		if id == "" {
			id = strings.TrimSuffix(entry.Name(), ".json")
			snapshot.ID = id
		}
		snapshots[id] = snapshot
	}
	return snapshots, nil
}
