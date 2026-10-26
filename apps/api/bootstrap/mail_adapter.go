package bootstrap

import (
	"context"
	"encoding/json"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

type passwordResetOutbox struct{ outbox *notifications.Outbox }
type emailVerificationOutbox struct{ outbox *notifications.Outbox }

func (a passwordResetOutbox) QueuePasswordReset(ctx context.Context, token identity.CreatePasswordResetTokenInput, message identity.PasswordResetMail) error {
	data := message.Brand.TemplateData()
	data["locale"] = message.Locale
	data["username"] = message.Username
	data["resetUrl"] = message.ResetURL
	data["expiresAt"] = message.ExpiresAt.UTC().Format(time.RFC3339)
	if message.SiteName != "" {
		data["siteName"] = message.SiteName
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = a.outbox.QueuePasswordReset(ctx, notifications.QueuePasswordResetInput{
		UserID: token.UserID, TokenHash: token.TokenHash, ExpiresAt: token.ExpiresAt, RequestIPHash: token.RequestIPHash,
		Mail: notifications.QueueMailInput{Recipient: message.Recipient, TemplateKey: "identity.password_reset", TemplateData: encoded, IdempotencyKey: message.IdempotencyKey},
	})
	return err
}

func (a emailVerificationOutbox) QueueEmailVerification(ctx context.Context, token identity.CreateEmailVerificationTokenInput, message identity.EmailVerificationMail) error {
	data := message.Brand.TemplateData()
	data["locale"] = message.Locale
	data["username"] = message.Username
	data["verifyUrl"] = message.VerifyURL
	data["expiresAt"] = message.ExpiresAt.UTC().Format(time.RFC3339)
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = a.outbox.QueueEmailVerification(ctx, notifications.QueueEmailVerificationInput{
		UserID: token.UserID, Email: token.Email, TokenHash: token.TokenHash,
		ExpiresAt: token.ExpiresAt, RequestIPHash: token.RequestIPHash,
		Mail: notifications.QueueMailInput{
			Recipient: message.Recipient, TemplateKey: "identity.email_verification",
			TemplateData: encoded, IdempotencyKey: message.IdempotencyKey,
		},
	})
	return err
}
