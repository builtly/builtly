// Package migrations embeds SQL migration files for both dialects.
package migrations

import "embed"

// FS contains postgres/ and sqlite/ migration trees.
//
//go:embed postgres/*.sql sqlite/*.sql
var FS embed.FS
