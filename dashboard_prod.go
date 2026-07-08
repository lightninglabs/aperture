//go:build dashboard

package aperture

import (
	"embed"
	"io/fs"
)

// The all: prefix is required because Next.js outputs assets under _next/
// which Go's embed would otherwise skip (directories starting with _ or .
// are excluded by default).
//
//go:embed all:dashboard/out
var dashboardEmbedFS embed.FS

// DashboardFS returns the embedded dashboard static files rooted at
// dashboard/out so callers can serve them without the path prefix.
func DashboardFS() (fs.FS, error) {
	return fs.Sub(dashboardEmbedFS, "dashboard/out")
}
