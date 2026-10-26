package main

import (
	"errors"
	"fmt"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	rowActionCommand  = "sforum.admin-surface-reference.surface.row-action-command"
	bulkActionCommand = "sforum.admin-surface-reference.surface.bulk-action-command"
	formCommand       = "sforum.admin-surface-reference.surface.form-command"
	editorCommand     = "sforum.admin-surface-reference.surface.editor-command"
	importerCommand   = "sforum.admin-surface-reference.surface.importer-command"
)

func renderAdminSurface(
	declaration surfaceDeclaration,
	input map[string]any,
	request *protocolwire.RequestContext,
) (map[string]any, error) {
	resourceIDs := inputResourceIDs(input)
	switch declaration.handler {
	case "admin.navigation":
		return map[string]any{"title": "Surface reference", "pageId": "/users", "icon": "i-lucide-panels-top-left"}, nil
	case "admin.dashboard":
		return map[string]any{
			"title": "Admin surface reference", "value": 12, "tone": "success",
			"description": "All twelve typed surface kinds are available.", "icon": "i-lucide-badge-check",
		}, nil
	case "admin.list_column":
		cells := make(map[string]any, len(resourceIDs))
		for index, id := range resourceIDs {
			cells[id] = (index + 1) * 10
		}
		return map[string]any{"title": "Reference score", "cells": cells}, nil
	case "admin.list_filter":
		result := map[string]any{
			"title": "Reference review",
			"options": []any{
				map[string]any{"label": "Active", "value": "active"},
				map[string]any{"label": "Needs review", "value": "review"},
			},
		}
		selection, _ := input["selection"].(string)
		if selection != "" {
			result["visibleResourceIds"] = filteredResourceIDs(input, selection)
		}
		return result, nil
	case "admin.row_action_view":
		return actionDescriptor("Review user", rowActionCommand, resourceIDs), nil
	case "admin.bulk_action_view":
		return actionDescriptor("Review selected", bulkActionCommand, resourceIDs), nil
	case "admin.form_view":
		return formDescriptor("Reference note", formCommand, []any{
			field("note", "Note", "textarea", true),
		}, map[string]any{"note": ""}), nil
	case "admin.notice":
		return map[string]any{
			"title": "Reference plugin active", "message": "Typed admin surfaces are connected.",
			"tone": "primary", "icon": "i-lucide-plug-zap",
		}, nil
	case "admin.editor_view":
		resource := contextResource(input)
		return formDescriptor("Reference editor", editorCommand, []any{
			field("review_note", "Review note", "textarea", false),
		}, map[string]any{"review_note": stringValue(resource, "displayName")}), nil
	case "admin.detail":
		resource := contextResource(input)
		return map[string]any{
			"title": "Reference detail",
			"items": []any{
				map[string]any{"label": "User ID", "value": primitiveValue(resource["id"])},
				map[string]any{"label": "Status", "value": primitiveValue(resource["status"])},
			},
		}, nil
	case "admin.importer_view":
		return formDescriptor("Reference import", importerCommand, []any{
			selectField("mode", "Mode", []any{
				map[string]any{"label": "Validate only", "value": "validate"},
				map[string]any{"label": "Import", "value": "import"},
			}),
		}, map[string]any{"mode": "validate"}), nil
	case "admin.exporter":
		return map[string]any{
			"title": "Reference export", "icon": "i-lucide-file-down",
			"download": map[string]any{"url": "/api/v1/admin/admin-surfaces?kind=exporter", "filename": "admin-surfaces.json"},
		}, nil
	case "admin.row_action_command":
		return commandResult("User review recorded.", request), nil
	case "admin.bulk_action_command":
		return commandResult(fmt.Sprintf("%d users reviewed.", len(resourceIDs)), request), nil
	case "admin.form_command":
		return commandResult("Reference note saved.", request), nil
	case "admin.editor_command":
		return commandResult("Reference editor saved.", request), nil
	case "admin.importer_command":
		return commandResult("Reference import completed.", request), nil
	default:
		return nil, errors.New("surface handler is not implemented")
	}
}

func actionDescriptor(title, commandID string, resourceIDs []string) map[string]any {
	return map[string]any{
		"title": title, "commandSurfaceId": commandID, "icon": "i-lucide-shield-check",
		"visibleResourceIds": stringValues(resourceIDs),
	}
}

func formDescriptor(title, commandID string, fields []any, values map[string]any) map[string]any {
	return map[string]any{
		"title": title, "commandSurfaceId": commandID, "fields": fields, "values": values,
		"icon": "i-lucide-notebook-pen",
	}
}

func commandResult(message string, request *protocolwire.RequestContext) map[string]any {
	return map[string]any{
		"message": message,
		"refresh": true,
		"items": []any{
			map[string]any{"label": "Actor", "value": request.GetActor().GetUserId()},
			map[string]any{"label": "Idempotency key", "value": request.GetIdempotencyKey()},
		},
	}
}

func inputResourceIDs(input map[string]any) []string {
	resources, _ := input["resources"].([]any)
	result := make([]string, 0, len(resources))
	seen := map[string]bool{}
	for _, raw := range resources {
		resource, _ := raw.(map[string]any)
		id, _ := resource["id"].(string)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	if len(result) > 0 {
		return result
	}
	ids, _ := input["resourceIds"].([]any)
	for _, raw := range ids {
		id, _ := raw.(string)
		if id != "" && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func filteredResourceIDs(input map[string]any, selection string) []any {
	resources, _ := input["resources"].([]any)
	result := []any{}
	for _, raw := range resources {
		resource, _ := raw.(map[string]any)
		attributes, _ := resource["attributes"].(map[string]any)
		status, _ := attributes["status"].(string)
		matches := selection == "active" && status == "active" || selection == "review" && status != "active"
		if id, ok := resource["id"].(string); matches && ok && id != "" {
			result = append(result, id)
		}
	}
	return result
}

func contextResource(input map[string]any) map[string]any {
	contextValue, _ := input["context"].(map[string]any)
	resource, _ := contextValue["resource"].(map[string]any)
	return resource
}

func field(key, label, fieldType string, required bool) map[string]any {
	return map[string]any{"key": key, "label": label, "type": fieldType, "required": required}
}

func selectField(key, label string, options []any) map[string]any {
	return map[string]any{"key": key, "label": label, "type": "select", "required": true, "options": options}
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func primitiveValue(value any) any {
	switch value := value.(type) {
	case nil, string, bool, float64:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
