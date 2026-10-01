// Package migrations embebe los archivos SQL de goose en el binario.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
