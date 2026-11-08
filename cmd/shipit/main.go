// shipit deploys a project to your own server from your own computer.
package main

import (
	"os"

	"github.com/amohammedimran/shipit/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:])) }
