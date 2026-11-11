package pages

import (
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
)

// L1 模板安全边界：
// 1) 抽出 allowlist 宿主岛 → 占位符
// 2) bluemonday 严格 allowlist 净化其余 HTML
// 3) 校验并还原宿主岛
// 正则不承担 HTML 安全边界，仅用于 {{var}} 与岛占位。

const (
	MaxTemplateBytes  = 256 * 1024
	MaxTemplateDepth  = 32
	MaxTemplateAttrs  = 32
	MaxTemplateRender = 2 * time.Second
)

// 允许的宿主岛标签仅映射到已注册的 SF 组件。sf-extension-widget
// 本身不执行包代码；后续 ThemeCompiler 仍会强制 exact 组件身份。
var allowedHostIslands = map[string]struct{}{
	"sf-home-page":                  {},
	"sf-search-page":                {},
	"sf-category-index-page":        {},
	"sf-category-show-page":         {},
	"sf-tag-index-page":             {},
	"sf-tag-show-page":              {},
	"sf-badge-index-page":           {},
	"sf-topic-show-page":            {},
	"sf-profile-page":               {},
	"sf-notifications-page":         {},
	"sf-notification-detail-page":   {},
	"sf-moderation-review":          {},
	"sf-terms-page":                 {},
	"sf-privacy-page":               {},
	"sf-guidelines-page":            {},
	"sf-not-found-page":             {},
	"sf-error-details":              {},
	"sf-error-actions":              {},
	"sf-error-recovery":             {},
	"sf-error-sidebar":              {},
	"sf-error-rail":                 {},
	"sf-navbar":                     {},
	"sf-footer":                     {},
	"sf-home-navigation":            {},
	"sf-topic-composer":             {},
	"sf-topic-editor":               {},
	"sf-topic-reply":                {},
	"sf-profile-settings":           {},
	"sf-appearance-settings":        {},
	"sf-login-methods-settings":     {},
	"sf-local-password-settings":    {},
	"sf-security-settings":          {},
	"sf-personal-access-tokens":     {},
	"sf-notification-settings":      {},
	"sf-login-form":                 {},
	"sf-register-form":              {},
	"sf-external-auth-continuation": {},
	"sf-email-verification":         {},
	"sf-recovery-request":           {},
	"sf-recovery-confirm":           {},
	"sf-extension-widget":           {},
	"sf-plugin-page-body":           {},
	"sf-account-settings-sidebar":   {},
	"sf-content-column-footer":      {},
}

// 允许的普通 HTML 标签（无 script/style/svg/math/iframe/form/object/embed）。
var allowedHTMLTags = []string{
	"a", "abbr", "article", "aside", "b", "blockquote", "br", "caption",
	"cite", "code", "col", "colgroup", "dd", "div", "dl", "dt", "em",
	"figcaption", "figure", "footer", "h1", "h2", "h3", "h4", "h5", "h6",
	"header", "hr", "i", "img", "li", "main", "mark", "nav", "ol", "p",
	"pre", "section", "small", "span", "strong", "sub", "sup", "table",
	"tbody", "td", "tfoot", "th", "thead", "tr", "u", "ul",
}

var (
	tplMustache = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)
	// 匹配任意 sf-* 标签开标签（用于未知岛拒绝与提取）
	tplAnyIslandOpen = regexp.MustCompile(`(?is)<(/?)(sf-[a-z0-9-]+)((?:\s[^>]*)?)\s*(/?)>`)
	onEventAttr      = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	// 宿主岛安全属性名
	safeIslandAttrName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	safeIslandAttrVal  = regexp.MustCompile(`^[a-zA-Z0-9_.:/@#-]{0,128}$`)
	publicL2IslandID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
)

// 占位必须是 bluemonday allowlist 内的元素；HTML 注释会被剥离。
const islandPlaceholderPrefix = `<span id="sf-island-ph-`
const islandPlaceholderSuffix = `"></span>`

// themeTemplatePolicy 返回 L1 模板专用 bluemonday 策略（严格 allowlist）。
func themeTemplatePolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(allowedHTMLTags...)
	p.AllowAttrs("class", "id", "title", "role", "aria-label", "aria-hidden", "aria-describedby").Globally()
	p.AllowAttrs("colspan", "rowspan", "scope").OnElements("td", "th")
	p.AllowAttrs("start", "type").OnElements("ol")
	p.AllowAttrs("alt", "width", "height", "loading").OnElements("img")
	p.AllowAttrs("src").Matching(regexp.MustCompile(`^(https:)?//|^/[^/\\]|^[a-zA-Z0-9_./-]+$`)).OnElements("img")
	p.AllowAttrs("href").Matching(regexp.MustCompile(`^(https?:)?//|^/|^#|^mailto:`)).OnElements("a")
	p.AllowAttrs("rel", "target").OnElements("a")
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowStandardURLs()
	p.RequireParseableURLs(true)
	p.AllowURLSchemes("http", "https", "mailto")
	return p
}

// ValidateTemplate 检查 L1 模板源。
func ValidateTemplate(src string) error {
	if len(src) == 0 {
		return nil
	}
	if len(src) > MaxTemplateBytes {
		return fmt.Errorf("pages: template exceeds size limit (%d bytes)", MaxTemplateBytes)
	}
	if utf8.RuneCountInString(src) > MaxTemplateBytes {
		return fmt.Errorf("pages: template exceeds size limit")
	}
	lower := strings.ToLower(src)
	for _, bad := range []string{
		"<script", "</script", "<iframe", "<object", "<embed", "<style",
		"<svg", "<math", "<base", "<meta", "<form", "<link",
		"javascript:", "vbscript:", "data:text/html",
	} {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("pages: template contains forbidden construct %q", bad)
		}
	}
	// HTML 实体编码的 javascript: 变体
	decoded := html.UnescapeString(src)
	decodedLower := strings.ToLower(decoded)
	if strings.Contains(decodedLower, "javascript:") || strings.Contains(decodedLower, "vbscript:") {
		return fmt.Errorf("pages: template contains forbidden URL scheme")
	}
	// 换行拆分 javascript:
	compact := regexp.MustCompile(`(?i)java\s*script\s*:`).MatchString(decoded)
	if compact {
		return fmt.Errorf("pages: template contains forbidden URL scheme")
	}
	if onEventAttr.MatchString(src) {
		return fmt.Errorf("pages: template rejects inline event handlers")
	}
	if err := checkTemplateStructure(src); err != nil {
		return err
	}
	if err := rejectUnknownIslands(src); err != nil {
		return err
	}
	// 完整净化管线必须成功
	if _, err := SanitizeTemplateHTML(src); err != nil {
		return err
	}
	return nil
}

func rejectUnknownIslands(src string) error {
	matches := tplAnyIslandOpen.FindAllStringSubmatch(src, -1)
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := strings.ToLower(m[2])
		if _, ok := allowedHostIslands[name]; !ok {
			return fmt.Errorf("pages: template host island %q is not allowed", name)
		}
	}
	return nil
}

func checkTemplateStructure(src string) error {
	depth := 0
	maxDepth := 0
	i := 0
	for i < len(src) {
		if src[i] != '<' {
			i++
			continue
		}
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		j := i + 1
		if j < len(src) && src[j] == '/' {
			depth--
			if depth < 0 {
				depth = 0
			}
			for j < len(src) && src[j] != '>' {
				j++
			}
			i = j + 1
			continue
		}
		tagEnd := strings.IndexByte(src[i:], '>')
		if tagEnd < 0 {
			break
		}
		tagChunk := src[i : i+tagEnd]
		attrCount := strings.Count(tagChunk, "=")
		if attrCount > MaxTemplateAttrs {
			return fmt.Errorf("pages: template element has too many attributes")
		}
		selfClose := strings.HasSuffix(strings.TrimSpace(tagChunk), "/")
		if !selfClose {
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
			if maxDepth > MaxTemplateDepth {
				return fmt.Errorf("pages: template exceeds max depth %d", MaxTemplateDepth)
			}
		}
		i += tagEnd + 1
	}
	return nil
}

// SanitizeTemplateHTML 抽出宿主岛 → bluemonday → 还原岛。
// 返回错误当净化失败或岛属性非法。
func SanitizeTemplateHTML(src string) (string, error) {
	stripped, islands, err := extractAndPlaceholderIslands(src)
	if err != nil {
		return "", err
	}
	clean := themeTemplatePolicy().Sanitize(stripped)
	cleanLower := strings.ToLower(clean)
	for _, bad := range []string{"<script", "<iframe", "<object", "<embed", "<style", "<svg", "<math", "javascript:"} {
		if strings.Contains(cleanLower, bad) {
			return "", fmt.Errorf("pages: template sanitize left forbidden content")
		}
	}
	// 还原占位符；任一缺失则失败（bluemonday 不得吞掉岛）
	for i, island := range islands {
		ph := fmt.Sprintf("%s%d%s", islandPlaceholderPrefix, i, islandPlaceholderSuffix)
		if !strings.Contains(clean, ph) {
			return "", fmt.Errorf("pages: template island placeholder %d lost during sanitize", i)
		}
		clean = strings.Replace(clean, ph, island, 1)
	}
	if strings.Contains(clean, `id="sf-island-ph-`) {
		return "", fmt.Errorf("pages: template island placeholder residual after restore")
	}
	return clean, nil
}

// extractAndPlaceholderIslands 将 allowlist 宿主岛替换为 HTML 注释占位符。
func extractAndPlaceholderIslands(src string) (string, []string, error) {
	var islands []string
	var b strings.Builder
	last := 0
	for last < len(src) {
		loc := tplAnyIslandOpen.FindStringSubmatchIndex(src[last:])
		if loc == nil {
			b.WriteString(src[last:])
			break
		}
		abs := func(i int) int {
			if i < 0 {
				return i
			}
			return last + i
		}
		start := abs(loc[0])
		endOpen := abs(loc[1])
		isClose := src[abs(loc[2]):abs(loc[3])] == "/"
		tag := strings.ToLower(src[abs(loc[4]):abs(loc[5])])
		attrRaw := ""
		if loc[6] >= 0 {
			attrRaw = src[abs(loc[6]):abs(loc[7])]
		}
		selfClose := src[abs(loc[8]):abs(loc[9])] == "/"

		b.WriteString(src[last:start])

		if isClose {
			// 孤立闭标签丢弃
			last = endOpen
			continue
		}
		if _, ok := allowedHostIslands[tag]; !ok {
			return "", nil, fmt.Errorf("pages: template host island %q is not allowed", tag)
		}
		end := endOpen
		inner := ""
		if !selfClose {
			closeTag := "</" + tag + ">"
			idx := strings.Index(strings.ToLower(src[endOpen:]), closeTag)
			if idx < 0 {
				return "", nil, fmt.Errorf("pages: unclosed host island %q", tag)
			}
			inner = src[endOpen : endOpen+idx]
			end = endOpen + idx + len(closeTag)
		}
		safeAttrs, err := sanitizeIslandAttrs(tag, attrRaw)
		if err != nil {
			return "", nil, err
		}
		safeInner, err := sanitizeIslandInner(tag, inner)
		if err != nil {
			return "", nil, err
		}
		serialized := serializeIsland(tag, safeAttrs, safeInner)
		idx := len(islands)
		islands = append(islands, serialized)
		b.WriteString(fmt.Sprintf("%s%d%s", islandPlaceholderPrefix, idx, islandPlaceholderSuffix))
		last = end
	}
	return b.String(), islands, nil
}

func sanitizeIslandAttrs(tag, attrRaw string) (map[string]string, error) {
	out := map[string]string{}
	attrRe := regexp.MustCompile(`([a-zA-Z0-9:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	matches := attrRe.FindAllStringSubmatch(attrRaw, -1)
	if len(matches) > MaxTemplateAttrs {
		return nil, fmt.Errorf("pages: host island has too many attributes")
	}
	allowedKeys := map[string]struct{}{
		"name": {}, "page": {}, "variant": {}, "class": {}, "id": {},
	}
	// 岛自带的声明式 prop 也是合法属性：白名单由生产绑定表提供，因此新增一个岛
	// 只需要在 theme_body_island.go 声明它的 prop，不必再回来改这张通用表。
	// 插件模板写不出绑定表里没声明的属性（ThemeCompiler 的 validateIslandPropNames
	// 对主题模板有同一条约束）。
	if binding, ok := productionThemeIslandBindings()[tag]; ok {
		for _, prop := range binding.Props {
			allowedKeys[prop.Name] = struct{}{}
		}
	}
	if tag == "sf-extension-widget" {
		allowedKeys["extension-id"] = struct{}{}
		allowedKeys["component-id"] = struct{}{}
	}
	// 绑定表为该岛显式声明的 prop 名同样放行（值仍受 safeIslandAttrVal 约束）。
	if binding, ok := productionThemeIslandBindings()[tag]; ok {
		for _, prop := range binding.Props {
			allowedKeys[prop.Name] = struct{}{}
		}
	}
	for _, am := range matches {
		key := strings.ToLower(am[1])
		if strings.HasPrefix(key, "on") || key == "style" || key == "src" || key == "href" {
			return nil, fmt.Errorf("pages: host island forbids attribute %q", key)
		}
		if !safeIslandAttrName.MatchString(key) {
			return nil, fmt.Errorf("pages: host island invalid attribute name %q", key)
		}
		if _, ok := allowedKeys[key]; !ok {
			return nil, fmt.Errorf("pages: host island attribute %q not allowed", key)
		}
		val := am[2]
		if val == "" {
			val = am[3]
		}
		if val == "" {
			val = am[4]
		}
		if !safeIslandAttrVal.MatchString(val) {
			return nil, fmt.Errorf("pages: host island attribute %q has unsafe value", key)
		}
		out[key] = val
	}
	if tag == "sf-extension-widget" {
		extensionID, extensionOK := out["extension-id"]
		componentID, componentOK := out["component-id"]
		if !extensionOK || !componentOK || !publicL2IslandID.MatchString(extensionID) ||
			!publicL2IslandID.MatchString(componentID) || !strings.HasPrefix(componentID, extensionID+".") {
			return nil, fmt.Errorf("pages: public L2 host island requires exact component identity")
		}
	}
	// 绑定表声明为 required 的 prop 必须出现：主题侧 validateIslandPropNames 已有同一条
	// 约束，插件模板在这里补齐，避免漏传必填属性后只在运行时表现成空组件。
	if binding, ok := productionThemeIslandBindings()[tag]; ok {
		for _, prop := range binding.Props {
			if _, present := out[prop.Name]; prop.Required && !present {
				return nil, fmt.Errorf("pages: host island %q requires attribute %q", tag, prop.Name)
			}
		}
	}
	return out, nil
}

func sanitizeIslandInner(tag, inner string) (string, error) {
	if strings.TrimSpace(inner) == "" {
		return "", nil
	}
	if tag != "sf-extension-widget" {
		return "", fmt.Errorf("pages: host island %q must be empty", tag)
	}
	if tplAnyIslandOpen.MatchString(inner) {
		return "", fmt.Errorf("pages: public L2 fallback cannot contain a host island")
	}
	// 仅 SSR fallback 可以包含普通 allowlist HTML；可执行入口和身份仍来自 Host descriptor。
	return themeTemplatePolicy().Sanitize(inner), nil
}

func serializeIsland(tag string, attrs map[string]string, inner string) string {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(tag)
	// 稳定顺序
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	// 简单插入排序避免额外 import
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	for _, k := range keys {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(html.EscapeString(attrs[k]))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	b.WriteString(inner)
	b.WriteString("</")
	b.WriteString(tag)
	b.WriteByte('>')
	return b.String()
}

// LoadTemplate 从主题包读取、校验并返回净化后的模板。
func LoadTemplate(packageRoot, rel string) (string, error) {
	full, err := ResolveThemeAsset(packageRoot, rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if len(raw) > MaxTemplateBytes {
		return "", fmt.Errorf("pages: template exceeds size limit")
	}
	src := string(raw)
	if err := ValidateTemplate(src); err != nil {
		return "", err
	}
	return SanitizeTemplateHTML(src)
}

// RenderTemplate 替换 {{key}} 为转义文本，再二次 sanitize。
func RenderTemplate(src string, vars map[string]string) (string, error) {
	start := time.Now()
	if err := ValidateTemplate(src); err != nil {
		return "", err
	}
	interpolated := tplMustache.ReplaceAllStringFunc(src, func(m string) string {
		if time.Since(start) > MaxTemplateRender {
			return ""
		}
		sub := tplMustache.FindStringSubmatch(m)
		if len(sub) < 2 {
			return ""
		}
		if vars == nil {
			return ""
		}
		return html.EscapeString(vars[sub[1]])
	})
	if time.Since(start) > MaxTemplateRender {
		return "", fmt.Errorf("pages: template render timeout")
	}
	clean, err := SanitizeTemplateHTML(interpolated)
	if err != nil {
		return "", err
	}
	if time.Since(start) > MaxTemplateRender {
		return "", fmt.Errorf("pages: template render timeout")
	}
	return clean, nil
}

// TemplateSegment 是净化后模板的结构段。
type TemplateSegment struct {
	Type  string // html | island
	Value string
	Tag   string
	Attrs map[string]string
}

// ExtractHostIslands 在已净化 HTML 上提取宿主岛段。
func ExtractHostIslands(sanitized string) []TemplateSegment {
	out := make([]TemplateSegment, 0)
	last := 0
	for last < len(sanitized) {
		loc := tplAnyIslandOpen.FindStringSubmatchIndex(sanitized[last:])
		if loc == nil {
			break
		}
		abs := func(i int) int {
			if i < 0 {
				return i
			}
			return last + i
		}
		start := abs(loc[0])
		endOpen := abs(loc[1])
		isClose := sanitized[abs(loc[2]):abs(loc[3])] == "/"
		tag := strings.ToLower(sanitized[abs(loc[4]):abs(loc[5])])
		attrRaw := ""
		if loc[6] >= 0 {
			attrRaw = sanitized[abs(loc[6]):abs(loc[7])]
		}
		selfClose := sanitized[abs(loc[8]):abs(loc[9])] == "/"
		if start > last {
			out = append(out, TemplateSegment{Type: "html", Value: sanitized[last:start]})
		}
		end := endOpen
		if !isClose && !selfClose {
			closeTag := "</" + tag + ">"
			idx := strings.Index(strings.ToLower(sanitized[endOpen:]), closeTag)
			if idx >= 0 {
				end = endOpen + idx + len(closeTag)
			}
		}
		if !isClose {
			if _, ok := allowedHostIslands[tag]; ok {
				attrs, _ := sanitizeIslandAttrs(tag, attrRaw)
				out = append(out, TemplateSegment{Type: "island", Tag: tag, Attrs: attrs})
			}
		}
		last = end
	}
	if last < len(sanitized) {
		out = append(out, TemplateSegment{Type: "html", Value: sanitized[last:]})
	}
	return out
}
