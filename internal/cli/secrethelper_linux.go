package cli

import (
	"os"

	"github.com/FreedomCats/qwdtt-cli/internal/secretservice"
)

// runSecretHelper выполняет служебную команду связки ключей, если args — она:
// так qwdtt-cli под sudo читает и пишет секреты в связке того, кто его вызвал
// (internal/secretservice). ok — команда была эта.
func runSecretHelper(args []string) (code int, ok bool) {
	if len(args) != 1 || args[0] != secretservice.HelperCommand {
		return 0, false
	}
	return secretservice.RunHelper(os.Stdin, os.Stdout, os.Stderr), true
}
