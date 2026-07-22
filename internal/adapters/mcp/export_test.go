package mcp

// TrackedSessionCounts reports how many connections and started sessions the
// adapter still tracks. Transports that serve many sessions from one server
// reuse the adapter, so these counts must return to zero once sessions end.
func (server *Server) TrackedSessionCounts() (connections int, started int) {
	if server == nil {
		return 0, 0
	}
	server.adapter.sessionMu.Lock()
	defer server.adapter.sessionMu.Unlock()
	return len(server.adapter.connectionSessions), len(server.adapter.sessionStarted)
}
