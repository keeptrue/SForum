package options

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// EmailVerificationPolicyResolver keeps email-verification option lookup out
// of the legacy aggregate Service receiver set.
type EmailVerificationPolicyResolver struct {
	service *Service
}

func NewEmailVerificationPolicyResolver(service *Service) EmailVerificationPolicyResolver {
	return EmailVerificationPolicyResolver{service: service}
}

func (r EmailVerificationPolicyResolver) EmailVerificationPolicy(ctx context.Context) (identity.EmailVerificationPolicy, error) {
	if r.service == nil {
		return identity.EmailVerificationPolicy{}, nil
	}
	values, err := r.service.loadMap(ctx)
	if err != nil {
		return identity.EmailVerificationPolicy{}, err
	}
	return identity.EmailVerificationPolicy{
		Required:     isEnabledOption(values[NameIdentityRegistrationRequireEmailVerification]),
		BlockContent: isEnabledOption(values[NameIdentityRegistrationBlockPostingUntilVerified]),
	}, nil
}
