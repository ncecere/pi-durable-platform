-- Threads that exist before they carry MCP servers (#5), and re-checking
-- IdP group membership after sign-in (#6).
--
-- Threads:
--   * thread_mcp_servers belong to a thread record (agent_threads): the
--     servers of a thread nobody has started were the hole (a later private
--     thread with that ID inherited them), so those rows are dropped and the
--     reference is enforced from now on. Deleting a thread (or its agent)
--     deletes its servers. Releases before this one could set servers on an
--     unrecorded thread; their requests fail against the reference during a
--     rolling upgrade (the platform runs one replica).
--   * agent_threads.reserved: the platform created the record (a thread
--     created with POST .../threads, or started by setting its MCP servers)
--     and no message has been accepted yet. Reserved threads are listed only
--     for their creator, are never forgotten when the agent's runtime lists
--     its threads (it does not know them), and are removed after 30 days
--     without a message.

-- +goose Up
DELETE FROM thread_mcp_servers s
WHERE NOT EXISTS (SELECT 1 FROM agent_threads t WHERE t.agent_id = s.agent_id AND t.thread_id = s.thread_id);

ALTER TABLE thread_mcp_servers
    ADD CONSTRAINT thread_mcp_servers_thread_fkey
    FOREIGN KEY (agent_id, thread_id) REFERENCES agent_threads (agent_id, thread_id) ON DELETE CASCADE;

ALTER TABLE agent_threads ADD COLUMN reserved boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE agent_threads DROP COLUMN reserved;
ALTER TABLE thread_mcp_servers DROP CONSTRAINT thread_mcp_servers_thread_fkey;
