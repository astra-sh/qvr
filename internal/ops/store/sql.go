package store

import "strings"

// placeholders returns "?,?,..." with n marks, for IN clauses.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
