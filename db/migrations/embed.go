package migrations

import "embed"

// Files contains repository-owned ordered SQL migrations.
//
//go:embed *.sql
var Files embed.FS
