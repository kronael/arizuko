package store

// LogCLIAudit records a mutating CLI operation. args is the space-joined
// argument list; values are redacted for secret set / user-secret set.
func (s *Store) LogCLIAudit(osUser, command, args string) error {
	_, err := s.db.Exec(
		`INSERT INTO cli_audit (os_user, command, args) VALUES (?,?,?)`,
		osUser, command, args,
	)
	return err
}
