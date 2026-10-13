//go:build !linux

package cli

import "github.com/FreedomCats/qwdtt-cli/internal/netguard"

// reversePathProblem — на Windows обратного фильтра, который мешал бы
// привязке IP_UNICAST_IF, нет (Э2). См. fulltunnel_linux.go.
func reversePathProblem(netguard.Choice) (string, error) { return "", nil }

// ipv6ReplyFilter — см. fulltunnel_linux.go.
func ipv6ReplyFilter() (string, error) { return "", nil }
