// Package web embeds the admin dashboard static assets.
package web

import "embed"

// Admin is the embedded admin UI filesystem (rooted at admin/).
//
//go:embed all:admin
var Admin embed.FS
