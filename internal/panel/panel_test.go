package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
)

const fakeHerdr = `#!/bin/sh
case "$1" in
pane) cat "$FAKE_HERDR_STATE" ;;
*) printf '{}' ;;
esac
`

func fakeEnv(t *testing.T) (env.Env, string, string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "herdr")
	if err := os.WriteFile(bin, []byte(fakeHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	sf := filepath.Join(root, "fake-state")
	t.Setenv("FAKE_HERDR_STATE", sf)
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := `{"workspace_cwd":"` + a + `","focused_pane_cwd":"` + a + `"}`
	e := env.Env{
		Root:    root,
		Config:  filepath.Join(root, "config"),
		State:   filepath.Join(root, "runstate"),
		Herdr:   bin,
		PaneID:  "w1:p2",
		SpaceID: "w1",
		Context: ctx,
	}
	return e, a, b, sf
}

func writePanes(t *testing.T, sf, userCwd string, userFocused bool) {
	t.Helper()
	panelFocused := "false"
	if !userFocused {
		panelFocused = "true"
	}
	userFocusedJSON := "true"
	if !userFocused {
		userFocusedJSON = "false"
	}
	raw := `{"result":{"panes":[` +
		`{"pane_id":"w1:p1","workspace_id":"w1","focused":` + userFocusedJSON + `,"cwd":"` + userCwd + `"},` +
		`{"pane_id":"w1:p2","workspace_id":"w1","focused":` + panelFocused + `,"label":"Custom Commands","cwd":"/"}` +
		`]}}`
	if err := os.WriteFile(sf, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecCwdFollowsCdedPane(t *testing.T) {
	e, a, b, sf := fakeEnv(t)
	p := New(e)
	if got := p.execCwd(); got != a {
		t.Fatalf("execCwd = %q, want %q", got, a)
	}
	writePanes(t, sf, b, true)
	if got := p.execCwd(); got != b {
		t.Fatalf("execCwd after cd = %q, want %q", got, b)
	}
}

func TestExecCwdKeepsCachedDirWhilePanelFocused(t *testing.T) {
	e, a, b, sf := fakeEnv(t)
	p := New(e)
	if got := p.execCwd(); got != a {
		t.Fatalf("execCwd = %q, want %q", got, a)
	}
	writePanes(t, sf, b, true)
	if got := p.execCwd(); got != b {
		t.Fatalf("execCwd after cd = %q, want %q", got, b)
	}
	writePanes(t, sf, b, false)
	if got := p.execCwd(); got != b {
		t.Fatalf("execCwd with panel focused = %q, want %q", got, b)
	}
}

func TestExecCwdFallsBackToContext(t *testing.T) {
	e, a, _, sf := fakeEnv(t)
	writePanes(t, sf, a, true)
	p := New(e)
	if err := os.Remove(sf); err != nil {
		t.Fatal(err)
	}
	if got := p.execCwd(); got != a {
		t.Fatalf("execCwd without live panes = %q, want %q", got, a)
	}
}

func TestExecCwdFallsBackToHome(t *testing.T) {
	e, a, _, sf := fakeEnv(t)
	writePanes(t, sf, a, true)
	p := New(e)
	if err := os.Remove(sf); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if got := p.execCwd(); got != env.Home() {
		t.Fatalf("execCwd without live panes or context dir = %q, want %q", got, env.Home())
	}
}

func TestTickUpdatesTitleDir(t *testing.T) {
	e, a, b, sf := fakeEnv(t)
	p := New(e)
	p.titleDir = p.execCwd()
	if p.titleDir != a {
		t.Fatalf("titleDir = %q, want %q", p.titleDir, a)
	}
	writePanes(t, sf, b, true)
	for range 5 {
		p.tick()
	}
	if p.titleDir != b {
		t.Fatalf("titleDir after ticks = %q, want %q", p.titleDir, b)
	}
}
