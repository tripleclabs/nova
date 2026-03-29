package main

import (
	"fmt"
	"os"

	"github.com/3clabs/nova/internal/cmd"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	root := cmd.NewRootCmd(version)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
