// Package env resolves the plugin's runtime environment: where its config and
// state live, and where the Herdr CLI is.
package env

import (
	"cmp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const PluginID = "herdr-custom-commands"

// Env is the plugin's runtime environment. Herdr sets it for every command it
// runs, but a pane restored from a session snapshot may carry only the
// environment the snapshot captured, so every lookup has a fallback.
type Env struct {
	Root    string
	Config  string
	State   string
	Herdr   string // Herdr CLI binary
	PaneID  string
	SpaceID string
	Context string // raw plugin context JSON
}

func Load() Env {
	config := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	state := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	root := cmp.Or(os.Getenv("HERDR_PLUGIN_ROOT"), exeRoot())

	if config == "" {
		config = filepath.Join(xdgConfigHome(), "herdr", "plugins", "config", PluginID)
		state = filepath.Join(xdgStateHome(), "herdr", "plugins", PluginID)
	} else {
		state = cmp.Or(state, config)
	}

	return Env{
		Root:    root,
		Config:  config,
		State:   state,
		Herdr:   cmp.Or(os.Getenv("HERDR_BIN_PATH"), "herdr"),
		PaneID:  os.Getenv("HERDR_PANE_ID"),
		SpaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		Context: os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"),
	}
}

func (e Env) CommandsFile() string { return filepath.Join(e.Config, "commands.txt") }
func (e Env) WidthFile() string    { return filepath.Join(e.Config, "width") }
func (e Env) TimeoutFile() string  { return filepath.Join(e.Config, "output-timeout") }
func (e Env) PendingFile() string  { return filepath.Join(e.State, "pending") }
func (e Env) RunLog() string       { return filepath.Join(e.State, "last-run.log") }
func (e Env) RunExitFile() string  { return filepath.Join(e.State, "last-run.exit") }
func (e Env) RunCmdFile() string   { return filepath.Join(e.State, "last-run.cmd") }
func (e Env) MarkerFile(suffix string) string {
	return filepath.Join(e.State, ".loaded."+suffix)
}

// No panel reads LegacyMarker. Each panel deletes it, so a stale file cannot be
// mistaken for a live marker.
func (e Env) LegacyMarker() string { return filepath.Join(e.State, ".loaded") }

// ReadIntConfig reads one bounded unsigned number from a config file. Anything
// else, a stray sign or suffix included, leaves the fallback in place rather
// than being half-read.
func ReadIntConfig(path string, fallback, min, max int) int {
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

// The executable lives in the plugin's bin/ directory, so the plugin root is
// its parent's parent.
func exeRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(exe))
}

func xdgConfigHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(Home(), ".config")
}

func xdgStateHome() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(Home(), ".local", "state")
}

func Home() string {
	if dir := os.Getenv("HOME"); dir != "" {
		return dir
	}
	return "/"
}
