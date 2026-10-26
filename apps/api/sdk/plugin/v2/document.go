package pluginv2

import (
	"fmt"
	"regexp"
	"strings"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

// contractVersionPattern 对齐 ExtensionManifest.contractVersionPattern：id@positiveVersion。
// ContractVersion 与 schema ref 同形，但语义不同：前者是声明契约，后者是 TypedDocument 绑定。
var contractVersionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)

// validManifestHandler 对齐 ExtensionManifest.validHandler：非空、无 URL scheme、无路径穿越。
func validManifestHandler(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.Contains(value, "://") && !strings.Contains(value, "..")
}

// validContractVersion 要求 Manifest contractVersion 形如 id@positiveVersion，拒绝裸 "1"。
func validContractVersion(value string) bool {
	return contractVersionPattern.MatchString(strings.TrimSpace(value))
}

// SchemaRef 将 Manifest 风格的 `id@version` 引用解析为 TypedDocument 字段。
// 与 Host/Jobs SplitVersionedSchema 语义一致，供作者侧复用。
func SplitSchemaRef(reference string) (schemaID, version string, ok bool) {
	reference = strings.TrimSpace(reference)
	index := strings.LastIndexByte(reference, '@')
	if index <= 0 || index == len(reference)-1 {
		return "", "", false
	}
	version = reference[index+1:]
	if version == "" || version[0] == '0' {
		return "", "", false
	}
	for _, r := range version {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return reference[:index], version, true
}

// JoinSchemaRef 构造稳定的 `id@version` 引用。
func JoinSchemaRef(schemaID, version string) string {
	return strings.TrimSpace(schemaID) + "@" + strings.TrimSpace(version)
}

// DocumentMatchesSchema 检查 TypedDocument 是否绑定给定 schema 引用。
func DocumentMatchesSchema(document *protocolwire.TypedDocument, schemaRef string) bool {
	schemaID, version, ok := SplitSchemaRef(schemaRef)
	if !ok || document == nil {
		return false
	}
	return document.GetSchemaId() == schemaID && document.GetSchemaVersion() == version
}

// NewTypedDocument 从 map 构造绑定 schema 的 TypedDocument。
// 仅绑定 id@version 身份；不做 JSON Schema 值校验。
func NewTypedDocument(schemaRef string, values map[string]any) (*protocolwire.TypedDocument, error) {
	schemaID, version, ok := SplitSchemaRef(schemaRef)
	if !ok {
		return nil, fmt.Errorf("pluginv2: invalid schema ref %q", schemaRef)
	}
	if values == nil {
		values = map[string]any{}
	}
	encoded, err := structpb.NewStruct(values)
	if err != nil {
		return nil, err
	}
	return &protocolwire.TypedDocument{
		SchemaId: schemaID, SchemaVersion: version, Value: encoded,
	}, nil
}

// TypedDocumentValues 安全读取 TypedDocument 的 map 值。
func TypedDocumentValues(document *protocolwire.TypedDocument) map[string]any {
	if document == nil || document.GetValue() == nil {
		return map[string]any{}
	}
	return document.GetValue().AsMap()
}
