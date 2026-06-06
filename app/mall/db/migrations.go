package dbmigrations

import "embed"

// FS contains the mall database migrations.
//
//go:embed migrations/*.sql
var FS embed.FS
