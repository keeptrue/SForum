package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// view=schema 公开页的宿主边界。
//
// 页面声明（theme.json）只给出两个 id：描述符文件与它必须满足的精确 schema 引用；
// 真正的内容是包内 JSON，因此这里负责三件事——精确制品（kind=schema + 摘要一致）、
// 契约（描述符满足包内声明的 JSON Schema）、有界性（只允许四种页面种类，并对
// 字段/条目/文本设上限）。
//
// 放在 SDK 而不是扩展层是有意的：`extension validate`（CLI）与运行时激活都经由
// PreflightExactTemplateRuntime，作者与运行时必须用同一套校验，否则会出现
// 「CLI 说 0 error、激活被拒」的旧问题。
const (
	schemaViewMaximumBytes       = 1 << 20
	schemaViewMaximumFields      = 24
	schemaViewMaximumItems       = 64
	schemaViewMaximumCells       = 64
	schemaViewMaximumOptions     = 64
	schemaViewMaximumTextLength  = 2000
	schemaViewMaximumShortLength = 200
)

var schemaViewKinds = map[string]bool{"list": true, "detail": true, "form": true, "notice": true}

var schemaViewFieldTypes = map[string]bool{
	"text": true, "textarea": true, "number": true, "boolean": true, "select": true,
}

var schemaViewTones = map[string]bool{
	"neutral": true, "primary": true, "success": true, "warning": true, "error": true,
}

// validateSchemaViewPages 校验所有 view=schema 页面贡献。
func validateSchemaViewPages(
	root string,
	manifest extensionmanifest.Manifest,
	contributions []pages.PageContribution,
) error {
	for _, contribution := range contributions {
		if pages.NormalizePageView(string(contribution.View)) != pages.PageViewSchema {
			continue
		}
		if err := validateSchemaViewPage(root, manifest, contribution); err != nil {
			return fmt.Errorf("page %s: %w", contribution.ID, err)
		}
	}
	return nil
}

func validateSchemaViewPage(
	root string,
	manifest extensionmanifest.Manifest,
	contribution pages.PageContribution,
) error {
	_, err := LoadSchemaViewDescriptor(
		root, manifest, contribution.SchemaDescriptorFile, contribution.SchemaContractRef,
	)
	return err
}

// LoadSchemaViewDescriptor 读取并校验一个有界页面描述符。
//
// 预检（PreflightExactTemplateRuntime）与解析期把描述符交给 Web 的路径共用这一实现：
// 校验和返回必须是同一次读取，否则会出现「预检通过、返回另一种形状」。
func LoadSchemaViewDescriptor(
	packageRoot string,
	manifest extensionmanifest.Manifest,
	descriptorFileID string,
	contractReference string,
) (map[string]any, error) {
	descriptorFile, ok := declaredSchemaFile(manifest, descriptorFileID, "")
	if !ok {
		return nil, fmt.Errorf("schema descriptor %q is not a declared kind=schema package file", descriptorFileID)
	}
	descriptorBody, err := readExactSchemaFile(packageRoot, descriptorFile)
	if err != nil {
		return nil, fmt.Errorf("schema descriptor: %w", err)
	}
	document, err := decodeSchemaDocument(descriptorBody)
	if err != nil {
		return nil, fmt.Errorf("schema descriptor: %w", err)
	}

	schemaID, schemaVersion, err := splitSchemaReference(contractReference)
	if err != nil {
		return nil, fmt.Errorf("schema contract: %w", err)
	}
	contractFile, ok := declaredSchemaFile(manifest, schemaID, schemaVersion)
	if !ok {
		return nil, fmt.Errorf("schema contract %q is not a declared kind=schema package file", contractReference)
	}
	contractBody, err := readExactSchemaFile(packageRoot, contractFile)
	if err != nil {
		return nil, fmt.Errorf("schema contract: %w", err)
	}
	contract, err := compileSchemaDocument(contractBody, contractFile.Digest)
	if err != nil {
		return nil, fmt.Errorf("schema contract: %w", err)
	}
	if err := contract.Validate(document); err != nil {
		return nil, fmt.Errorf("schema descriptor does not satisfy its contract: %w", err)
	}
	if err := validateBoundedSchemaDescriptor(document); err != nil {
		return nil, err
	}
	descriptor, ok := document.(map[string]any)
	if !ok {
		return nil, errors.New("schema descriptor must be a JSON object")
	}
	return descriptor, nil
}

func declaredSchemaFile(
	manifest extensionmanifest.Manifest,
	id string,
	version string,
) (extensionmanifest.ManifestPackageFile, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return extensionmanifest.ManifestPackageFile{}, false
	}
	for _, file := range manifest.PackageFiles {
		if file.Kind != "schema" || file.ID != id {
			continue
		}
		if version != "" && file.Version != version {
			continue
		}
		if strings.TrimSpace(file.Path) == "" || len(file.Digest) != sha256.Size*2 {
			return extensionmanifest.ManifestPackageFile{}, false
		}
		return file, true
	}
	return extensionmanifest.ManifestPackageFile{}, false
}

// readExactSchemaFile 读取包内 schema 文件并要求摘要与声明一致（精确制品）。
func readExactSchemaFile(root string, file extensionmanifest.ManifestPackageFile) ([]byte, error) {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(cleanRoot, filepath.FromSlash(file.Path))
	relative, err := filepath.Rel(cleanRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return nil, errors.New("schema path escapes the package root")
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("schema file is not a regular file inside the package")
	}
	body, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > schemaViewMaximumBytes {
		return nil, errors.New("schema file exceeds Host bounds")
	}
	digest := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), file.Digest) {
		return nil, errors.New("schema file digest does not match the exact package declaration")
	}
	return body, nil
}

func decodeSchemaDocument(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("schema document contains trailing JSON")
	}
	return document, nil
}

func compileSchemaDocument(body []byte, digest string) (*jsonschema.Schema, error) {
	document, err := decodeSchemaDocument(body)
	if err != nil {
		return nil, err
	}
	resource := "https://sforum.invalid/page-schema/" + digest + ".json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, err
	}
	return compiler.Compile(resource)
}

func splitSchemaReference(reference string) (string, string, error) {
	reference = strings.TrimSpace(reference)
	separator := strings.LastIndex(reference, "@")
	if separator <= 0 || separator == len(reference)-1 {
		return "", "", fmt.Errorf("%q must be declared as <schemaId>@<version>", reference)
	}
	return reference[:separator], reference[separator+1:], nil
}

// validateBoundedSchemaDescriptor 是「有界页面描述符」的宿主规则：
// 只允许 list/detail/form/notice，并对字段、条目、选项与文本长度设上限。
// 这些边界与 Web 侧公开渲染器允许渲染的形状一致，避免包声明出宿主渲染不了的内容。
func validateBoundedSchemaDescriptor(document any) error {
	descriptor, ok := document.(map[string]any)
	if !ok {
		return errors.New("schema descriptor must be a JSON object")
	}
	kind, _ := descriptor["kind"].(string)
	if !schemaViewKinds[strings.TrimSpace(kind)] {
		return fmt.Errorf("schema descriptor kind %q is not one of list|detail|form|notice", kind)
	}
	for _, key := range []string{"title", "description", "message", "icon"} {
		if err := boundSchemaViewText(descriptor, key, schemaViewMaximumShortLength); err != nil {
			return err
		}
	}
	if tone, declared := descriptor["tone"]; declared {
		value, isString := tone.(string)
		if !isString || !schemaViewTones[strings.TrimSpace(value)] {
			return fmt.Errorf("schema descriptor tone %v is not a supported tone", tone)
		}
	}
	if err := boundSchemaViewOptions(descriptor["options"], "options"); err != nil {
		return err
	}
	if err := boundSchemaViewItems(descriptor["items"]); err != nil {
		return err
	}
	if err := boundSchemaViewCells(descriptor["cells"]); err != nil {
		return err
	}
	return boundSchemaViewFields(descriptor["fields"])
}

func boundSchemaViewText(descriptor map[string]any, key string, maximum int) error {
	value, declared := descriptor[key]
	if !declared {
		return nil
	}
	text, isString := value.(string)
	if !isString {
		return fmt.Errorf("schema descriptor %s must be a string", key)
	}
	if len([]rune(text)) > maximum {
		return fmt.Errorf("schema descriptor %s exceeds %d characters", key, maximum)
	}
	return nil
}

func boundSchemaViewOptions(value any, key string) error {
	if value == nil {
		return nil
	}
	options, ok := value.([]any)
	if !ok {
		return fmt.Errorf("schema descriptor %s must be an array", key)
	}
	if len(options) > schemaViewMaximumOptions {
		return fmt.Errorf("schema descriptor %s exceeds %d entries", key, schemaViewMaximumOptions)
	}
	for _, option := range options {
		entry, ok := option.(map[string]any)
		if !ok {
			return fmt.Errorf("schema descriptor %s entries must be objects", key)
		}
		if err := requireBoundedString(entry, "label", schemaViewMaximumShortLength); err != nil {
			return err
		}
	}
	return nil
}

func boundSchemaViewItems(value any) error {
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return errors.New("schema descriptor items must be an array")
	}
	if len(items) > schemaViewMaximumItems {
		return fmt.Errorf("schema descriptor items exceed %d entries", schemaViewMaximumItems)
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return errors.New("schema descriptor items entries must be objects")
		}
		if err := requireBoundedString(entry, "label", schemaViewMaximumShortLength); err != nil {
			return err
		}
	}
	return nil
}

func boundSchemaViewCells(value any) error {
	if value == nil {
		return nil
	}
	cells, ok := value.(map[string]any)
	if !ok {
		return errors.New("schema descriptor cells must be an object")
	}
	if len(cells) > schemaViewMaximumCells {
		return fmt.Errorf("schema descriptor cells exceed %d keys", schemaViewMaximumCells)
	}
	for key, cell := range cells {
		if len([]rune(key)) > schemaViewMaximumShortLength {
			return fmt.Errorf("schema descriptor cell key %q is too long", key)
		}
		if text, isString := cell.(string); isString && len([]rune(text)) > schemaViewMaximumTextLength {
			return fmt.Errorf("schema descriptor cell %s exceeds %d characters", key, schemaViewMaximumTextLength)
		}
	}
	return nil
}

func boundSchemaViewFields(value any) error {
	if value == nil {
		return nil
	}
	fields, ok := value.([]any)
	if !ok {
		return errors.New("schema descriptor fields must be an array")
	}
	if len(fields) > schemaViewMaximumFields {
		return fmt.Errorf("schema descriptor fields exceed %d entries", schemaViewMaximumFields)
	}
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		entry, ok := field.(map[string]any)
		if !ok {
			return errors.New("schema descriptor fields entries must be objects")
		}
		if err := requireBoundedString(entry, "key", 80); err != nil {
			return err
		}
		if err := requireBoundedString(entry, "label", schemaViewMaximumShortLength); err != nil {
			return err
		}
		key, _ := entry["key"].(string)
		if seen[key] {
			return fmt.Errorf("schema descriptor field key %q is duplicated", key)
		}
		seen[key] = true
		fieldType, _ := entry["type"].(string)
		if !schemaViewFieldTypes[strings.TrimSpace(fieldType)] {
			return fmt.Errorf("schema descriptor field %s has unsupported type %q", key, fieldType)
		}
		if err := boundSchemaViewOptions(entry["options"], "field options"); err != nil {
			return err
		}
	}
	return nil
}

func requireBoundedString(entry map[string]any, key string, maximum int) error {
	value, declared := entry[key]
	if !declared {
		return fmt.Errorf("schema descriptor entry is missing %s", key)
	}
	text, isString := value.(string)
	if !isString || strings.TrimSpace(text) == "" {
		return fmt.Errorf("schema descriptor %s must be a non-empty string", key)
	}
	if len([]rune(text)) > maximum {
		return fmt.Errorf("schema descriptor %s exceeds %d characters", key, maximum)
	}
	return nil
}
