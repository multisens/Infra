//go:build windows

package main

import "syscall"

// The Windows counterpart of sockopt_other.go: the descriptor is
// syscall.Handle, and socket errors come as WSA codes (winsock2.h), which do
// not match the syscall.E* values of the syscall package on Windows.

func setMulticastOpts(fd uintptr, a [4]byte, ttl int) error {
	h := syscall.Handle(fd)
	if err := syscall.SetsockoptInet4Addr(h, syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, a); err != nil {
		return err
	}
	return syscall.SetsockoptInt(h, syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl)
}

const (
	wsaeAddrInUse    = syscall.Errno(10048)
	wsaeAddrNotAvail = syscall.Errno(10049)
	wsaeNetDown      = syscall.Errno(10050)
	wsaeNetUnreach   = syscall.Errno(10051)
	wsaeHostDown     = syscall.Errno(10064)
	wsaeHostUnreach  = syscall.Errno(10065)
)

var netErrnos = []syscall.Errno{
	wsaeNetUnreach, wsaeHostUnreach, wsaeNetDown, wsaeHostDown, wsaeAddrNotAvail,
}

var errAddrInUse error = wsaeAddrInUse
