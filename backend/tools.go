//go:build tools

// Package tools pins build-time tool dependencies (never imported by the
// product) so `go run github.com/gzuidhof/tygo` uses the module-locked
// version. See https://go.dev/wiki/Modules#how-can-i-track-tool-dependencies-for-a-module
package tools

import (
	// The module root is package main (not importable); its cmd package is
	// the binary's entire import graph, so this blank import keeps every
	// dependency of `go run github.com/gzuidhof/tygo` in go.mod/go.sum
	// across `go mod tidy`.
	_ "github.com/gzuidhof/tygo/cmd"
)
