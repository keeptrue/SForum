package attachments

import (
	"context"
	"errors"
	"testing"

	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
)

func TestUploadPolicyManagementFailsClosedWithoutPolicyService(t *testing.T) {
	service := NewService(nil, newAttachmentOptions(nil))
	actor := uploadActor()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "set role", run: func() error {
			_, err := service.SetRoleUploadPolicy(context.Background(), actor, "member", uploadpolicy.LimitInput{MaxFileSizeMB: 5})
			return err
		}},
		{name: "delete role", run: func() error {
			_, err := service.DeleteRoleUploadPolicy(context.Background(), actor, "member")
			return err
		}},
		{name: "set user", run: func() error {
			_, err := service.SetUserUploadPolicy(context.Background(), actor, 7, uploadpolicy.LimitInput{MaxFileSizeMB: 5})
			return err
		}},
		{name: "delete user", run: func() error {
			_, err := service.DeleteUserUploadPolicy(context.Background(), actor, 7)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, uploadpolicy.ErrInvalidPolicy) {
				t.Fatalf("expected fail-closed policy error, got %v", err)
			}
		})
	}
}
