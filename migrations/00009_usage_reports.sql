-- Usage reports: how many model requests a usage row covers (the proxy
-- records one row per request; agents report batches), and indexes for
-- reports by day across every team.

-- +goose Up
ALTER TABLE model_usage ADD COLUMN requests integer NOT NULL DEFAULT 1 CHECK (requests >= 0);
CREATE INDEX model_usage_time ON model_usage (created_at);

-- +goose Down
DROP INDEX model_usage_time;
ALTER TABLE model_usage DROP COLUMN requests;
