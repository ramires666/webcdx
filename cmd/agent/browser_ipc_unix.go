//go:build !windows

package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

type browserIPCListener struct {
	net.Listener
	path string
}

func browserListenIPC(dir string) (*browserIPCListener, string, error) {
	path := filepath.Join(dir, "browser.sock")
	if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
		conn.Close()
		return nil, "", os.ErrExist
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, "", err
	}
	return &browserIPCListener{listener, path}, path, nil
}

func (l *browserIPCListener) Accept() (io.ReadWriteCloser, error) { return l.Listener.Accept() }
func (l *browserIPCListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}
func browserDialIPC(endpoint string) (io.ReadWriteCloser, error) {
	return net.DialTimeout("unix", endpoint, 3*time.Second)
}
