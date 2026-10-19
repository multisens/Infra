//go:build !windows

package main

import "syscall"

// Opcoes de socket e erros do sistema fora do Windows (Linux e macOS). O
// Windows tem o par em sockopt_windows.go: la o descritor e syscall.Handle, e
// os erros de rede sao os codigos WSA (complemento do Joel de 15/10: os
// binarios dos tres sistemas saem do mesmo codigo, so com a biblioteca
// padrao).

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
