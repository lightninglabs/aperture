//go:build !dashboard

package aperture

import "io/fs"

// DashboardFS returns nil when the dashboard is not embedded.
func DashboardFS() (fs.FS, error) {
	return nil, nil
}
