package main

import (
	"MyCode/internal/app"
	"os"
)

// version is replaced during release builds with:
// go build -ldflags "-X main.version=vX.Y.Z"
var version = "0.1.0"

func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
