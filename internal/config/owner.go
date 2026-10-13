package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// Под sudo tt пишет в каталог настроек того, кто его вызвал (см. configDir).
// Файлы и каталоги, созданные root'ом, остались бы root'у: без sudo человек их
// потом не прочтёт и не перепишет. Поэтому всё, что tt создаёт под sudo,
// отдаётся вызвавшему.
//
// Обе переменные подменяются в тестах: настоящий chown требует root.
var (
	invoker = sudo.Invoker
	chown   = os.Chown
)

// makeDir создаёт каталог со всеми недостающими родителями, как MkdirAll, и
// под sudo отдаёт вызвавшему каждый созданный — включая, например, ~/.config,
// если его не было. Уже существовавшие каталоги не трогаются.
func makeDir(dir string) error {
	owner, err := invoker()
	if err != nil {
		return err
	}

	var created []string
	if owner != nil {
		for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
			if _, err := os.Lstat(d); !errors.Is(err, fs.ErrNotExist) {
				break
			}
			created = append(created, d)
			if parent := filepath.Dir(d); parent == d {
				break
			}
		}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог настроек %s: %w", dir, err)
	}

	// Сверху вниз: так каталог отдаётся раньше, чем то, что в нём.
	for i := len(created) - 1; i >= 0; i-- {
		if err := giveTo(owner, created[i]); err != nil {
			return fmt.Errorf("каталог настроек %s: %w", created[i], err)
		}
	}
	return nil
}

// giveToInvoker отдаёт файл вызвавшему sudo; без sudo ничего не делает.
// Зовётся для временного файла до Rename: после переименования тот же файл
// уже на месте настоящего, и окна, когда он принадлежит root, нет.
func giveToInvoker(path string) error {
	owner, err := invoker()
	if err != nil {
		return err
	}
	return giveTo(owner, path)
}

func giveTo(owner *sudo.User, path string) error {
	if owner == nil {
		return nil
	}
	if err := chown(path, int(owner.Uid), int(owner.Gid)); err != nil {
		return fmt.Errorf("не удалось отдать файл пользователю %s: %w", owner.Name, err)
	}
	return nil
}
