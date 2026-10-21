//go:build !windows

package main

import "syscall"

// Opcoes de socket e erros do sistema fora do Windows (Linux e macOS); o par
// do Windows esta em sockopt_windows.go.

func setMulticastOpts(fd uintptr, a [4]byte, ttl int) error {
	if err := syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, a); err != nil {
		return err
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttl)
}

// netErrnos: erros do sistema que sao falta de rede (endereco ou interface
// que sumiu, sem rota). Lista fechada: erro fora dela e de configuracao.
var netErrnos = []syscall.Errno{
	syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN,
	syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL, syscall.ENODEV,
}

// errAddrInUse: a UDP 1900 presa por socket sem SO_REUSEADDR (configuracao).
var errAddrInUse error = syscall.EADDRINUSE
