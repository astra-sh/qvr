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

// nullableInt64 maps nil → SQL NULL; a non-nil pointer keeps its value (a
// genuine 0 stays 0, distinct from "not reported").
func nullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// nullInt64Ptr is the scan-side inverse of nullableInt64.
func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
