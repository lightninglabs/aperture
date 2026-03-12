//go:build !nodashboard

package aperture

import (
	"embed"
	"io/fs"
)

//go:embed dashboard/out
var dashboardEmbedFS embed.FS

// DashboardFS returns the embedded dashboard static files rooted at
// dashboard/out so callers can serve them without the path prefix.
func DashboardFS() (fs.FS, error) {
	return fs.Sub(dashboardEmbedFS, "dashboard/out")
}
