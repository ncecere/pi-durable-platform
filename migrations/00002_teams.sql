-- Teams, memberships and email invites.

-- +goose Up
CREATE TABLE teams (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Used in URLs; immutable.
    slug        text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$'),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description text        NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by  uuid        REFERENCES users (id),
    revision    bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);

CREATE TABLE team_members (
    team_id    uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'member')),
    added_by   uuid        REFERENCES users (id),
    revision   bigint      NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user_idx ON team_members (user_id);

CREATE TABLE team_invites (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id          uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    email            citext      NOT NULL,
    role             text        NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'member')),
    invited_by       uuid        REFERENCES users (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    accepted_at      timestamptz,
    accepted_user_id uuid        REFERENCES users (id),
    revoked_at       timestamptz
);
-- At most one open invite per team and email.
CREATE UNIQUE INDEX team_invites_open_key ON team_invites (team_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX team_invites_open_email_idx ON team_invites (email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- +goose Down
DROP TABLE team_invites;
DROP TABLE team_members;
DROP TABLE teams;
