package store

// LogIPCAudit records a mutating MCP tool call. params is JSON-encoded;
// secret values must be redacted by the caller before encoding.
func (s *Store) LogIPCAudit(folder, sub, tool, params, outcome string) error {
	_, err := s.db.Exec(
		`INSERT INTO ipc_audit (folder, sub, tool, params, outcome) VALUES (?,?,?,?,?)`,
		folder, sub, tool, params, outcome,
	)
	return err
}
