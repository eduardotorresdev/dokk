//go:build darwin || linux || freebsd || netbsd || openbsd

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// ttyWidth lê a largura do terminal (TIOCGWINSZ); 0 se não der.
func ttyWidth(f *os.File) int {
	var ws struct{ row, col, x, y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.col)
}
