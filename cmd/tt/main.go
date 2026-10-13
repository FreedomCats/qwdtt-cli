// Command tt — консольный клиент qWDTT для настольных систем.
//
// Сама программа — тонкая: разбор команд и всё, что за ними стоит, живёт
// в internal/cli. Здесь остаются только сведения о сборке и код возврата,
// потому что os.Exit из библиотеки звать нельзя — отложенные вызовы тогда
// не отработают.
package main

import (
	"os"

	"github.com/FreedomCats/qwdtt-cli/internal/cli"
)

// version и commit подставляются при сборке через -ldflags (задача H1).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Build{Version: version, Commit: commit}))
}
