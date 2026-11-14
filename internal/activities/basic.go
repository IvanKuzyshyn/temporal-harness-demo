// Package activities is the I/O boundary for web workflow orchestration.
package activities

import "unicode/utf8"

// bounded preserves UTF-8 while bounding persisted document excerpts in bytes.
func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}
