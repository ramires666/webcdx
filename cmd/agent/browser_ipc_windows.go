package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type browserIPCListener struct {
	mu     sync.Mutex
	name   string
	handle windows.Handle
	closed bool
}

func browserListenIPC(dir string) (*browserIPCListener, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, "", err
	}
	name := `\\.\pipe\webcodex-browser-` + commandHash(user.User.Sid.String())[:16]
	listener := &browserIPCListener{name: name}
	if err := listener.create(); err != nil {
		return nil, "", err
	}
	return listener, name, nil
}

func (l *browserIPCListener) create() error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, &sa)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		windows.CloseHandle(handle)
		return os.ErrClosed
	}
	l.handle = handle
	return nil
}

func (l *browserIPCListener) Accept() (io.ReadWriteCloser, error) {
	l.mu.Lock()
	handle, closed := l.handle, l.closed
	l.mu.Unlock()
	if closed {
		return nil, os.ErrClosed
	}
	if handle == 0 {
		if err := l.create(); err != nil {
			return nil, err
		}
		l.mu.Lock()
		handle = l.handle
		l.mu.Unlock()
	}
	for {
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			return nil, err
		}
		overlap := windows.Overlapped{HEvent: event}
		err = windows.ConnectNamedPipe(handle, &overlap)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			var n uint32
			err = windows.GetOverlappedResult(handle, &overlap, &n, true)
		}
		windows.CloseHandle(event)
		if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			break
		}
		if errors.Is(err, windows.ERROR_NO_DATA) {
			_ = windows.DisconnectNamedPipe(handle)
			continue
		}
		return nil, err
	}
	mode := uint32(windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT)
	if err := windows.SetNamedPipeHandleState(handle, &mode, nil, nil); err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.handle = 0
	l.mu.Unlock()
	return &browserPipeConn{handle: handle, server: true}, nil
}

func (l *browserIPCListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.handle != 0 {
		_ = windows.CancelIoEx(l.handle, nil)
		return windows.CloseHandle(l.handle)
	}
	return nil
}

func browserDialIPC(endpoint string) (io.ReadWriteCloser, error) {
	name, err := windows.UTF16PtrFromString(endpoint)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	return &browserPipeConn{handle: handle}, nil
}

type browserPipeConn struct {
	handle windows.Handle
	server bool
	once   sync.Once
}

func (c *browserPipeConn) Read(data []byte) (int, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	var n uint32
	err = windows.ReadFile(c.handle, data, &n, &overlap)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(c.handle, &overlap, &n, true)
	}
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		err = io.EOF
	}
	return int(n), err
}
func (c *browserPipeConn) Write(data []byte) (int, error) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlap := windows.Overlapped{HEvent: event}
	var n uint32
	err = windows.WriteFile(c.handle, data, &n, &overlap)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		err = windows.GetOverlappedResult(c.handle, &overlap, &n, true)
	}
	return int(n), err
}
func (c *browserPipeConn) Close() error {
	var err error
	c.once.Do(func() {
		_ = windows.CancelIoEx(c.handle, nil)
		if c.server {
			_ = windows.DisconnectNamedPipe(c.handle)
		}
		err = windows.CloseHandle(c.handle)
	})
	return err
}

func browserIPCPeer(conn io.ReadWriteCloser) bool {
	pipe, ok := conn.(*browserPipeConn)
	if !ok {
		return false
	}
	var pid uint32
	if windows.GetNamedPipeClientProcessId(pipe.handle, &pid) != nil {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if windows.QueryFullProcessImageName(process, 0, &buffer[0], &size) != nil {
		return false
	}
	executable, err := os.Executable()
	return err == nil && strings.EqualFold(windows.UTF16ToString(buffer[:size]), executable)
}
