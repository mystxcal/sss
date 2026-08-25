// Command sss is the single binary: client, daemon, and admin tool.
//
// It also serves as sssend, ssrecv, and sssd when invoked under those names.
package main

import (
	"os"

	"github.com/sss/sss/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args, cli.Default()))
}
