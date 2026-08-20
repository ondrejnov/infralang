package main

import (
	"os"
)

func main() {
	os.Exit(newServer(os.Stdin, os.Stdout).run())
}
