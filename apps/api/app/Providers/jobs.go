package providers

import (
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	jobscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Jobs"
	attachmentjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Attachments"
	auditjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Audit"
	identityjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Identity"
	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	jobs "github.com/keeptrue/sforum/apps/api/app/Models/Jobs"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type JobsProvider struct{ controller *jobscontroller.Controller }

func NewJobsProvider(pool *pgxpool.Pool, client *supportjobs.Client, users identity.ActorStore, sessions *authsession.Manager) *JobsProvider {
	service := jobs.NewService(pool, client).WithScheduleOptions(
		jobs.NewPostgresOptionStore(pool),
		coreScheduleConstructors(),
	)
	return &JobsProvider{controller: jobscontroller.NewController(service, users, sessions)}
}

func (p *JobsProvider) RegisterRoutes(api fiber.Router) { p.controller.RegisterRoutes(api) }

// coreScheduleConstructors 与 worker bootstrap 保持同源，供手动 trigger 入队。
func coreScheduleConstructors() map[string]jobs.ScheduleConstructor {
	return map[string]jobs.ScheduleConstructor{
		supportjobs.ScheduleIdentityCleanupSessions: func() (river.JobArgs, *river.InsertOpts) {
			return identityjobs.CleanupSessionsArgs{}, nil
		},
		supportjobs.ScheduleAttachmentsCleanupOrphans: func() (river.JobArgs, *river.InsertOpts) {
			return attachmentjobs.CleanupOrphansArgs{}, nil
		},
		supportjobs.ScheduleAuditCleanupEvents: func() (river.JobArgs, *river.InsertOpts) {
			return auditjobs.CleanupEventsArgs{}, nil
		},
		supportjobs.ScheduleSearchReconcile: func() (river.JobArgs, *river.InsertOpts) {
			opts := searchjobs.ReconcileArgs{}.EnqueueOptions().RiverInsertOpts()
			return searchjobs.ReconcileArgs{}, opts
		},
	}
}
