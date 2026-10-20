package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// agentSchedule is an agent-made schedule as the runtime's schedule tool
// reads it (the gateway's, not the public API's: docs/runtime-contract.md).
type agentSchedule struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Once           bool       `json:"once"`
	Cron           string     `json:"cron,omitempty"`
	Timezone       string     `json:"timezone"`
	Message        string     `json:"message"`
	ThreadID       *string    `json:"threadId"`
	OriginThreadID *string    `json:"originThreadId"`
	NextFireAt     *time.Time `json:"nextFireAt"`
	Paused         bool       `json:"paused"`
	PausedReason   *string    `json:"pausedReason,omitempty"`
}

func toAgentSchedule(v triggers.View) agentSchedule {
	t := v.Trigger
	out := agentSchedule{ID: t.ID, Name: t.Name, Once: t.Once, Message: t.MessageTemplate, ThreadID: t.ThreadID,
		OriginThreadID: t.OriginThreadID, Paused: t.Paused, PausedReason: t.PausedReason}
	if t.Timezone != nil {
		out.Timezone = *t.Timezone
	}
	if t.Cron != nil && !t.Once {
		out.Cron = *t.Cron
	}
	if t.NextFireAt != nil && !t.Paused {
		at := t.NextFireAt.UTC()
		out.NextFireAt = &at
	}
	return out
}

// registerSchedules serves the runtime's schedule tool (docs/triggers.md,
// agent-made schedules): the agent's own credential (never a person's) names
// the agent, which may make, list and cancel its own schedules only.
func registerSchedules(mux *http.ServeMux, ag *agents.Service, trig *triggers.Service, log *slog.Logger) {
	authed := func(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
		id, token, ok := llm.Credential(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "A gateway credential is required")
			return uuid.Nil, false
		}
		if _, _, err := ag.AuthenticateProxy(r.Context(), id, token); err != nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid gateway credential")
			return uuid.Nil, false
		}
		return id, true
	}
	mux.HandleFunc("GET /schedules", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		rows, err := trig.AgentList(r.Context(), id)
		writeList(w, r, rows, err, toAgentSchedule)
	})
	mux.HandleFunc("POST /schedules", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in struct {
			Name     string `json:"name"`
			At       string `json:"at"`
			Cron     string `json:"cron"`
			Timezone string `json:"timezone"`
			Message  string `json:"message"`
			ThreadID string `json:"threadId"`
			Thread   string `json:"thread"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be a schedule")
			return
		}
		out, err := trig.AgentCreate(r.Context(), id, triggers.AgentScheduleInput{Name: in.Name, At: in.At, Cron: in.Cron,
			Timezone: in.Timezone, Message: in.Message, ThreadID: in.ThreadID, Thread: in.Thread})
		if err != nil {
			if _, known := apperr.As(err); !known {
				log.Warn("an agent's schedule", "agent", id, "err", err)
			}
			failed(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, struct {
			Schedule   agentSchedule `json:"schedule"`
			Where      string        `json:"where"`
			WhyNotHere string        `json:"whyNotHere,omitempty"`
		}{toAgentSchedule(out.View), out.Where, out.WhyNotHere})
	})
	mux.HandleFunc("DELETE /schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		sid, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "trigger_not_found", "Trigger not found")
			return
		}
		v, err := trig.AgentCancel(r.Context(), id, sid)
		if failed(w, r, err) {
			return
		}
		httpx.JSON(w, http.StatusOK, toAgentSchedule(v))
	})
}
