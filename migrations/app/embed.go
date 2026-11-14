// Package app embeds application-only schema migrations.
package app

import "embed"

//go:embed *.sql
var Files embed.FS
