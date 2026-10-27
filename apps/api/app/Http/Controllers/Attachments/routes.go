package attachmentscontroller

import "github.com/gofiber/fiber/v3"

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Post("/attachments", h.upload)
	api.Get("/attachments/upload-policy", h.currentUploadPolicy)
	api.Get("/attachments/:publicId", h.get)
	api.Get("/attachments/:publicId/content", h.content)
	api.Get("/attachments/:publicId/variants/:variant/content", h.variantContent)

	api.Get("/admin/attachment-settings", h.settings)
	api.Put("/admin/attachment-settings", h.updateSettings)
	api.Post("/admin/attachment-settings/test", h.testSettings)
	api.Get("/admin/attachment-upload-policies/roles", h.listRoleUploadPolicies)
	api.Put("/admin/attachment-upload-policies/roles/:roleKey", h.setRoleUploadPolicy)
	api.Delete("/admin/attachment-upload-policies/roles/:roleKey", h.deleteRoleUploadPolicy)
	api.Get("/admin/attachment-upload-policies/users/:userID", h.getUserUploadPolicy)
	api.Put("/admin/attachment-upload-policies/users/:userID", h.setUserUploadPolicy)
	api.Delete("/admin/attachment-upload-policies/users/:userID", h.deleteUserUploadPolicy)
	api.Get("/admin/attachment-compression-settings", h.compressionSettings)
	api.Put("/admin/attachment-compression-settings", h.updateCompressionSettings)
	api.Get("/admin/attachment-storage-instances", h.listStorageInstances)
	api.Post("/admin/attachment-storage-instances", h.createStorageInstance)
	api.Post("/admin/attachment-storage-instances/probe", h.probeStorageInstance)
	api.Post("/admin/attachment-storage-instances/local/activate", h.activateLocalStorage)
	api.Put("/admin/attachment-storage-instances/:id", h.updateStorageInstance)
	api.Post("/admin/attachment-storage-instances/:id/activate", h.activateStorageInstance)
	api.Delete("/admin/attachment-storage-instances/:id", h.deleteStorageInstance)
	api.Get("/admin/attachments", h.listAdmin)
	api.Get("/admin/attachments/compression-stats", h.compressionStats)
	api.Post("/admin/attachments/compression/backfill", h.backfillCompression)
	api.Get("/admin/attachments/:id", h.detailAdmin)
	api.Patch("/admin/attachments/:id", h.updateAdmin)
	api.Delete("/admin/attachments/:id", h.deleteAdmin)
	api.Post("/admin/attachments/cleanup", h.cleanupAdmin)
	api.Post("/admin/attachments/reconcile", h.reconcileAdmin)
}
