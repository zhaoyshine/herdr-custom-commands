package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/fs"
)

const stalePendingSeconds = 300

// childScript runs the user's line in their own shell, with the rc file already
// sourced: a non-interactive shell reads no rc, and Bash expands an alias only
// with expand_aliases on. Shell, line and rc reach that shell as argv, never
// spliced into the script text.
const childScript = `shell=$1; cmd=$2; rc=$3; exitf=$4
if [ -n "$rc" ]; then
  case "${shell##*/}" in
    bash) "$shell" -O expand_aliases -c '. "$1" >/dev/null 2>&1; eval "$2"' _ "$rc" "$cmd" ;;
    *) "$shell" -c '. "$1" >/dev/null 2>&1; eval "$2"' _ "$rc" "$cmd" ;;
  esac
else
  "$shell" -c "$cmd"
fi
status=$?
printf '%s' "$status" >"$exitf"`

func Run(e env.Env) int {
	raw, err := os.ReadFile(e.PendingFile())
	if err != nil {
		return 0
	}
	_ = os.Remove(e.PendingFile())

	lines := strings.SplitN(strings.TrimRight(string(raw), "\n"), "\n", 3)
	if len(lines) < 3 {
		return 0
	}
	runCwd, command, stamp := lines[0], lines[1], lines[2]
	if command == "" {
		return 0
	}
	if ts, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64); err == nil {
		if time.Now().Unix()-ts > stalePendingSeconds {
			return 0
		}
	}

	if !fs.IsDir(runCwd) {
		runCwd = env.Home()
	}
	shell := userShell()
	rc := shellRc(shell)

	// A panel restored from a session snapshot has no other way to label its
	// output area with the command the output belongs to.
	_ = os.WriteFile(e.RunCmdFile(), []byte(command+"\n"), 0o644)

	if err := os.Chdir(runCwd); err != nil {
		_ = os.Chdir(env.Home())
	}
	launchDetached(e, shell, command, rc)
	return 0
}

// userShell is $SHELL when it names an executable, and bash otherwise.
func userShell() string {
	shell := os.Getenv("SHELL")
	if !filepath.IsAbs(shell) {
		return "/bin/bash"
	}
	info, err := os.Stat(shell)
	if err != nil || info.Mode()&0o111 == 0 {
		return "/bin/bash"
	}
	return shell
}

func shellRc(shell string) string {
	switch filepath.Base(shell) {
	case "zsh":
		// ZDOTDIR is often set to a terminal integration directory by the
		// emulator Herdr runs under, so only honour it when it holds an rc file.
		if dir := os.Getenv("ZDOTDIR"); dir != "" {
			if p := filepath.Join(dir, ".zshrc"); fs.Exists(p) {
				return p
			}
		}
		if p := filepath.Join(env.Home(), ".zshrc"); fs.Exists(p) {
			return p
		}
	case "bash":
		if p := filepath.Join(env.Home(), ".bashrc"); fs.Exists(p) {
			return p
		}
	}
	return ""
}

// The new session holds no pipe open to the action and gives the command no
// controlling terminal to lose.
func launchDetached(e env.Env, shell, command, rc string) {
	log, err := os.OpenFile(e.RunLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		reportLaunchFailure(e)
		return
	}
	defer log.Close()

	cmd := exec.Command("/bin/bash", "-c", childScript, "cs",
		shell, command, rc, e.RunExitFile())
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		reportLaunchFailure(e)
		return
	}
	_ = cmd.Process.Release()
}

// A launch that never reached the shell writes no exit file of its own, and the
// panel reads a missing one as "still running".
func reportLaunchFailure(e env.Env) {
	_ = os.WriteFile(e.RunExitFile(), []byte("-1"), 0o644)
}
