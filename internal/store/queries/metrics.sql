-- name: CountAgentsByEnabled :many
-- How many agents exist, enabled or not (the pdp_agents metric). Counts
-- only: nothing identifies an agent.
SELECT enabled, count(*)::bigint AS total FROM agents GROUP BY enabled;
