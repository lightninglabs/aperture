//go:build dashboard

package aperture

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDashboardEmbedContainsAllAssets verifies that the embedded dashboard
// filesystem contains all critical asset types: HTML pages, JS bundles, CSS,
// and self-hosted fonts. This ensures the binary is fully self-contained with
// no external requests needed.
func TestDashboardEmbedContainsAllAssets(t *testing.T) {
	dashFS, err := DashboardFS()
	require.NoError(t, err)
	require.NotNil(t, dashFS, "DashboardFS should not be nil "+
		"when built with -tags=dashboard")

	var (
		htmlFiles []string
		jsFiles   []string
		cssFiles  []string
		fontFiles []string
	)

	err = fs.WalkDir(dashFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(path, ".html"):
			htmlFiles = append(htmlFiles, path)
		case strings.HasSuffix(path, ".js"):
			jsFiles = append(jsFiles, path)
		case strings.HasSuffix(path, ".css"):
			cssFiles = append(cssFiles, path)
		case strings.HasSuffix(path, ".woff2"):
			fontFiles = append(fontFiles, path)
		}
		return nil
	})
	require.NoError(t, err)

	// Verify we have the root index.html.
	require.Contains(t, htmlFiles, "index.html",
		"embedded FS must contain root index.html")

	// Verify we have page routes.
	foundServices := false
	foundTransactions := false
	for _, f := range htmlFiles {
		if strings.Contains(f, "services") {
			foundServices = true
		}
		if strings.Contains(f, "transactions") {
			foundTransactions = true
		}
	}
	require.True(t, foundServices,
		"embedded FS must contain services page HTML")
	require.True(t, foundTransactions,
		"embedded FS must contain transactions page HTML")

	// Verify JS bundles exist.
	require.NotEmpty(t, jsFiles,
		"embedded FS must contain JavaScript bundles")

	// Verify CSS exists.
	require.NotEmpty(t, cssFiles,
		"embedded FS must contain CSS files")

	// Verify all 7 self-hosted font files are present.
	expectedFonts := []string{
		"fonts/open-sans-300.woff2",
		"fonts/open-sans-400.woff2",
		"fonts/open-sans-600.woff2",
		"fonts/open-sans-700.woff2",
		"fonts/work-sans-300.woff2",
		"fonts/work-sans-500.woff2",
		"fonts/work-sans-600.woff2",
	}
	for _, f := range expectedFonts {
		require.Contains(t, fontFiles, f,
			"embedded FS must contain font: %s", f)
	}

	t.Logf("Embedded dashboard contains: %d HTML, %d JS, %d CSS, "+
		"%d font files", len(htmlFiles), len(jsFiles),
		len(cssFiles), len(fontFiles))
}

// TestDashboardEmbedNoCDNReferences scans all HTML and CSS files in the
// embedded FS to verify no external CDN references remain (e.g. Google
// Fonts, unpkg, cdnjs).
func TestDashboardEmbedNoCDNReferences(t *testing.T) {
	dashFS, err := DashboardFS()
	require.NoError(t, err)
	require.NotNil(t, dashFS)

	blockedDomains := []string{
		"fonts.googleapis.com",
		"fonts.gstatic.com",
		"unpkg.com",
		"cdnjs.cloudflare.com",
		"cdn.jsdelivr.net",
	}

	err = fs.WalkDir(dashFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		// Only check HTML and CSS files for external references.
		if !strings.HasSuffix(path, ".html") &&
			!strings.HasSuffix(path, ".css") {

			return nil
		}

		data, readErr := fs.ReadFile(dashFS, path)
		if readErr != nil {
			return readErr
		}
		content := string(data)

		for _, domain := range blockedDomains {
			require.False(t,
				strings.Contains(content, domain),
				"file %s contains reference to external "+
					"CDN %s — all assets must be "+
					"self-hosted", path, domain,
			)
		}
		return nil
	})
	require.NoError(t, err)
}
