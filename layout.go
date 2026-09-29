package main

import (
	"os"
	"strconv"
	"strings"
)

const defaultWidthPermille = 150

// widthPermille is the panel's share of its tab in per-mille, 15% by default.
func widthPermille(e env) int {
	return readConfigInt(e.widthFile(), defaultWidthPermille, 30, 900)
}

// readConfigInt reads one bounded unsigned number from a config file. Anything
// else, a stray sign or suffix included, leaves the default in place rather
// than being half-read.
func readConfigInt(path string, fallback, min, max int) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	n, ok := parseDigits(strings.TrimSpace(string(raw)))
	if !ok || n < min || n > max {
		return fallback
	}
	return n
}

// Each resize moves the nearest divider by a ratio delta, which does not map
// onto cells directly when the tab has nested splits, so measure again and stop
// once close enough.
func applyWidth(e env, paneID string, permille int) {
	for range 5 {
		paneWidth, tabWidth, err := e.layoutWidths(paneID)
		if err != nil || tabWidth <= 0 {
			return
		}
		target := (tabWidth*permille + 500) / 1000
		if target < 2 {
			target = 2
		}
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
		if err := e.resizePane(paneID, direction, amount); err != nil {
			return
		}
	}
}

// parseDigits accepts an unsigned decimal and nothing else, so a stray sign or
// suffix in a hand-edited file is ignored rather than half-read.
func parseDigits(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
