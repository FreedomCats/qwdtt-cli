// Package linuxsys — системная часть Linux для Э4: выбор физического
// интерфейса и подписка на смену сети (plan-e4.md, блок N), дальше — адаптер,
// маршруты и DNS. Аналог winsys.
//
// Код пакета живёт в файлах *_linux.go, и только они импортируют netlink
// из github.com/vishvananda/netlink. Под другие системы пакет пустой.
package linuxsys
