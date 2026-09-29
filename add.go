package main

import (
	"errors"
	"os"
	"strings"
	"time"
)

const (
	keyBackspace = "\x7f"
	keyCtrlH     = "\b"
)

// Raw mode, not line mode: Escape closes the popup without a newline.
func runAdd(e env) int {
	term := openTerminal()
	saved := saveModes()
	term.cleanupOnExit(saved)

	term.write(seqCursorOff + "\n  Command: ")
	term.raw()
	_ = term.flush()

	var line []rune
input:
	for {
		ev, err := term.readEvent(time.Second)
		if errors.Is(err, errClosed) {
			break
		}
		if errors.Is(err, errTimeout) {
			continue
		}
		if ev.mouse != nil {
			continue
		}
		switch ev.key {
		case keyEsc:
			return 0
		case keyEnter:
			break input
		case keySpace:
			line = append(line, ' ')
			term.write(" ")
		case keyUp, keyDown, keyLeft, keyRight, keyHome, keyEnd, keyNone:
		case keyBackspace, keyCtrlH:
			if len(line) > 0 {
				line = line[:len(line)-1]
				term.write("\b \b")
			}
		default:
			line = append(line, []rune(ev.key)...)
			term.write(ev.key)
		}
		_ = term.flush()
	}

	command := strings.TrimSuffix(string(line), "\r")
	if strings.ReplaceAll(command, " ", "") != "" && !strings.HasPrefix(command, "#") {
		appendLine(e.commandsFile(), command)
	}
	return 0
}

// The panel reads the list line by line, so a file whose last line has no
// newline would swallow the appended command.
func appendLine(path, line string) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	sep := ""
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		sep = "\n"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(sep + line + "\n")
}
