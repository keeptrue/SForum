package http

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func entityMetaReadGuardEvaluator(policy EntityMetaValueGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if err := requireEntityMetaReadAuthority(ctx, evaluation); !errors.Is(err, routes.ErrCoreGuardEvaluatorUnavailable) {
			return err
		}
		if evaluation.Descriptor.RouteID != "core.route.entity_meta.list_values" || policy == nil ||
			evaluation.Request.Query != "" || len(bytes.TrimSpace(evaluation.Request.Body)) != 0 {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		entityType, entityID, ok := entityMetaGuardTarget(evaluation)
		if !ok {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		subject, err := policy.LoadValueGuardSubject(ctx, entityType, entityID, nil)
		if err != nil || !validEntityMetaGuardSubject(subject, entityType, entityID) {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return nil
	}
}

func entityMetaWriteGuardEvaluator(policy EntityMetaValueGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if evaluation.Descriptor.RouteID != "core.route.entity_meta.upsert_values" || policy == nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if err := requireAuthenticatedCoreGuardActor(ctx, evaluation); err != nil {
			return err
		}
		if evaluation.Request.Query != "" {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		entityType, entityID, ok := entityMetaGuardTarget(evaluation)
		if !ok {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		var input entityMetaUpsertGuardInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil || len(input.Values) == 0 {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		fieldKeys := make([]string, 0, len(input.Values))
		seen := make(map[string]struct{}, len(input.Values))
		for _, value := range input.Values {
			key := strings.TrimSpace(value.FieldKey)
			if key == "" {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				fieldKeys = append(fieldKeys, key)
			}
		}
		subject, err := policy.LoadValueGuardSubject(ctx, entityType, entityID, fieldKeys)
		if err != nil || !validEntityMetaGuardSubject(subject, entityType, entityID) || len(subject.Fields) != len(fieldKeys) {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return authorizeEntityMetaWrite(evaluation, subject, fieldKeys)
	}
}

func entityMetaGuardTarget(evaluation routes.CoreGuardEvaluation) (string, int64, bool) {
	entityType := strings.TrimSpace(evaluation.Request.Params["entityType"])
	if entityType != entitymeta.EntityUser && entityType != entitymeta.EntityTopic {
		return "", 0, false
	}
	entityID, err := strconv.ParseInt(evaluation.Request.Params["entityID"], 10, 64)
	return entityType, entityID, err == nil && entityID > 0
}

func validEntityMetaGuardSubject(subject entitymeta.ValueGuardSubject, entityType string, entityID int64) bool {
	return subject.Exists && subject.EntityType == entityType && subject.EntityID == entityID && subject.OwnerUserID > 0 &&
		(entityType != entitymeta.EntityUser || subject.OwnerUserID == entityID)
}

func authorizeEntityMetaWrite(
	evaluation routes.CoreGuardEvaluation,
	subject entitymeta.ValueGuardSubject,
	fieldKeys []string,
) error {
	manage := evaluation.Request.Permissions["*"] || evaluation.Request.Permissions[identity.PermissionEntityMetaManage]
	if !manage {
		if evaluation.Request.ActorID != subject.OwnerUserID ||
			subject.EntityType == entitymeta.EntityTopic &&
				!evaluation.Request.Permissions[identity.PermissionTopicEditOwn] &&
				!evaluation.Request.Permissions[identity.PermissionTopicEditAny] {
			return routes.ErrCoreGuardPermissionDenied
		}
	}
	for _, key := range fieldKeys {
		field, ok := subject.Fields[key]
		if !ok || field.FieldKey != key || !field.Enabled {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		switch field.Visibility {
		case entitymeta.VisibilityPublic, entitymeta.VisibilityOwner:
		case entitymeta.VisibilityAdmin:
			if !manage {
				return routes.ErrCoreGuardPermissionDenied
			}
		default:
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
	}
	return nil
}

type entityMetaUpsertGuardInput struct {
	Values []struct {
		FieldKey string `json:"fieldKey"`
		Value    any    `json:"value"`
		Clear    bool   `json:"clear"`
	} `json:"values"`
}
