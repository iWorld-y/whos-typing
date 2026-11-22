//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package shell

import "syscall"

const pasteTermiosRequest = syscall.TIOCGETA
