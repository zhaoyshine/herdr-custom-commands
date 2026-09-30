package add

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/zhaoyshine/herdr-custom-commands/internal/app"
	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/terminal"
)

const (
	keyBackspace = "\x7f"
	keyCtrlH     = "\b"
)

// Raw mode, not line mode: Escape closes the popup without a newline.
func Run(e env.Env) int {
	term := terminal.Open()
	saved := terminal.SaveModes()
	app.OnCleanup(func() { term.Shutdown(saved) })

	term.Write(terminal.SeqCursorOff + "\n  Command: ")
	term.Raw()
	_ = term.Flush()

	var line []rune
input:
	for {
		ev, err := term.ReadEvent(time.Second)
		if errors.Is(err, terminal.ErrClosed) {
			break
		}
		if errors.Is(err, terminal.ErrTimeout) {
			continue
		}
		if ev.Mouse != nil {
			continue
		}
		switch ev.Key {
		case terminal.KeyEsc:
			return 0
		case terminal.KeyEnter:
			break input
		case terminal.KeySpace:
			line = append(line, ' ')
			term.Write(" ")
		case terminal.KeyUp, terminal.KeyDown, terminal.KeyLeft, terminal.KeyRight, terminal.KeyHome, terminal.KeyEnd, terminal.KeyNone:
		case keyBackspace, keyCtrlH:
			if len(line) > 0 {
				line = line[:len(line)-1]
				term.Write("\b \b")
			}
		default:
			line = append(line, []rune(ev.Key)...)
			term.Write(ev.Key)
		}
		_ = term.Flush()
	}

	command := strings.TrimSuffix(string(line), "\r")
	if strings.ReplaceAll(command, " ", "") != "" && !strings.HasPrefix(command, "#") {
		appendLine(e.CommandsFile(), command)
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
