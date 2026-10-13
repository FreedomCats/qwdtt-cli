package datapath

// lockNeedsPrivilege — абстрактный сокет прав не требует.
func lockNeedsPrivilege(error) bool { return false }
