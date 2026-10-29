package main

import "github.com/UCR-Research-Computing/clusterwand/internal/config"

const goldenCLISiteName = "ursa-major"

func applyCLIGoldenSite(c *config.Config) { c.Site = "ursa-major" }
