package config

// DefaultDatapath — режим профиля без поля datapath. На macOS tun и external
// не поддерживаются, работает только socks — он и умолчание. На остальных
// системах — tun (datapath_default.go).
const DefaultDatapath = DatapathSocks
