// Command tt — консольный клиент qWDTT для настольных систем.
//
// Разбор команд появится в задаче G1 (см. doc/plan-e1.md).
package main

import "fmt"

// version и commit подставляются при сборке через -ldflags (задача H1).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	fmt.Printf("tt %s (%s)\n", version, commit)
}
