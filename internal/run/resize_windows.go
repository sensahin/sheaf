package run

import "os"

// watchResize is a no-op on Windows, which has no SIGWINCH.
func watchResize(tty, ptmx *os.File) func() { return func() {} }
