// Package sites embeds the site profiles shipped with clusterwand
// (sites/<name>/site.yaml). Code reads them through internal/site.
package sites

import "embed"

// FS holds every shipped site.yaml.
//
//go:embed */site.yaml
var FS embed.FS
