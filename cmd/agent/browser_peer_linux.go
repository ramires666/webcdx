package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
)

func browserIPCPeer(conn io.ReadWriteCloser) bool {
	connection, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	var credentials *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credentials == nil || peerErr != nil {
		return false
	}
	if credentials.Uid != uint32(os.Getuid()) {
		return false
	}
	peer, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", credentials.Pid))
	if err != nil {
		return false
	}
	current, err := os.Executable()
	return err == nil && peer == current
}
