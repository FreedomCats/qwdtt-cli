// Package config читает profiles.toml — файл с настройками подключения.
//
// Двадцать пять флагов ядра заменяются профилем: набором именованных настроек,
// между которыми переключаются одним словом в командной строке (решение Р6
// в doc/architecture.md, формат — там же, §10).
//
// Секретов в файле нет. Пароль подключения — это одновременно ключ маскировки
// трафика (HKDF из него выводит WRAP-ключ, см. internal/core/wrap.go), поэтому
// он и хеши звонков лежат в хранилище секретов, а в профиле остаются только
// ссылки вида "dpapi:…". Разворачивает их задача C2; здесь ссылка — обычная
// строка, и её содержимое пакету неизвестно.
package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Имя каталога и файла настроек. Каталог общий для всей программы: рядом
// с profiles.toml задача C2 положит хранилище секретов.
const (
	AppDir   = "qwdtt-cli"
	FileName = "profiles.toml"
)

// Способы отдать трафик наружу. Значения — те же слова, что в architecture.md §07.
const (
	// DatapathSocks — userspace WireGuard и локальный SOCKS5. Прав администратора
	// не требует; по умолчанию — только на macOS, где других режимов нет
	// (DefaultDatapath).
	DatapathSocks = "socks"
	// DatapathExternal — конфиг отдаётся штатному WireGuard, тот поднимает
	// туннель сам. Нужны права администратора и установленный WireGuard.
	DatapathExternal = "external"
	// DatapathTun — свой адаптер tt, весь трафик системы (Э2). На Windows
	// адаптер даёт драйвер Wintun: нужны права администратора и wintun.dll
	// рядом с tt.
	DatapathTun = "tun"
	// DatapathWintun — прежнее имя tun (до Э4); принимается как синоним,
	// чтобы старые профили не ломались. См. NormalizeDatapath.
	DatapathWintun = "wintun"
)

// NormalizeDatapath приводит значение datapath к каноническому: без пробелов
// по краям, синоним wintun — к tun. Остальное не трогает: неизвестные
// значения отвергает Validate.
func NormalizeDatapath(value string) string {
	value = strings.TrimSpace(value)
	if value == DatapathWintun {
		return DatapathTun
	}
	return value
}

// EffectiveDatapath — каким режимом профиль подключится: значение поля после
// NormalizeDatapath, а пустое — DefaultDatapath. Умолчание читается только
// здесь, чтобы выбор реализации, проверка прав и показ профиля не разошлись.
func EffectiveDatapath(value string) string {
	if value = NormalizeDatapath(value); value == "" {
		return DefaultDatapath
	}
	return value
}

// File — разобранный profiles.toml целиком.
type File struct {
	// DefaultProfile — какой профиль брать, если в командной строке имя
	// не названо. Может быть пустым: когда профиль в файле один, выбирать
	// не из чего.
	DefaultProfile string `toml:"default_profile"`
	// Profiles — секции [profile.<имя>].
	Profiles map[string]*Profile `toml:"profile"`
}

// Profile — одна секция [profile.<имя>].
//
// Пустое поле означает «не задано»: значение по умолчанию подставляется
// в Core, и берётся оно из core.DefaultConfig, чтобы умолчания не разъезжались
// с ядром. Исключение — datapath, см. Core.
type Profile struct {
	// Name — имя секции. Заполняется при разборе, в файле его нет.
	Name string `toml:"-"`

	// Peer — адрес VPS в виде host:port.
	Peer string `toml:"peer"`
	// PasswordRef — ссылка на пароль подключения в хранилище секретов.
	PasswordRef string `toml:"password_ref"`
	// VkHashesRef — ссылка на список хешей звонков VK.
	VkHashesRef string `toml:"vk_hashes_ref"`

	// Workers — число воркеров. Кратно девяти, потолок 108; округляет
	// и обрезает ядро (core.Config.Normalize), здесь значение не трогают.
	Workers int `toml:"workers"`
	// Obfs — режим RTP-маскировки: audio или video.
	Obfs string `toml:"obfs"`
	// TurnTransport — udp или tcp. TCP помогает в сетях, где душат UDP.
	TurnTransport string `toml:"turn_transport"`
	// DTLS — поднимать ли DTLS поверх TURN. Указатель, потому что «не задано»
	// и «задано false» здесь разные вещи: пропущенное поле означает обычный
	// режим с DTLS, а false — прямой RTP-obfs AEAD, который требует сервера,
	// запущенного с -listen-direct.
	DTLS *bool `toml:"dtls"`
	// DNS — резолвер для доменов VK.
	DNS string `toml:"dns"`
	// Datapath — как отдавать трафик: socks, tun или external.
	Datapath string `toml:"datapath"`
	// KillSwitch — блокировать ли трафик системы, пока туннель не поднят
	// (tun и external, plan-e3.md). Указатель: пропущенное поле значит
	// «включён», выключает только явное false. Читать — KillSwitchOn.
	KillSwitch *bool `toml:"kill_switch"`

	// Socks — секция [profile.<имя>.socks].
	Socks SocksSection `toml:"socks"`
}

// SocksSection — настройки локального SOCKS5. Учитываются при datapath = "socks".
//
// Логина и пароля здесь нет намеренно: SOCKS5 слушает петлю, а пароль к нему
// был бы вторым секретом в файле. Появятся вместе с задачей, которой они
// понадобятся.
type SocksSection struct {
	Listen string `toml:"listen"`
}

// Core переводит профиль в настройки ядра.
//
// Секреты не подставляются: Password и Hashes остаются пустыми, их заполняет
// вызывающий, развернув PasswordRef и VkHashesRef через хранилище (задача C2).
// Поэтому core.Config.Validate после Core ещё не проходит — и не должен.
//
// Умолчания берутся из core.DefaultConfig целиком.
//
// Полей datapath здесь нет: ядру всё равно, кто заберёт у него конфиг. Выбор
// реализации (Profile.Datapath, умолчание — EffectiveDatapath) и её
// настройки (Profile.Socks) читает CLI и передаёт
// конструктору из internal/datapath, минуя ядро (задача D2).
func (p *Profile) Core() core.Config {
	cfg := core.DefaultConfig()

	cfg.Peer = p.Peer

	if p.Workers != 0 {
		cfg.Workers = p.Workers
	}
	if p.Obfs != "" {
		cfg.ObfsMode = p.Obfs
	}
	if p.DNS != "" {
		cfg.DNS = p.DNS
	}
	cfg.TurnTCP = strings.EqualFold(strings.TrimSpace(p.TurnTransport), "tcp")
	cfg.NoDTLS = p.DTLS != nil && !*p.DTLS

	return cfg
}

// KillSwitchOn — включён ли kill-switch: да, если в профиле не сказано
// обратного.
func (p *Profile) KillSwitchOn() bool {
	return p.KillSwitch == nil || *p.KillSwitch
}

// Names возвращает имена профилей по алфавиту — для выбора и для сообщений
// об ошибках, где порядок обязан быть устойчивым.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Profiles))
	for name := range f.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Profile возвращает профиль по имени. Пустое имя означает «профиль
// по умолчанию»: сначала default_profile, а если его нет — единственный
// профиль в файле.
//
// Сообщения об ошибках перечисляют, что в файле есть: чаще всего промах
// с именем — это опечатка, и список избавляет от похода в файл.
func (f *File) Profile(name string) (*Profile, error) {
	name = strings.TrimSpace(name)

	if name == "" {
		switch {
		case f.DefaultProfile != "":
			name = f.DefaultProfile
		case len(f.Profiles) == 1:
			name = f.Names()[0]
		case len(f.Profiles) == 0:
			return nil, fmt.Errorf("в файле настроек нет ни одного профиля")
		default:
			return nil, fmt.Errorf(
				"не выбран профиль: в файле их %d (%s), а default_profile не задан",
				len(f.Profiles), strings.Join(f.Names(), ", "))
		}
	}

	profile, ok := f.Profiles[name]
	if !ok {
		if len(f.Profiles) == 0 {
			return nil, fmt.Errorf("профиля %q нет: в файле настроек нет ни одного профиля", name)
		}
		return nil, fmt.Errorf("профиля %q нет; в файле есть: %s", name, strings.Join(f.Names(), ", "))
	}
	return profile, nil
}
