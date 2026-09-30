// Package app owns the process's exit path: the signal handler and the
// cleanup every entrypoint registers for it.
package app

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	cleanupFn func()
	signals   = make(chan os.Signal, 1)
	armed     sync.Once
)

// OnCleanup registers the cleanup that runs when the process leaves, and arms
// the signal handler that leaves it early.
func OnCleanup(fn func()) {
	cleanupFn = sync.OnceFunc(fn)
	armed.Do(func() {
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
		// A stopped panel could not hand its terminal back: this pane has no
		// shell to resume it from.
		signal.Ignore(syscall.SIGTSTP)
		go func() {
			<-signals
			Cleanup()
			os.Exit(130)
		}()
	})
}

func Cleanup() {
	if cleanupFn != nil {
		cleanupFn()
	}
}
