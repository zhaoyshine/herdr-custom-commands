package panel

import (
	"strings"

	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
)

const Label = "Custom Commands"

// truncate cuts a string to at most max runes, marking the cut with an ellipsis.
// Runes, not display cells: a wide CJK character counts as one.
func truncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func pad(s string, width int) string {
	n := width - len([]rune(s))
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}

// shortPath keeps the tail of a path rather than the head: the last segments
// say which project this is, the leading ones rarely do.
func shortPath(path string, max int) string {
	home := env.Home()
	if home != "/" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			path = "~/" + rest
		}
	}
	if len([]rune(path)) <= max {
		return path
	}
	if max < 4 {
		return truncate(path, max)
	}
	r := []rune(path)
	tail := string(r[len(r)-(max-1):])
	if i := strings.Index(tail, "/"); i >= 0 {
		return "…" + tail[i+1:]
	}
	return "…" + tail
}
