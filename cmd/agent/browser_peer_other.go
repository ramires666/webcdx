//go:build !windows && !linux

package main

import "io"

func browserIPCPeer(conn io.ReadWriteCloser) bool { return false }
