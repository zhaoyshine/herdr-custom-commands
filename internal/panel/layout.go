package panel

import (
	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/herdr"
)

const defaultWidthPermille = 150

// WidthPermille is the panel's share of its tab in per-mille, 15% by default.
func WidthPermille(e env.Env) int {
	return env.ReadIntConfig(e.WidthFile(), defaultWidthPermille, 30, 900)
}

// Each resize moves the nearest divider by a ratio delta, which does not map
// onto cells directly when the tab has nested splits, so measure again and stop
// once close enough.
func ApplyWidth(c *herdr.Client, paneID string, permille int) {
	for range 5 {
		paneWidth, tabWidth, err := c.LayoutWidths(paneID)
		if err != nil || tabWidth <= 0 {
			return
		}
		target := max((tabWidth*permille+500)/1000, 2)
		diff := paneWidth - target
		if diff >= -1 && diff <= 1 {
			return
		}
		direction := "right"
		if diff < 0 {
			direction = "left"
			diff = -diff
		}
		amount := float64(diff) / float64(tabWidth)
		if err := c.ResizePane(paneID, direction, amount); err != nil {
			return
		}
	}
}
