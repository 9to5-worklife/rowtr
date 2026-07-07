//go:build !windows

package main

// doubleClicked is Windows-only: elsewhere a bare `rowtr` is a shell
// invocation and gets the normal CLI behavior.
func doubleClicked() bool { return false }
