package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

const pluginID = "herdr-custom-commands"

var (
	cleanupOnce sync.Once
	cleanupFn   func()
	signals     = make(chan os.Signal, 1)
)

func installCleanup(fn func()) {
	cleanupFn = fn
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	// A stopped panel could not hand its terminal back: this pane has no shell
	// to resume it from.
	signal.Ignore(syscall.SIGTSTP)
	go func() {
		<-signals
		runCleanup()
		os.Exit(130)
	}()
}

func runCleanup() {
	cleanupOnce.Do(func() {
		if cleanupFn != nil {
			cleanupFn()
		}
	})
}

func main() {
	e := loadEnv()
	_ = os.MkdirAll(e.config, 0o755)
	_ = os.MkdirAll(e.state, 0o755)

	var code int
	switch entrypoint() {
	case "panel":
		code = newPanel(e).run()
	case "add":
		code = runAdd(e)
	case "run":
		code = runRunner(e)
	case "startup":
		code = runStartup(e)
	default:
		fmt.Fprintf(os.Stderr, "usage: hcc {panel|add|run|startup}\n")
		code = 2
	}
	runCleanup()
	os.Exit(code)
}

func entrypoint() string {
	if len(os.Args) < 2 {
		return ""
	}
	return os.Args[1]
}
