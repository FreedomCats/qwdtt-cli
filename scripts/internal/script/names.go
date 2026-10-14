package script

import (
	"slices"
	"strings"
	"unicode"
)

// SortNames сортирует имена файлов так, как их сортировал Sort-Object в
// pwsh: по правилам языка, а не по байтам. Регистр — только при равенстве
// остального (строчная раньше), знаки — раньше цифр, цифры — раньше букв,
// а среди знаков «_» раньше «-», «-» раньше «.» (listen_unix.go раньше
// listen.go, creds_vkcalls.go раньше creds.go). Порядок сверен с pwsh 7.4,
// сортирующим по ICU; на Windows у .NET своя таблица (NLS), с ней порядок
// не сверялся.
func SortNames(names []string) {
	slices.SortStableFunc(names, CompareNames)
}

// punctuation — порядок знаков в сортировке ICU по умолчанию.
const punctuation = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

// CompareNames — порядок SortNames: отрицательное, если a раньше b.
func CompareNames(a, b string) int {
	if c := slices.Compare(primary(a), primary(b)); c != 0 {
		return c
	}
	// Различаются только регистром: строчная раньше заглавной.
	ra, rb := []rune(a), []rune(b)
	for i := range min(len(ra), len(rb)) {
		la, lb := unicode.IsLower(ra[i]), unicode.IsLower(rb[i])
		if la != lb {
			if la {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

// primary — вес каждой буквы без учёта регистра.
func primary(s string) []int {
	w := make([]int, 0, len(s))
	for _, r := range s {
		switch {
		case strings.ContainsRune(punctuation, r):
			w = append(w, 1+strings.IndexRune(punctuation, r))
		case unicode.IsDigit(r):
			w = append(w, 1000+int(r))
		default:
			w = append(w, 1<<20+int(unicode.ToLower(r)))
		}
	}
	return w
}
