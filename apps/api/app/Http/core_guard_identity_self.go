package http

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const maxGuardSessionIDBytes = 256

func identitySelfCredentialsGuardEvaluator(
	sessions IdentitySessionGuardPolicy,
	tokens IdentityAPITokenGuardPolicy,
) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if err := requireIdentitySelfCredentialsAuthority(ctx, evaluation); !errors.Is(err, routes.ErrCoreGuardEvaluatorUnavailable) {
			return err
		}
		if evaluation.Request.Query != "" || len(bytes.TrimSpace(evaluation.Request.Body)) != 0 {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		switch evaluation.Descriptor.RouteID {
		case "core.route.identity.revoke_session":
			if err := requireAuthenticatedCoreGuardActor(ctx, evaluation); err != nil {
				return err
			}
			if sessions == nil {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			sid := strings.TrimSpace(evaluation.Request.Params["sessionId"])
			if sid == "" || len(sid) > maxGuardSessionIDBytes || strings.ContainsRune(sid, '\x00') {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			subject, err := sessions.LoadSessionGuardSubject(ctx, sid)
			if err != nil || !subject.Exists || subject.SID != sid || subject.OwnerUserID != evaluation.Request.ActorID {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			return nil
		case "core.route.identity.revoke_apitoken", "core.route.identity.rotate_apitoken":
			if tokens == nil {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			if err := requireCookieCredentialAuthority(ctx, evaluation); err != nil {
				return err
			}
			tokenID, err := strconv.ParseInt(evaluation.Request.Params["tokenID"], 10, 64)
			if err != nil || tokenID <= 0 {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			subject, err := tokens.LoadGuardSubject(ctx, tokenID)
			if err != nil || !subject.Exists || subject.TokenID != tokenID || subject.OwnerUserID != evaluation.Request.ActorID {
				return routes.ErrCoreGuardEvaluatorUnavailable
			}
			return nil
		default:
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
	}
}
