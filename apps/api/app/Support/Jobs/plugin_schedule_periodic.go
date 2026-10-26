package jobs

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/robfig/cron/v3"
	timezone "github.com/keeptrue/sforum/apps/api/app/Support/Timezone"
)

const PluginScheduleTriggerKind = "extension.plugin_schedule_trigger"

// PluginScheduleTriggerArgs is Host-owned work. The periodic leader inserts
// only this exact-runtime marker; its worker acquires schedule admission before
// inserting the declared plugin job.
type PluginScheduleTriggerArgs struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	ArtifactDigest   string `json:"artifactDigest"`
	InstanceID       string `json:"instanceId"`
	ScheduleID       string `json:"scheduleId"`
}

func (PluginScheduleTriggerArgs) Kind() string { return PluginScheduleTriggerKind }

func (a PluginScheduleTriggerArgs) Identity() PluginScheduleRuntimeIdentity {
	return PluginScheduleRuntimeIdentity{
		ExtensionID: a.ExtensionID, ExtensionVersion: a.ExtensionVersion,
		ArtifactDigest: a.ArtifactDigest, InstanceID: a.InstanceID,
	}
}

func (PluginScheduleTriggerArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 3}
}

type PluginSchedulePeriodicBundle interface {
	AddSafely(*river.PeriodicJob) (rivertype.PeriodicJobHandle, error)
	RemoveByID(string) bool
}

// PluginSchedulePeriodicPublisher owns this process's dynamic River catalog.
// River elects one leader, but every worker publishes the same exact snapshot
// so a leadership change cannot restore removed schedules.
type PluginSchedulePeriodicPublisher struct {
	mu        sync.Mutex
	bundle    PluginSchedulePeriodicBundle
	published map[PluginScheduleRuntimeIdentity][]string
}

func NewPluginSchedulePeriodicPublisher(bundle PluginSchedulePeriodicBundle) *PluginSchedulePeriodicPublisher {
	return &PluginSchedulePeriodicPublisher{bundle: bundle, published: make(map[PluginScheduleRuntimeIdentity][]string)}
}

func (p *PluginSchedulePeriodicPublisher) Replace(previous *PluginScheduleRuntime, next PluginScheduleRuntime) error {
	if p == nil || p.bundle == nil {
		return fmt.Errorf("%w: River periodic bundle is unavailable", ErrPluginScheduleInvalid)
	}
	jobs, ids, err := buildPluginPeriodicJobs(next)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var previousJobs []*river.PeriodicJob
	var previousIDs []string
	if previous != nil {
		previousJobs, previousIDs, err = buildPluginPeriodicJobs(*previous)
		if err != nil {
			return err
		}
		p.removeIDsLocked(previousIDs)
	}
	added := make([]string, 0, len(jobs))
	for index, job := range jobs {
		if _, err := p.bundle.AddSafely(job); err != nil {
			p.removeIDsLocked(added)
			for _, rollback := range previousJobs {
				_, _ = p.bundle.AddSafely(rollback)
			}
			return fmt.Errorf("%w: publish schedule %q: %v", ErrPluginScheduleInvalid, ids[index], err)
		}
		added = append(added, ids[index])
	}
	if previous != nil {
		delete(p.published, previous.Identity)
	}
	p.published[next.Identity] = ids
	return nil
}

func (p *PluginSchedulePeriodicPublisher) Remove(runtime PluginScheduleRuntime) {
	if p == nil || p.bundle == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := p.published[runtime.Identity]
	if len(ids) == 0 {
		_, ids, _ = buildPluginPeriodicJobs(runtime)
	}
	p.removeIDsLocked(ids)
	delete(p.published, runtime.Identity)
}

func (p *PluginSchedulePeriodicPublisher) removeIDsLocked(ids []string) {
	for _, id := range ids {
		p.bundle.RemoveByID(id)
	}
}

func buildPluginPeriodicJobs(runtime PluginScheduleRuntime) ([]*river.PeriodicJob, []string, error) {
	identity := normalizePluginScheduleIdentity(runtime.Identity)
	if !identity.valid() || len(runtime.Schedules) == 0 {
		return nil, nil, ErrPluginScheduleInvalid
	}
	jobs := make([]*river.PeriodicJob, 0, len(runtime.Schedules))
	ids := make([]string, 0, len(runtime.Schedules))
	seen := make(map[string]struct{}, len(runtime.Schedules))
	for _, declaration := range runtime.Schedules {
		declaration.ScheduleID = strings.TrimSpace(declaration.ScheduleID)
		declaration.Contract = declaration.Contract.Normalized()
		if declaration.ScheduleID == "" || !declaration.Contract.Valid() ||
			declaration.Contract.ExtensionID != identity.ExtensionID ||
			declaration.Contract.ExtensionVersion != identity.ExtensionVersion ||
			declaration.Contract.ArtifactDigest != identity.ArtifactDigest ||
			strings.TrimSpace(declaration.TrustGrantID) == "" {
			return nil, nil, fmt.Errorf("%w: incomplete exact schedule %q", ErrPluginScheduleInvalid, declaration.ScheduleID)
		}
		periodicID := "plugin_schedule." + declaration.ScheduleID
		if _, duplicate := seen[periodicID]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate schedule %q", ErrPluginScheduleInvalid, declaration.ScheduleID)
		}
		schedule, err := pluginPeriodicSchedule(declaration.Cron, declaration.Timezone)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: schedule %q: %v", ErrPluginScheduleInvalid, declaration.ScheduleID, err)
		}
		args := PluginScheduleTriggerArgs{
			ExtensionID: identity.ExtensionID, ExtensionVersion: identity.ExtensionVersion,
			ArtifactDigest: identity.ArtifactDigest, InstanceID: identity.InstanceID,
			ScheduleID: declaration.ScheduleID,
		}
		jobs = append(jobs, river.NewPeriodicJob(schedule, func() (river.JobArgs, *river.InsertOpts) {
			opts := args.InsertOpts()
			return args, &opts
		}, &river.PeriodicJobOpts{ID: periodicID}))
		ids = append(ids, periodicID)
		seen[periodicID] = struct{}{}
	}
	return jobs, ids, nil
}

func pluginPeriodicSchedule(expression, zone string) (river.PeriodicSchedule, error) {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "@every ") {
		interval, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expression, "@every ")))
		if err != nil || interval < time.Second {
			return nil, fmt.Errorf("invalid interval")
		}
		return river.PeriodicInterval(interval), nil
	}
	location, err := timezone.Load(zone)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(expression)
	var parser cron.Parser
	switch len(fields) {
	case 5:
		parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	case 6:
		parser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	default:
		return nil, fmt.Errorf("cron must contain five or six fields")
	}
	parsed, err := parser.Parse(expression)
	if err != nil {
		return nil, err
	}
	return locatedPeriodicSchedule{Schedule: parsed, Location: location}, nil
}

type locatedPeriodicSchedule struct {
	cron.Schedule
	Location *time.Location
}

func (s locatedPeriodicSchedule) Next(current time.Time) time.Time {
	return s.Schedule.Next(current.In(s.Location))
}
