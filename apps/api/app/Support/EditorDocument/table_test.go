package editordocument_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

func acceptTable(t *testing.T, native string) editordocument.Accepted {
	t.Helper()
	accepted, err := editordocument.Accept(editordocument.Input{
		NativeJSON: []byte(native),
		Schema:     editordocument.CoreSchema(),
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return accepted
}

func containsFallback(fallbacks []string, want string) bool {
	for _, fallback := range fallbacks {
		if fallback == want {
			return true
		}
	}
	return false
}

func TestAcceptNativeTableRendersHTMLAndGFMMarkdown(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"tableRow","content":[
			{"type":"tableHeader","attrs":{"align":"center","colspan":1,"colwidth":[120]},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"名称"}]}]},
			{"type":"tableHeader","content":[
				{"type":"paragraph","content":[{"type":"text","text":"值"}]}]}
		]},
		{"type":"tableRow","content":[
			{"type":"tableCell","content":[
				{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
			{"type":"tableCell","attrs":{"align":"right"},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"1"}]}]}
		]}
	]}]}`
	accepted := acceptTable(t, native)

	for _, fragment := range []string{
		"<table><thead><tr><th align=\"center\">",
		"<tbody><tr><td>",
		`<td align="right">`,
		"</tbody></table>",
	} {
		if !strings.Contains(accepted.HTMLSanitized, fragment) {
			t.Fatalf("html missing %q: %s", fragment, accepted.HTMLSanitized)
		}
	}
	for _, forbidden := range []string{"style=", "colgroup"} {
		if strings.Contains(accepted.HTMLSanitized, forbidden) {
			t.Fatalf("html must not contain %q: %s", forbidden, accepted.HTMLSanitized)
		}
	}

	wantMarkdown := "| 名称 | 值 |\n| :---: | --- |\n| a | 1 |"
	if accepted.Markdown != wantMarkdown {
		t.Fatalf("markdown =\n%s\nwant:\n%s", accepted.Markdown, wantMarkdown)
	}

	table := accepted.Native.Content[0]
	if table.Type != "table" || len(table.Content) != 2 {
		t.Fatalf("normalized table = %#v", table)
	}
	header := table.Content[0].Content[0]
	if _, exists := header.Attrs["colwidth"]; exists {
		t.Fatalf("colwidth must be filtered: %#v", header.Attrs)
	}
	if header.Attrs["align"] != "center" {
		t.Fatalf("header align = %#v", header.Attrs["align"])
	}
}

func TestAcceptRepairsTableStructure(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"paragraph","content":[{"type":"text","text":"outer"}]},
		{"type":"tableRow","content":[
			{"type":"paragraph","content":[{"type":"text","text":"stray"}]},
			{"type":"tableHeader","content":[{"type":"text","text":"标题"}]},
			{"type":"tableCell"}
		]},
		{"type":"tableRow"}
	]}]}`
	accepted := acceptTable(t, native)

	if !containsFallback(accepted.Fallbacks, "table") || !containsFallback(accepted.Fallbacks, "tableRow") {
		t.Fatalf("fallbacks = %#v", accepted.Fallbacks)
	}
	want := "<table><tbody><tr><th><p>标题</p></th><td><p></p></td></tr></tbody></table>"
	if accepted.HTMLSanitized != want {
		t.Fatalf("html = %q, want %q", accepted.HTMLSanitized, want)
	}

	table := accepted.Native.Content[0]
	if len(table.Content) != 1 || table.Content[0].Type != "tableRow" {
		t.Fatalf("normalized table = %#v", table)
	}
	row := table.Content[0]
	if len(row.Content) != 2 {
		t.Fatalf("normalized row = %#v", row)
	}
	if row.Content[0].Content[0].Type != "paragraph" {
		t.Fatalf("inline cell text was not wrapped: %#v", row.Content[0])
	}
	if len(row.Content[1].Content) != 1 || row.Content[1].Content[0].Type != "paragraph" {
		t.Fatalf("empty cell was not padded: %#v", row.Content[1])
	}
}

func TestAcceptClampsTableSpansAndDropsInvalidAttrs(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"tableRow","content":[
			{"type":"tableCell","attrs":{"colspan":99,"rowspan":0,"align":"justify","colwidth":[80],"style":"text-align:left"},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"big"}]}]},
			{"type":"tableCell","attrs":{"colspan":"two","rowspan":-3},"content":[
				{"type":"paragraph","content":[{"type":"text","text":"bad"}]}]}
		]}
	]}]}`
	accepted := acceptTable(t, native)

	row := accepted.Native.Content[0].Content[0]
	first := row.Content[0]
	if first.Attrs["colspan"] != 20 || first.Attrs["rowspan"] != 1 {
		t.Fatalf("clamped attrs = %#v", first.Attrs)
	}
	if _, exists := first.Attrs["align"]; exists {
		t.Fatalf("invalid align retained: %#v", first.Attrs)
	}
	if _, exists := first.Attrs["colwidth"]; exists {
		t.Fatalf("colwidth retained: %#v", first.Attrs)
	}
	if _, exists := first.Attrs["style"]; exists {
		t.Fatalf("style retained: %#v", first.Attrs)
	}
	second := row.Content[1]
	if _, exists := second.Attrs["colspan"]; exists {
		t.Fatalf("non-numeric colspan retained: %#v", second.Attrs)
	}
	if second.Attrs["rowspan"] != 1 {
		t.Fatalf("negative rowspan = %#v", second.Attrs["rowspan"])
	}
	if !strings.Contains(accepted.HTMLSanitized, `colspan="20"`) {
		t.Fatalf("html missing clamped colspan: %s", accepted.HTMLSanitized)
	}
}

func TestAcceptDropsTableCellsOutsideTable(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[
		{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"orphan"}]}]},
		{"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"loose"}]}]}]}
	]}`
	accepted := acceptTable(t, native)
	if accepted.HTMLSanitized != "" {
		t.Fatalf("orphan cells rendered: %q", accepted.HTMLSanitized)
	}
	if accepted.Markdown != "" {
		t.Fatalf("orphan cells projected: %q", accepted.Markdown)
	}
}

func TestAcceptEmptyTableFallsBackToParagraph(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"paragraph","content":[{"type":"text","text":"no rows"}]}
	]}]}`
	accepted := acceptTable(t, native)
	if accepted.HTMLSanitized != "<p>[table]</p>" {
		t.Fatalf("html = %q", accepted.HTMLSanitized)
	}
	if !containsFallback(accepted.Fallbacks, "table") {
		t.Fatalf("fallbacks = %#v", accepted.Fallbacks)
	}
}

func TestAcceptTruncatesOversizedTables(t *testing.T) {
	t.Parallel()
	var rows []string
	for index := 0; index < 201; index++ {
		rows = append(rows, fmt.Sprintf(
			`{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"r%d"}]}]}]}`,
			index,
		))
	}
	accepted := acceptTable(t, `{"type":"doc","content":[{"type":"table","content":[`+strings.Join(rows, ",")+`]}]}`)
	table := accepted.Native.Content[0]
	if len(table.Content) != 200 {
		t.Fatalf("row count = %d", len(table.Content))
	}
	if !containsFallback(accepted.Fallbacks, "table") {
		t.Fatalf("fallbacks = %#v", accepted.Fallbacks)
	}

	var cells []string
	for index := 0; index < 60; index++ {
		cells = append(cells, fmt.Sprintf(
			`{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"c%d"}]}]}`,
			index,
		))
	}
	wide := acceptTable(t, `{"type":"doc","content":[{"type":"table","content":[{"type":"tableRow","content":[`+strings.Join(cells, ",")+`]}]}]}`)
	wideRow := wide.Native.Content[0].Content[0]
	if len(wideRow.Content) != 50 {
		t.Fatalf("column count = %d", len(wideRow.Content))
	}
	if !containsFallback(wide.Fallbacks, "tableRow") {
		t.Fatalf("fallbacks = %#v", wide.Fallbacks)
	}
}

func TestRenderMarkdownTableEscapesPipesAndLineBreaks(t *testing.T) {
	t.Parallel()
	native := `{"type":"doc","content":[{"type":"table","content":[
		{"type":"tableRow","content":[
			{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"a|b"}]}]},
			{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"multi"}]}]}
		]},
		{"type":"tableRow","content":[
			{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"x"}]}]},
			{"type":"tableCell","content":[
				{"type":"paragraph","content":[{"type":"text","text":"one"}]},
				{"type":"paragraph","content":[{"type":"text","text":"two"}]}
			]}
		]}
	]}]}`
	accepted := acceptTable(t, native)
	want := "| a\\|b | multi |\n| --- | --- |\n| x | one<br>two |"
	if accepted.Markdown != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", accepted.Markdown, want)
	}
}

func TestSanitizeHTMLKeepsTableAlignmentAndSpans(t *testing.T) {
	t.Parallel()
	sanitized := editordocument.SanitizeHTML(`<table><tbody><tr><td align="center" colspan="2" rowspan="3" style="color:red">x</td></tr></tbody></table>`)
	for _, fragment := range []string{`align="center"`, `colspan="2"`, `rowspan="3"`} {
		if !strings.Contains(sanitized, fragment) {
			t.Fatalf("sanitized html missing %q: %s", fragment, sanitized)
		}
	}
	if strings.Contains(sanitized, "style=") {
		t.Fatalf("sanitized html retained style: %s", sanitized)
	}
}

func TestShortcodeBlockRoundTripsGFMTable(t *testing.T) {
	t.Parallel()
	markdown := "[reply]\n| 名称 | 值 |\n| :--- | ---: |\n| a | 1 |\n\n[/reply]"
	doc, activated, err := editordocument.ParseShortcodeMarkdown(markdown, "comment")
	if err != nil || !activated {
		t.Fatalf("ParseShortcodeMarkdown = %#v, %v, %v", doc, activated, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Type != editordocument.ShortcodeBlockNode {
		t.Fatalf("doc = %#v", doc)
	}
	body := doc.Content[0].Content
	if len(body) != 1 || body[0].Type != "table" {
		t.Fatalf("shortcode body = %#v", body)
	}
	header := body[0].Content[0]
	if len(header.Content) != 2 || header.Content[0].Type != "tableHeader" {
		t.Fatalf("table header row = %#v", header)
	}
	if header.Content[0].Attrs["align"] != "left" || header.Content[1].Attrs["align"] != "right" {
		t.Fatalf("header alignments = %#v / %#v", header.Content[0].Attrs, header.Content[1].Attrs)
	}

	native, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := editordocument.Accept(editordocument.Input{
		NativeJSON:   native,
		Schema:       editordocument.CoreSchema(),
		ResourceKind: "comment",
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Markdown != markdown {
		t.Fatalf("round trip =\n%s\nwant:\n%s", accepted.Markdown, markdown)
	}
}
