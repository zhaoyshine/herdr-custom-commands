package main

import (
	"fmt"
	"os"

	"github.com/zhaoyshine/herdr-custom-commands/internal/add"
	"github.com/zhaoyshine/herdr-custom-commands/internal/app"
	"github.com/zhaoyshine/herdr-custom-commands/internal/env"
	"github.com/zhaoyshine/herdr-custom-commands/internal/panel"
	"github.com/zhaoyshine/herdr-custom-commands/internal/runner"
	"github.com/zhaoyshine/herdr-custom-commands/internal/startup"
)

func main() {
	e := env.Load()
	_ = os.MkdirAll(e.Config, 0o755)
	_ = os.MkdirAll(e.State, 0o755)

	var code int
	switch entrypoint() {
	case "panel":
		code = panel.New(e).Run()
	case "add":
		code = add.Run(e)
	case "run":
		code = runner.Run(e)
	case "startup":
		code = startup.Run(e)
	default:
		fmt.Fprintf(os.Stderr, "usage: hcc {panel|add|run|startup}\n")
		code = 2
	}
	app.Cleanup()
	os.Exit(code)
}

func entrypoint() string {
	if len(os.Args) < 2 {
		return ""
	}
	return os.Args[1]
}
