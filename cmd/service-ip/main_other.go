//go:build !linux

// Command service-ip only runs on Linux: it drives netlink and raw ARP
// sockets. This stub keeps `go build ./...` and `go test ./...` working on
// the machines the rest of SDS is developed on.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "service-ip runs on Linux only")
	os.Exit(1)
}
