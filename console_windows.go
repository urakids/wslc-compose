//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const enableVirtualTerminalProcessing = 0x0004

func consoleMode(f *os.File) (uint32, bool) {
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(f.Fd(), uintptr(unsafe.Pointer(&mode)))
	return mode, r != 0
}

func isTerminal(f *os.File) bool {
	_, ok := consoleMode(f)
	return ok
}

// enableVT は Windows コンソールで ANSI エスケープを有効化する。
func enableVT() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if mode, ok := consoleMode(f); ok {
			procSetConsoleMode.Call(f.Fd(), uintptr(mode|enableVirtualTerminalProcessing))
		}
	}
}
