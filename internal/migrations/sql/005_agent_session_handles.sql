ALTER TABLE agent_sessions ADD COLUMN handle_hash BLOB;

UPDATE agent_sessions
SET handle_hash = randomblob(32)
WHERE handle_hash IS NULL;

CREATE UNIQUE INDEX agent_sessions_handle_hash_idx ON agent_sessions(handle_hash);
