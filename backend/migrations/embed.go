// Package migrations embeds the golang-migrate SQL files so they ship inside
// the binary and run on startup. The embed lives here (not in internal/store)
// because //go:embed cannot reference parent directories; store consumes FS
// via the iofs source driver.
package migrations

import "embed"

// FS holds every *.sql migration, rooted at this directory.
//
//go:embed *.sql
var FS embed.FS
