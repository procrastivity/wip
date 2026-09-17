package store

// v10 keeps the original backlog detail separate from the reason for a later
// decline. Existing rows receive the empty value until the projection refolds.
func v10Statements() []string {
	return []string{
		`ALTER TABLE backlog_entries ADD COLUMN decline_reason TEXT NOT NULL DEFAULT ''`,
	}
}
