//go:build !windows

package main

import "syscall"

// Socket options and system errors outside Windows (Linux and macOS); the
// Windows counterpart is sockopt_windows.go.

func setMulticastOpts(fd uintptr, a [4]byte, ttl int) error {
	if err := syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, a); err != nil {
		return err
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl)
}

// netErrnos: system errors that are a lack of network (address or interface
// gone, no route). A closed list: an error outside it is a configuration
// error.
var netErrnos = []syscall.Errno{
	syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN,
	syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL, syscall.ENODEV,
}

// errAddrInUse: UDP 1900 held by a socket without SO_REUSEADDR
// (configuration).
var errAddrInUse error = syscall.EADDRINUSE
