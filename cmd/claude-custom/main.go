package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"claude-custom/internal/cli"
)

func main() {
	code, err := cli.Run(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "claude-custom:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
