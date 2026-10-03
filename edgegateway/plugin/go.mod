module tv30-auth

// SOMENTE biblioteca padrao: nada em require. Um plugin Go so carrega no
// KrakenD se toda dependencia compartilhada tiver a MESMA versao do binario
// (krakend 2.7.2 / Go 1.22.7) — sem dependencias, nao ha conflito possivel.
go 1.22
