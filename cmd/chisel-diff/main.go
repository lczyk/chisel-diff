package main

import (
	"os"

	"github.com/lczyk/chisel-diff/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
