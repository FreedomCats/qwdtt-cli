package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// maxWorkers повторяет потолок ядра (internal/core/api.go). Дублируется числом,
// потому что в ядре он неэкспортируемый, а сообщение об ошибке должно называть
// границу, а не отсылать к исходникам.
const maxWorkers = 108

// Пресеты резолвера, которые понимает ядро (internal/core/creds.go, doh.go).
// Значение не из этого списка ядро молча заменяет на yandex — здесь такое
// не проходит: молчаливая подмена резолвера означала бы, что запросы к VK
// идут не туда, куда просил пользователь.
var dnsPresets = []string{
	"yandex", "cloudflare", "google",
	"doh-yandex", "doh-cloudflare", "doh-google",
}

// Validate проверяет файл целиком. Вызывается при разборе; отдельно нужен тем,
// кто собрал File в памяти — например, `tt profile set` перед записью.
func (f *File) Validate() error {
	problems := validateFile(f)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("настройки:\n  %s", strings.Join(problems, "\n  "))
}

// Validate проверяет один профиль — тот же набор правил, что и у файла.
// Нужен `tt connect`: флаги командной строки перекрывают поля профиля, и
// собранное из двух источников значение обязано пройти ту же проверку, что
// и записанное в файл.
func (p *Profile) Validate() error {
	problems := profileProblems(p)
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("настройки профиля %q:\n  %s", p.Name, strings.Join(problems, "\n  "))
}

// validateFile собирает все претензии разом.
//
// Именно все, а не первую попавшуюся: человек правит файл в редакторе, и
// показать ему сразу весь список ошибок дешевле, чем гонять по кругу
// «исправил — запустил — снова ошибка».
func validateFile(f *File) []string {
	var problems []string

	names := f.Names()
	for _, name := range names {
		if bad := profileNameProblem(name); bad != "" {
			problems = append(problems, bad)
		}
	}

	if def := strings.TrimSpace(f.DefaultProfile); def != "" {
		if _, ok := f.Profiles[def]; !ok {
			if len(names) == 0 {
				problems = append(problems, fmt.Sprintf(
					"default_profile = %q: такого профиля нет, в файле нет ни одного профиля", def))
			} else {
				problems = append(problems, fmt.Sprintf(
					"default_profile = %q: такого профиля нет; в файле есть: %s", def, strings.Join(names, ", ")))
			}
		}
	}

	for _, name := range names {
		for _, problem := range profileProblems(f.Profiles[name]) {
			problems = append(problems, fmt.Sprintf("профиль %q: %s", name, problem))
		}
	}

	return problems
}

// profileNameProblem бережёт имя профиля от того, что тяжело набрать в командной
// строке: пробелов, кавычек, кириллицы в неожиданной раскладке. Имя — это
// аргумент `tt connect <имя>`, а не описание.
func profileNameProblem(name string) string {
	if name == "" {
		return "пустое имя профиля: секция называется [profile.<имя>]"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Sprintf(
				"имя профиля %q: допустимы латинские буквы, цифры, дефис и подчёркивание", name)
		}
	}
	return ""
}

func profileProblems(p *Profile) []string {
	var problems []string

	switch {
	case strings.TrimSpace(p.Peer) == "":
		problems = append(problems, "не задан peer: адрес сервера в виде host:port")
	default:
		if bad := hostPortProblem("peer", p.Peer); bad != "" {
			problems = append(problems, bad)
		}
	}

	problems = append(problems, secretRefProblems("password_ref", p.PasswordRef,
		"пароль подключения")...)
	problems = append(problems, secretRefProblems("vk_hashes_ref", p.VkHashesRef,
		"хеши звонков VK")...)

	switch {
	case p.Workers < 0:
		problems = append(problems, fmt.Sprintf("workers = %d: число воркеров не может быть отрицательным", p.Workers))
	case p.Workers > maxWorkers:
		problems = append(problems, fmt.Sprintf("workers = %d: больше %d воркеров не бывает", p.Workers, maxWorkers))
	}

	if bad := oneOfProblem("obfs", p.Obfs, "audio", "video"); bad != "" {
		problems = append(problems, bad)
	}
	if bad := oneOfProblem("turn_transport", p.TurnTransport, "udp", "tcp"); bad != "" {
		problems = append(problems, bad)
	}
	if bad := dnsProblem(p.DNS); bad != "" {
		problems = append(problems, bad)
	}
	if bad := datapathProblem(p.Datapath); bad != "" {
		problems = append(problems, bad)
	}

	if strings.TrimSpace(p.Socks.Listen) != "" {
		if bad := hostPortProblem("socks.listen", p.Socks.Listen); bad != "" {
			problems = append(problems, bad)
		}
	}

	return problems
}

// secretRefProblems проверяет ссылку на секрет, не заглядывая в хранилище:
// какие бывают хранилища, знает задача C2, а здесь важна форма — <хранилище>:
// <идентификатор>. Форма и ловит главную ошибку: пароль, написанный прямо
// в файле, ссылкой не выглядит.
func secretRefProblems(key, ref, what string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return []string{fmt.Sprintf(
			"не задан %s: ссылка на %s; секреты в файле не хранятся, ссылку записывает `tt profile set`", key, what)}
	}

	scheme, id, found := strings.Cut(ref, ":")
	if !found || strings.TrimSpace(scheme) == "" || strings.TrimSpace(id) == "" {
		return []string{fmt.Sprintf(
			"%s = %q: ожидается ссылка вида <хранилище>:<идентификатор>, например dpapi:8f2a1c; "+
				"сам %s в файле не хранится", key, ref, what)}
	}
	return nil
}

func oneOfProblem(key, value string, allowed ...string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, ok := range allowed {
		if value == ok {
			return ""
		}
	}
	return fmt.Sprintf("%s = %q: допустимы %s", key, value, quotedList(allowed))
}

func datapathProblem(value string) string {
	switch strings.TrimSpace(value) {
	case "", DatapathSocks, DatapathExternal:
		return ""
	case DatapathWintun:
		return fmt.Sprintf(
			"datapath = %q: свой адаптер появится в Э2; сейчас доступны %q (без прав администратора) и %q",
			DatapathWintun, DatapathSocks, DatapathExternal)
	default:
		return fmt.Sprintf("datapath = %q: допустимы %s",
			value, quotedList([]string{DatapathSocks, DatapathExternal}))
	}
}

// dnsProblem разбирает значение так же, как ядро: сначала пресеты, затем две
// приставки — custom: со списком IP и doh: со списком https-адресов.
func dnsProblem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	for _, preset := range dnsPresets {
		if strings.EqualFold(value, preset) {
			return ""
		}
	}

	if rest, ok := strings.CutPrefix(value, "custom:"); ok {
		return customDNSProblem(value, rest)
	}
	if rest, ok := strings.CutPrefix(value, "doh:"); ok {
		return dohProblem(value, rest)
	}

	return fmt.Sprintf("dns = %q: допустимы %s, а также custom:<IP> и doh:<https-адрес>",
		value, quotedList(dnsPresets))
}

func customDNSProblem(whole, list string) string {
	addrs := splitList(list)
	if len(addrs) == 0 {
		return fmt.Sprintf("dns = %q: после custom: нужен хотя бы один IP-адрес, например custom:192.168.1.1", whole)
	}
	for _, addr := range addrs {
		host := addr
		// Порт необязателен: ядро само подставляет :53, если его нет.
		if h, _, err := net.SplitHostPort(addr); err == nil {
			host = h
		}
		if net.ParseIP(host) == nil {
			return fmt.Sprintf(
				"dns = %q: %q — не IP-адрес; custom: принимает адреса, а не имена, потому что резолвить их было бы нечем",
				whole, addr)
		}
	}
	return ""
}

func dohProblem(whole, list string) string {
	endpoints := splitList(list)
	if len(endpoints) == 0 {
		return fmt.Sprintf(
			"dns = %q: после doh: нужен хотя бы один адрес, например doh:https://dns.example/dns-query", whole)
	}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Sprintf("dns = %q: %q — не https-адрес; DoH ходит только по https", whole, endpoint)
		}
	}
	return ""
}

// hostPortProblem проверяет адрес так, как его прочитает ядро: host:port,
// порт обязателен и должен быть числом в допустимом диапазоне.
func hostPortProblem(key, value string) string {
	value = strings.TrimSpace(value)

	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Sprintf("%s = %q: адрес должен быть в виде host:port, например 203.0.113.10:56000", key, value)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Sprintf("%s = %q: не задан адрес до двоеточия", key, value)
	}

	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Sprintf("%s = %q: порт %q должен быть числом от 1 до 65535", key, value, port)
	}
	return ""
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}
