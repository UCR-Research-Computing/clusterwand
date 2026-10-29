package mcpserver

import "github.com/UCR-Research-Computing/clusterwand/internal/config"

// applyGoldenSite selects the site profile under test.
func applyGoldenSite(c *config.Config, gs goldenSite) {
	c.Site, c.SiteFile = gs.site, gs.siteFile
}
