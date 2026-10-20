-- Tool approvals (docs/approvals.md): a person approves sensitive tool calls
-- before they run.
--
--   * agent_template_versions.approval_*: which tools ask first (names or
--     patterns with *), the lowest team role that may decide calls in shared
--     threads besides the person the run is for (owners and admins always),
--     and how long a request waits before it is denied. Part of the version,
--     so upgrades and rollbacks change it like the instructions.
--   * tool_approvals: one request per call (or per key an author's hook
--     asked with), reported by the agent's runtime, decided by a person, timed
--     out or cancelled. The ID is the runtime's (derived from the call's
--     durable task), unique per agent. arguments is a bounded, redacted view;
--     approver_role and expires_at are taken from the agent's version when the
--     request arrives. A decision is delivered to the runtime (waking it);
--     delivered_at records that it landed.

-- +goose Up
ALTER TABLE agent_template_versions
    ADD COLUMN approval_tools text[] NOT NULL DEFAULT '{}' CHECK (cardinality(approval_tools) <= 100),
    ADD COLUMN approval_role text NOT NULL DEFAULT 'admin' CHECK (approval_role IN ('admin', 'editor', 'member')),
    ADD COLUMN approval_timeout_seconds integer NOT NULL DEFAULT 86400
        CHECK (approval_timeout_seconds BETWEEN 60 AND 2592000);

CREATE TABLE tool_approvals (
    agent_id              uuid        NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    id                    text        NOT NULL CHECK (id ~ '^ap_[A-Za-z0-9_-]{1,64}$'),
    thread_id             text        NOT NULL CHECK (thread_id ~ '^[A-Za-z0-9_-]{0,64}$'),
    tool                  text        NOT NULL CHECK (char_length(tool) BETWEEN 1 AND 200),
    call_id               text        NOT NULL CHECK (char_length(call_id) <= 200),
    arguments             jsonb       NOT NULL DEFAULT '{}',
    summary               text        NOT NULL DEFAULT '' CHECK (char_length(summary) <= 2000),
    requested_for_kind    text        CHECK (requested_for_kind IN ('user', 'service_account')),
    requested_for_id      uuid,
    requested_for_name    text        NOT NULL DEFAULT '' CHECK (char_length(requested_for_name) <= 200),
    approver_role         text        NOT NULL DEFAULT 'admin' CHECK (approver_role IN ('admin', 'editor', 'member')),
    status                text        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'timed_out', 'cancelled')),
    requested_at          timestamptz NOT NULL DEFAULT now(),
    expires_at            timestamptz NOT NULL,
    decided_at            timestamptz,
    decided_by_user       uuid        REFERENCES users (id) ON DELETE SET NULL,
    decided_by_service    uuid        REFERENCES service_accounts (id) ON DELETE SET NULL,
    decided_by_name       text        NOT NULL DEFAULT '' CHECK (char_length(decided_by_name) <= 200),
    reason                text        NOT NULL DEFAULT '' CHECK (char_length(reason) <= 1000),
    delivered_at          timestamptz,
    delivery_attempts     integer     NOT NULL DEFAULT 0,
    next_delivery_at      timestamptz,
    PRIMARY KEY (agent_id, id),
    CHECK ((requested_for_kind IS NULL) = (requested_for_id IS NULL))
);
CREATE INDEX tool_approvals_thread ON tool_approvals (agent_id, thread_id, requested_at DESC);
CREATE INDEX tool_approvals_pending ON tool_approvals (expires_at) WHERE status = 'pending';
CREATE INDEX tool_approvals_undelivered ON tool_approvals (next_delivery_at)
    WHERE status <> 'pending' AND delivered_at IS NULL;

-- +goose Down
DROP TABLE tool_approvals;
ALTER TABLE agent_template_versions
    DROP COLUMN approval_timeout_seconds,
    DROP COLUMN approval_role,
    DROP COLUMN approval_tools;
