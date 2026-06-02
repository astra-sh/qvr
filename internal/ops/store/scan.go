package store

import "database/sql"

// nullableString maps "" → SQL NULL so empty Go strings round-trip as NULL
// columns rather than empty-string columns.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
