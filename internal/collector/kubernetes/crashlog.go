package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

// Defaults and ceilings for crash-log capture.
const (
	defaultCrashLogTailLines = 200
	maxCrashLogTailLines     = 2000
	defaultCrashLogMaxBytes  = 64 * 1024
	maxCrashLogMaxBytes      = 1024 * 1024
)

// Options carries optional collector behaviour.
type Options struct {
	CrashLogs CrashLogOptions
	K8sEvents K8sEventOptions
	// ErrorLogs enables opt-in error-log capture (nil = off).
	ErrorLogs ErrorLogHost
}

// CrashLogOptions configures crash-time log snapshots. Logs can contain
// sensitive data, so capture is only active when Enabled is set.
type CrashLogOptions struct {
	Enabled   bool
	TailLines int // 0 = 200, max 2000
	MaxBytes  int // 0 = 64 KiB, max 1 MiB
}

func (o CrashLogOptions) normalized() CrashLogOptions {
	if o.TailLines <= 0 {
		o.TailLines = defaultCrashLogTailLines
	}
	if o.TailLines > maxCrashLogTailLines {
		o.TailLines = maxCrashLogTailLines
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = defaultCrashLogMaxBytes
	}
	if o.MaxBytes > maxCrashLogMaxBytes {
		o.MaxBytes = maxCrashLogMaxBytes
	}
	return o
}

// captureCrashLog starts a tracked goroutine that fetches the tail of the
// terminated container instance's logs and emits a CollectorEventCrashLog.
// The OOM and crash events of one termination share a single snapshot: the
// fetch is de-duplicated per (container, finishedAt).
func (c *Collector) captureCrashLog(
	ctx context.Context,
	pod *corev1.Pod,
	cs corev1.ContainerStatus,
	t *corev1.ContainerStateTerminated,
	finishedAt time.Time,
) {
	if !c.crashLogs.Enabled || c.clientset == nil {
		return
	}
	cid := containerID(pod.Namespace, pod.Name, cs.Name)
	key := fmt.Sprintf("%s@%d", cid, finishedAt.UnixNano())
	// Still Terminated (Job pods, restartPolicy Never): the logs are the
	// current instance's. Otherwise the container has restarted and the dead
	// instance's logs are the "previous" ones.
	previous := cs.State.Terminated != t

	c.logMu.Lock()
	if c.stopped {
		c.logMu.Unlock()
		return
	}
	if _, dup := c.logged[key]; dup {
		c.logMu.Unlock()
		return
	}
	if len(c.logged) >= maxTrackedCrashLogs {
		c.logged = make(map[string]struct{})
	}
	c.logged[key] = struct{}{}
	c.wg.Add(1)
	c.logMu.Unlock()

	namespace, podName, container := pod.Namespace, pod.Name, cs.Name
	reason, exitCode := t.Reason, int(t.ExitCode)
	if reason == "" {
		reason = "Error"
	}
	sourceName := c.sourceName

	go func() {
		defer c.wg.Done()

		select {
		case c.logSem <- struct{}{}:
			defer func() { <-c.logSem }()
		case <-ctx.Done():
			return
		}

		fctx, cancel := context.WithTimeout(ctx, crashLogTimeout)
		defer cancel()

		lines, usedPrevious, cut, err := c.fetchLogs(fctx, namespace, podName, container, previous)
		if err != nil {
			c.log.Debug("kubernetes: crash log unavailable",
				zap.String("container", cid), zap.Bool("previous", previous), zap.Error(err))
			return
		}

		lineCount := countLines(lines)
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventCrashLog,
			SourceName: sourceName,
			Container:  &model.Container{ID: cid, SourceName: sourceName},
			CrashLog: &model.CrashLog{
				ContainerID: cid,
				SourceName:  sourceName,
				OccurredAt:  finishedAt,
				Reason:      reason,
				ExitCode:    exitCode,
				Previous:    usedPrevious,
				Lines:       lines,
				LineCount:   lineCount,
				Truncated:   cut || lineCount >= c.crashLogs.TailLines,
				CapturedAt:  time.Now().UTC(),
			},
		})
	}()
}

// fetchLogs reads the log tail. When previous is requested and the API reports
// NotFound/BadRequest (no previous instance / not available), it retries once
// with previous=false. It returns the Previous flag actually used.
// It also reports whether the tail was cut to MaxBytes.
func (c *Collector) fetchLogs(ctx context.Context, namespace, pod, container string, previous bool) (string, bool, bool, error) {
	out, cut, err := c.readLogs(ctx, namespace, pod, container, previous)
	if err != nil && previous && (apierrors.IsNotFound(err) || apierrors.IsBadRequest(err)) {
		previous = false
		out, cut, err = c.readLogs(ctx, namespace, pod, container, false)
	}
	return out, previous, cut, err
}

// crashLogReadCap bounds how much of a log tail is read from the API before
// trimming. TailLines already bounds the response; this guards against
// pathological multi-megabyte lines.
const crashLogReadCap = 8 * 1024 * 1024

// readLogs fetches the last TailLines lines and keeps at most MaxBytes from the
// END of them. PodLogOptions.LimitBytes is deliberately not used: the kubelet
// applies it from the start of the tail, which would drop the newest lines —
// the ones printed right before the crash.
func (c *Collector) readLogs(ctx context.Context, namespace, pod, container string, previous bool) (string, bool, error) {
	tail := int64(c.crashLogs.TailLines)
	stream, err := c.clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
		TailLines: &tail,
	}).Stream(ctx)
	if err != nil {
		return "", false, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, crashLogReadCap))
	if err != nil {
		return "", false, err
	}
	kept := keepTail(data, c.crashLogs.MaxBytes)
	return strings.ToValidUTF8(string(kept), "�"), len(kept) < len(data), nil
}

// keepTail returns the last max bytes of data, starting at a line boundary
// when one exists within the kept part.
func keepTail(data []byte, max int) []byte {
	if max <= 0 || len(data) <= max {
		return data
	}
	tail := data[len(data)-max:]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return tail
}

// countLines returns the number of lines in s (a trailing newline does not
// start another line).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
