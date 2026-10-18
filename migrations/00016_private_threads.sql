-- Private threads and who has sent to a thread.
--
--   * agent_threads.visibility: 'shared' (everyone who may use the agent
--     lists and reads it; threads recorded before this, or reported by the
--     runtime without a record, are shared) or 'private' (listed and read only
--     by its creator: created_by_user, or created_by_service for a service
--     account). It is chosen with the thread's first message and never
--     changes. The platform writes the record before it relays that message,
--     so a private thread is never briefly shared.
--   * agent_thread_participants: each person or service account that has
--     sent a message to the thread (recorded after the runtime accepted it),
--     with when they first and last did.

-- +goose Up
ALTER TABLE agent_threads
    ADD COLUMN visibility text NOT NULL DEFAULT 'shared' CHECK (visibility IN ('shared', 'private'));

CREATE TABLE agent_thread_participants (
    agent_id           uuid        NOT NULL,
    thread_id          text        NOT NULL,
    user_id            uuid        REFERENCES users (id) ON DELETE CASCADE,
    service_account_id uuid        REFERENCES service_accounts (id) ON DELETE CASCADE,
    first_sent_at      timestamptz NOT NULL DEFAULT now(),
    last_sent_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (agent_id, thread_id) REFERENCES agent_threads (agent_id, thread_id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (service_account_id IS NULL))
);
CREATE UNIQUE INDEX agent_thread_participants_user ON agent_thread_participants (agent_id, thread_id, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX agent_thread_participants_service ON agent_thread_participants (agent_id, thread_id, service_account_id)
    WHERE service_account_id IS NOT NULL;

-- +goose Down
DROP TABLE agent_thread_participants;
ALTER TABLE agent_threads DROP COLUMN visibility;
