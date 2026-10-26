package bootstrap

import (
	"context"
	"log/slog"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func syncExternalExtensionSources(ctx context.Context, logger *slog.Logger, service *extensions.Service) error {
	result, err := service.SyncExternalSources(ctx)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}
	for _, diagnostic := range result.Diagnostics {
		logger.WarnContext(ctx, "external extension source skipped",
			"code", diagnostic.Code,
			"root", diagnostic.Root,
			"packagePath", diagnostic.PackagePath,
			"extensionId", diagnostic.ExtensionID,
			"reason", diagnostic.Message,
		)
	}
	if len(result.Items) > 0 || len(result.Diagnostics) > 0 {
		logger.InfoContext(ctx, "external extension sources synchronized",
			"packages", len(result.Items), "diagnostics", len(result.Diagnostics))
	}
	return nil
}
