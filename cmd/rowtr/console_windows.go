//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// doubleClicked reports whether this process owns its console alone — true
// when the exe was launched from Explorer (double-click) rather than a shell,
// which would itself be attached to the console.
func doubleClicked() bool {
	var pids [2]uint32
	n, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList").
		Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
