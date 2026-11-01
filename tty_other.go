//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package main

import "os"

func ttyWidth(*os.File) int { return 0 }
