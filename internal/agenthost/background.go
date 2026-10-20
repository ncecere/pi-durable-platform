package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
)

// Background-aware hibernation (docs/hibernation.md): the runtime reports its
// live background work at /status (contract version 2), the reaper keeps an
// agent awake while there is some, up to BackgroundMax, and past that has the
// runtime abort it (POST /background/abort) and hibernates the agent.

// backgroundWork is /status's backgroundWork, bounded (decodeBackground).
type backgroundWork struct {
	// Tasks is the units of background work that keep the agent awake.
	Tasks int
	// OldestStartedAt is when the oldest of them started (zero: none).
	OldestStartedAt time.Time
	// WaitingForPerson is the live tasks waiting for a person's answer; they
	// keep nothing awake.
	WaitingForPerson int
}

const (
	// maxBackgroundTasks clamps the counts a runtime reports (the library
	// reports at most 1000: "this many or more").
	maxBackgroundTasks = 1000
	// A start time outside these bounds (Unix ms: 2000 and 3000) is not one.
	minStartedAtMs = 946684800000
	maxStartedAtMs = 32503680000000
)

// decodeBackground reads backgroundWork, or returns nil when it is absent
// (a runtime before contract version 2) or malformed: counts that aren't
// whole numbers from 0, or a start time missing (with tasks) or implausible.
// The runtime is not trusted; a report the platform won't read is judged as
// a runtime that reports none, never as an error that would keep the reaper
// from hibernating the agent.
func decodeBackground(raw json.RawMessage) *backgroundWork {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var wire struct {
		Tasks            *int64 `json:"tasks"`
		OldestStartedAt  *int64 `json:"oldestStartedAt"`
		WaitingForPerson *int64 `json:"waitingForPerson"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Tasks == nil || *wire.Tasks < 0 {
		return nil
	}
	bg := &backgroundWork{Tasks: int(min(*wire.Tasks, maxBackgroundTasks))}
	if wire.WaitingForPerson != nil {
		if *wire.WaitingForPerson < 0 {
			return nil
		}
		bg.WaitingForPerson = int(min(*wire.WaitingForPerson, maxBackgroundTasks))
	}
	if bg.Tasks > 0 {
		at := wire.OldestStartedAt
		if at == nil || *at < minStartedAtMs || *at > maxStartedAtMs {
			return nil
		}
		bg.OldestStartedAt = time.UnixMilli(*at)
	}
	return bg
}

// awakeReasons says why an agent isn't hibernating (a set).
type awakeReasons uint8

const (
	awakeReply      awakeReasons = 1 << iota // a thread is replying
	awakeStream                              // an event stream is open
	awakeBackground                          // background work under the ceiling
	awakeRecent                              // activity within the idle timeout
)

// names are the reasons' names, as metrics.AwakeReasons and the API spell them.
func (r awakeReasons) names() []string {
	out := []string{}
	for i, name := range []string{"reply", "stream", "background", "recent"} {
		if r&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}

// judge applies the reaper's rule to a runtime's report: why the agent is
// awake (none: it may hibernate), and whether background work past the
// ceiling is all that is left of it. localStreams stands in for a runtime
// that doesn't count its streams (before openStreams).
func (m *Manager) judge(st runtimeStatus, localStreams int, now time.Time) (reasons awakeReasons, overCeiling bool) {
	if st.BusyThreads > 0 {
		reasons |= awakeReply
	}
	if (st.OpenStreams != nil && *st.OpenStreams > 0) || (st.OpenStreams == nil && localStreams > 0) {
		reasons |= awakeStream
	}
	if bg := st.Background; bg != nil && bg.Tasks > 0 {
		if m.BackgroundMax > 0 && now.Sub(bg.OldestStartedAt) >= m.BackgroundMax {
			overCeiling = true
		} else {
			reasons |= awakeBackground
		}
	}
	if now.Sub(time.UnixMilli(st.LastActivityAt)) < m.idle {
		reasons |= awakeRecent
	}
	return reasons, overCeiling
}

// recheckIn is how long the reaper may leave an agent it found awake before
// asking its runtime again: until it could be idle (its last activity plus
// the timeout); while it works or is watched, half a timeout, since it counts
// as active until it stops and then the timeout starts; and with background
// work, not past the moment its oldest reaches the ceiling.
func (m *Manager) recheckIn(st runtimeStatus, reasons awakeReasons, now time.Time) time.Duration {
	wait := m.idle - now.Sub(time.UnixMilli(st.LastActivityAt))
	if reasons&(awakeReply|awakeStream|awakeBackground) != 0 {
		wait = m.idle / 2
	}
	if reasons&awakeBackground != 0 && m.BackgroundMax > 0 {
		wait = min(wait, m.BackgroundMax-now.Sub(st.Background.OldestStartedAt))
	}
	return max(wait, 0)
}

// BackgroundAbort is one ceiling abort: the runtime aborted Aborted units of
// background work that started at least Ceiling ago (the oldest at
// OldestStartedAt), and Settled says whether they ended within its wait.
type BackgroundAbort struct {
	Aborted         int
	Settled         bool
	Ceiling         time.Duration
	OldestStartedAt time.Time
}

// maxAbortBytes bounds what the reaper reads from POST /background/abort.
const maxAbortBytes = 4 << 10

// abortBackground has the runtime abort its background work older than the
// ceiling (POST /background/abort), counts it, and reports work it aborted to
// BackgroundAborted (the audit log).
func (m *Manager) abortBackground(ctx context.Context, agentID string, ep launcher.Endpoint, bg *backgroundWork) {
	ctx, cancel := context.WithTimeout(ctx, abortTimeout)
	defer cancel()
	abort, err := m.requestAbort(ctx, ep)
	m.Metrics.BackgroundAbort(err)
	if err != nil {
		m.log.Warn("abort background work past the ceiling", "agent", agentID, "err", err)
		return
	}
	m.log.Info("aborted background work past the ceiling", "agent", agentID, "aborted", abort.Aborted, "settled", abort.Settled,
		"ceiling", m.BackgroundMax.String())
	if abort.Aborted == 0 || m.BackgroundAborted == nil {
		return
	}
	abort.OldestStartedAt = bg.OldestStartedAt
	if err := m.BackgroundAborted(ctx, agentID, abort); err != nil {
		m.log.Warn("audit a background abort", "agent", agentID, "err", err)
	}
}

func (m *Manager) requestAbort(ctx context.Context, ep launcher.Endpoint) (BackgroundAbort, error) {
	body, _ := json.Marshal(map[string]int64{"olderThanMs": m.BackgroundMax.Milliseconds()})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL+"/background/abort", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	// Its own client: the runtime may wait 5 seconds for the work to end.
	client := &http.Client{Timeout: abortTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return BackgroundAbort{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BackgroundAbort{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAbortBytes+1))
	if err != nil {
		return BackgroundAbort{}, err
	}
	if len(raw) > maxAbortBytes {
		return BackgroundAbort{}, errStatusTooLarge
	}
	var reply struct {
		Data struct {
			Aborted int  `json:"aborted"`
			Settled bool `json:"settled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return BackgroundAbort{}, err
	}
	return BackgroundAbort{Aborted: min(max(reply.Data.Aborted, 0), maxBackgroundTasks), Settled: reply.Data.Settled, Ceiling: m.BackgroundMax}, nil
}

// setAwake records why the reaper left an agent awake (for agents_awake).
func (m *Manager) setAwake(agentID string, reasons awakeReasons) {
	m.mu.Lock()
	m.awake[agentID] = reasons
	m.mu.Unlock()
}

// countAwake sets agents_awake from the reaper's last view of the running agents.
func (m *Manager) countAwake(running map[string]launcher.Endpoint) {
	m.mu.Lock()
	counts := map[string]int{}
	for id, reasons := range m.awake {
		if _, ok := running[id]; !ok {
			continue
		}
		for _, name := range reasons.names() {
			counts[name]++
		}
	}
	m.mu.Unlock()
	m.Metrics.SetAgentsAwake(counts)
}

// AwakeReport says why a running agent isn't hibernating, from its runtime's
// own report (the API's agent `awake`).
type AwakeReport struct {
	// Reasons: reply, stream, background, recent (none: it hibernates at the
	// reaper's next pass).
	Reasons []string
	// BackgroundTasks and BackgroundSince are its background work that keeps
	// it awake: how many units, since when (nil: none, or a runtime before
	// contract version 2).
	BackgroundTasks int
	BackgroundSince *time.Time
	// WaitingForPerson is its tasks waiting for a person's answer.
	WaitingForPerson int
	// OverCeiling: background work past BackgroundMax, aborted at the next pass.
	OverCeiling bool
}

type awakeCache struct {
	at     time.Time
	report *AwakeReport
}

const (
	// awakeCacheFor is how long an Awake answer is reused (pages poll).
	awakeCacheFor = 5 * time.Second
	// awakeTimeout bounds Awake's question to the runtime.
	awakeTimeout = 2 * time.Second
)

// Awake asks a running agent's runtime why it isn't hibernating, without
// waking it: nil when the agent isn't running or its runtime didn't answer in
// time. Answers are reused for a few seconds.
func (m *Manager) Awake(ctx context.Context, agentID string) *AwakeReport {
	ep, ok := m.launcher.Running()[agentID]
	if !ok {
		return nil
	}
	now := time.Now()
	m.mu.Lock()
	cached, hit := m.awakeSeen[agentID]
	streams := m.streams[agentID]
	m.mu.Unlock()
	if hit && now.Sub(cached.at) < awakeCacheFor {
		return cached.report
	}
	ctx, cancel := context.WithTimeout(ctx, awakeTimeout)
	defer cancel()
	if ep.Token == "" && m.Resolve != nil {
		spec, err := m.Resolve(ctx, agentID)
		if err != nil {
			return nil
		}
		ep.Token = spec.Token
	}
	var report *AwakeReport
	if st, err := m.status(ctx, ep); err == nil {
		reasons, over := m.judge(st, streams, now)
		report = &AwakeReport{Reasons: reasons.names(), OverCeiling: over}
		if bg := st.Background; bg != nil {
			report.WaitingForPerson = bg.WaitingForPerson
			if bg.Tasks > 0 {
				since := bg.OldestStartedAt
				report.BackgroundTasks, report.BackgroundSince = bg.Tasks, &since
			}
		}
	}
	m.mu.Lock()
	m.awakeSeen[agentID] = awakeCache{at: now, report: report}
	m.mu.Unlock()
	return report
}
