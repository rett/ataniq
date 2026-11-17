package main

import "os"

func main() {
	os.Exit(execute(os.Args, os.Stderr))
}
