// Package winsys — системная часть Windows для Э2: выбор физического
// интерфейса, маршруты, DNS и подписка на смену сети (architecture.md §09).
//
// Код пакета живёт в файлах *_windows.go, и только они импортируют winipcfg
// из golang.zx2c4.com/wireguard/windows. Под другие системы пакет пустой.
package winsys
