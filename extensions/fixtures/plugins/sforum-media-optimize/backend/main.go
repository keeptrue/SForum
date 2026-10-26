package main

import (
	"context"
	"encoding/base64"
	"errors"
	"image/color"
	"log"
	"strings"
	"sync"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// P13 media-optimize reference: 真实图片处理 + 可控 scan Provider +
// River job 重试/去重/original fallback/retention。

const (
	jobID      = "sforum.media-optimize.job.variants"
	jobKind    = "sforum.media-optimize.variants"
	jobHandler = "sforum.media-optimize.job.variants"
	jobPayload = "sforum.media-optimize.job.variants.payload@1"

	retentionJobID      = "sforum.media-optimize.job.retention"
	retentionJobKind    = "sforum.media-optimize.retention"
	retentionJobHandler = "sforum.media-optimize.job.retention"
	retentionJobPayload = "sforum.media-optimize.job.retention.payload@1"
)

// 进程内去重与 retention 证据（job 真实执行时写入）。
var (
	jobMu       sync.Mutex
	seenDigests = map[string]string{} // sourceDigest -> variantDigest
	retained    = map[string]bool{}
	lastScan    = "allow"
)

func main() {
	jobs, err := pluginv2.NewJobRegistry(
		pluginv2.JobDefinition{
			ID: jobID, ContractVersion: jobID + "@1", Name: jobKind,
			Handler: jobHandler, PayloadSchema: jobPayload,
			RetryPolicy: "bounded", MaxAttempts: 3, RetryDelaySeconds: 5, ConcurrencyLimit: 2,
			Execute: runOptimizeJob,
		},
		pluginv2.JobDefinition{
			ID: retentionJobID, ContractVersion: retentionJobID + "@1", Name: retentionJobKind,
			Handler: retentionJobHandler, PayloadSchema: retentionJobPayload,
			RetryPolicy: "bounded", MaxAttempts: 2, RetryDelaySeconds: 30, ConcurrencyLimit: 1,
			Execute: runRetentionJob,
		},
	)
	if err != nil {
		log.Fatalf("configure media-optimize jobs: %v", err)
	}
	pluginv2.Serve(pluginv2.NewServer().WithJobRegistry(jobs))
}

func runRetentionJob(ctx context.Context, call *pluginv2.JobCall) error {
	if call == nil || call.Progress == nil {
		return errors.New("missing job progress stream")
	}
	if err := call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 1, TotalUnits: 2, Checkpoint: "retention-scan",
	}); err != nil {
		return err
	}
	values := pluginv2.TypedDocumentValues(call.Payload)
	// uninstall/retention：清理变体证据，但 original 标记保留。
	jobMu.Lock()
	for key := range seenDigests {
		retained[key] = true
		delete(seenDigests, key)
	}
	if source, _ := values["sourceDigest"].(string); source != "" {
		retained[source] = true
	}
	jobMu.Unlock()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	return call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		CompletedUnits: 2, TotalUnits: 2, Checkpoint: "retention-done",
	})
}

func runOptimizeJob(ctx context.Context, call *pluginv2.JobCall) error {
	if call == nil || call.Progress == nil {
		return errors.New("missing job progress stream")
	}
	values := pluginv2.TypedDocumentValues(call.Payload)
	source, _ := values["sourceDigest"].(string)
	scanMode, _ := values["scanMode"].(string)
	if scanMode == "" {
		scanMode = lastScan
	}
	declaredMIME, _ := values["declaredMime"].(string)
	imageB64, _ := values["imageBase64"].(string)
	mode, _ := values["mode"].(string)

	// 测试触发：payload 标记 fail/timeout 时失败，Host 应保留 original。
	switch strings.TrimSpace(source) {
	case "reference:fail":
		return errors.New("reference media optimize failure")
	case "reference:timeout":
		<-ctx.Done()
		return context.Cause(ctx)
	}

	if err := call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 1, TotalUnits: 4, Checkpoint: "scan",
	}); err != nil {
		return err
	}

	payload, err := decodePayload(imageB64, source)
	if err != nil {
		return err
	}

	// 可控开发扫描 Provider：必须实际执行，不得只生成计划。
	scanner := controllableScanProvider{Mode: scanMode}
	if err := scanner.Scan(declaredMIME, payload); err != nil {
		// MIME 欺骗 / 扫描拒绝 → 失败；Host fallback_original。
		return err
	}
	lastScan = scanMode

	// 去重：同一 sourceDigest 不重复生成变体。
	jobMu.Lock()
	if prev, ok := seenDigests[source]; ok && source != "" && mode != "force" {
		jobMu.Unlock()
		_ = call.Progress.Send(&protocolwire.ProgressUpdate{
			StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
			CompletedUnits: 3, TotalUnits: 4, Checkpoint: "deduped:" + prev[:8],
		})
		return call.Progress.Send(&protocolwire.ProgressUpdate{
			StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
			CompletedUnits: 4, TotalUnits: 4, Checkpoint: "done-dedupe",
		})
	}
	jobMu.Unlock()

	if err := call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 2, TotalUnits: 4, Checkpoint: "metadata",
	}); err != nil {
		return err
	}

	timeout := 2 * time.Second
	if v, ok := values["timeoutMs"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Millisecond
	}
	maxDim := 4096
	if v, ok := values["maxDimension"].(float64); ok && v > 0 {
		maxDim = int(v)
	}

	// 攻击面：超大尺寸 / 损坏图 / 超时。
	variant, meta, err := processImage(payload, processOptions{
		MaxWidth: maxDim, MaxHeight: maxDim, Timeout: timeout, WantWebP: true, VariantName: "thumb",
	})
	if err != nil {
		// original fallback 证据：记录但返回错误让 Host 走 FailureFallbackOriginal。
		fb, _ := originalFallback(payload, err.Error())
		jobMu.Lock()
		if source != "" {
			retained[source] = true
			seenDigests[source] = fb.Digest
		}
		jobMu.Unlock()
		_ = meta
		return err
	}

	if err := call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_RUNNING,
		CompletedUnits: 3, TotalUnits: 4, Checkpoint: "variants:" + variant.Digest[:12],
	}); err != nil {
		return err
	}

	jobMu.Lock()
	if source != "" {
		seenDigests[source] = variant.Digest
	}
	jobMu.Unlock()

	_ = meta
	return call.Progress.Send(&protocolwire.ProgressUpdate{
		StepId: call.JobID, State: protocolwire.ProgressState_PROGRESS_STATE_SUCCEEDED,
		CompletedUnits: 4, TotalUnits: 4, Checkpoint: "done",
	})
}

func decodePayload(imageB64, source string) ([]byte, error) {
	if strings.TrimSpace(imageB64) != "" {
		raw, err := base64.StdEncoding.DecodeString(imageB64)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	// 无 payload 时生成真实样例图，保证 job 不空跑。
	switch {
	case strings.HasPrefix(source, "reference:jpeg"):
		return sampleJPEG(64, 48)
	case strings.HasPrefix(source, "reference:huge"):
		// 真实大图：512x512；测试用 maxDimension 触发超限。
		return samplePNG(512, 512, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	case strings.HasPrefix(source, "reference:corrupt"):
		return []byte("not-an-image"), nil
	default:
		return samplePNG(64, 48, color.RGBA{R: 30, G: 144, B: 255, A: 255})
	}
}
