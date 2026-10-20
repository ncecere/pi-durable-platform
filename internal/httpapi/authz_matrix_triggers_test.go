package httpapi_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// The authorization matrix's trigger fixtures.

// newTrigger adds a webhook trigger to the team's agent, as its owner.
func (e *matrixEnv) newTrigger(tf teamFix) triggers.Saved {
	e.t.Helper()
	owner := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	name := fmt.Sprintf("trigger-%d", e.next())
	saved, err := e.app.Triggers.Create(context.Background(), owner, uuid.MustParse(tf.agent), triggers.Input{Kind: triggers.KindWebhook,
		Name: &name, MessageTemplate: &name})
	if err != nil {
		e.t.Fatal(err)
	}
	return saved
}

// trigger is the team agent's fixture trigger, or a fresh one for a call
// expected to succeed that changes it.
func (c *mctx) trigger(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newTrigger(*c.tf).Trigger.ID.String()
	}
	if c.tf.trigger == "" {
		saved := c.e.newTrigger(*c.tf)
		c.tf.trigger = saved.Trigger.ID.String()
		c.tf.hook = saved.WebhookURL[strings.LastIndex(saved.WebhookURL, "/")+1:]
	}
	return c.tf.trigger
}

// hook is the address of the team agent's fixture trigger.
func (c *mctx) hook() string {
	c.trigger(false)
	return c.tf.hook
}

// run is a (skipped) run of the team agent's fixture trigger.
func (c *mctx) run() string {
	if c.tf.run == "" {
		trigger := c.trigger(false)
		c.tf.run = c.e.exec(`INSERT INTO trigger_runs (fire_id, trigger_id, agent_id, team_id, trigger_name, trigger_kind, fired_by,
			status, reason, created_at, finished_at)
			VALUES ($1, $2, $3, $4, 'Matrix', 'webhook', 'webhook', 'skipped', 'trigger_paused', now(), now()) RETURNING id::text`,
			fmt.Sprintf("mx_%d", c.e.next()), trigger, c.tf.agent, c.tf.id)
	}
	return c.tf.run
}
