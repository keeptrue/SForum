package extensionmanifest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	manifestV3SchemaURL                     = "https://sforum.dev/schemas/extensions/manifest-v3.schema.json"
	routeMutableRequestPointerSchemaFormat  = "sforum-route-mutable-request-pointer"
	routeMutableResponsePointerSchemaFormat = "sforum-route-mutable-response-pointer"
)

//go:embed schemas/manifest-v3.schema.json
var manifestV3SchemaBody []byte

var (
	manifestSchemaOnce sync.Once
	manifestSchemas    map[string]*jsonschema.Schema
	manifestSchemaErr  error
)

func ManifestV3JSONSchema() []byte {
	return append([]byte(nil), manifestV3SchemaBody...)
}

func ValidateV3JSONSchema(body []byte) error {
	return validateV3JSONSchemaFragment(body, "")
}

func validateV3JSONSchemaFragment(body []byte, fragment string) error {
	schema, err := compiledManifestSchema(fragment)
	if err != nil {
		return fmt.Errorf("%w: compile V3 JSON Schema: %v", ErrInvalidManifest, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: decode V3 JSON: %v", ErrInvalidManifest, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("%w: trailing V3 JSON value", ErrInvalidManifest)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("%w: V3 JSON Schema: %v", ErrInvalidManifest, err)
	}
	return nil
}

func compiledManifestSchema(fragment string) (*jsonschema.Schema, error) {
	manifestSchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.RegisterFormat(&jsonschema.Format{
			Name: routeMutableRequestPointerSchemaFormat,
			Validate: func(value any) error {
				pointer, ok := value.(string)
				if !ok || ValidRouteMutableRequestPointer(pointer, true) {
					return nil
				}
				return jsonschema.LocalizableError("must target the route request mutation document within the RFC 6901 budget")
			},
		})
		compiler.RegisterFormat(&jsonschema.Format{
			Name: routeMutableResponsePointerSchemaFormat,
			Validate: func(value any) error {
				pointer, ok := value.(string)
				if !ok || ValidRouteMutableResponsePointer(pointer) {
					return nil
				}
				return jsonschema.LocalizableError("must target the route response mutation document within the RFC 6901 budget")
			},
		})
		compiler.AssertFormat()
		var resource any
		if err := json.Unmarshal(manifestV3SchemaBody, &resource); err != nil {
			manifestSchemaErr = err
			return
		}
		if err := compiler.AddResource(manifestV3SchemaURL, resource); err != nil {
			manifestSchemaErr = err
			return
		}
		manifestSchemas = map[string]*jsonschema.Schema{}
		// 追加片段时**必须与 schemas/manifest-v3.schema.json 的 $defs 同批**：
		// 这里多一个名字但 $defs 里没有对应成员，compiler.Compile 在 init 期就失败，
		// 全仓的清单校验会一起挂（不是「只影响这个分片」）。
		fragments := []string{"", "guards", "schedules", "components", "templates", "assets", "content", "editor", "database", "cache", "seo", "services", "commands", "adminSurfaces", "adminPages", "aiTools", "badgeConditions", "queries", "queryResultFilters", "identity", "permissionDefinitions", "notificationTypes", "media", "navigation", "regions", "dependencies", "lifecycle", "openapi", "packageFiles"}
		for _, name := range fragments {
			location := manifestV3SchemaURL
			if name != "" {
				location += "#/$defs/" + name
			}
			schema, err := compiler.Compile(location)
			if err != nil {
				manifestSchemaErr = err
				return
			}
			manifestSchemas[name] = schema
		}
	})
	if manifestSchemaErr != nil {
		return nil, manifestSchemaErr
	}
	schema, ok := manifestSchemas[fragment]
	if !ok {
		return nil, fmt.Errorf("unknown manifest schema fragment %q", fragment)
	}
	return schema, nil
}
