-- The platform's record of every agent conversation (thread): its title,
-- who started it, and whether a reply is in progress, finished or failed.
-- Listing an agent's threads reads this, so it does not wake the agent.
--
-- The platform writes it when it relays a message (busy, and the title from
-- the first message) and on renames; the agent's runtime reports when a reply
-- ends and, when it starts, every thread it has (gateway /threads/*). Status
-- changes carry the time they happened (status_at); an older report never
-- overwrites a newer one. Every change notifies channel pdp_threads with the
-- agent's ID, for the event stream.

-- +goose Up
CREATE TABLE agent_threads (
    agent_id           uuid        NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    thread_id          text        NOT NULL CHECK (thread_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    title              text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    created_by_user    uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_by_service uuid        REFERENCES service_accounts (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    busy               boolean     NOT NULL DEFAULT false,
    last_reply_at      timestamptz,
    last_error         text        CHECK (char_length(last_error) <= 2000),
    status_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, thread_id)
);
CREATE INDEX agent_threads_updated ON agent_threads (agent_id, updated_at DESC);

-- +goose Down
DROP TABLE agent_threads;
