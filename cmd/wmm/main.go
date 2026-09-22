package main

import (
	"github.com/eseceve/workmux-multirepo/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
