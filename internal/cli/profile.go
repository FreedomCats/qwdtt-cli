// profile.go — команда tt profile: профили подключения и их секреты.
//
// Четыре подкоманды со своими флагами, поэтому у самой profile флагов нет:
// они ставятся после подкоманды, как в `tt profile set home -peer …`.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"qwdtt-cli/internal/config"
	"qwdtt-cli/internal/core"
	"qwdtt-cli/internal/datapath"
)

func profileCommand() *command {
	return &command{
		name:    "profile",
		args:    "<подкоманда> [имя] [флаги]",
		summary: "профили подключения: list, show, set, rm",
		description: `Подкоманды:
  list        перечислить профили из profiles.toml
  show <имя>  показать настройки профиля; секреты — замаскированными
  set <имя>   создать или изменить профиль; пароль и хеши читаются скрытым вводом
  rm <имя>    удалить профиль вместе с его секретами

Флаги у каждой подкоманды свои: tt profile set -h.

Пароль подключения и хеши звонков VK не передаются аргументами намеренно:
аргумент попал бы в историю оболочки и в список процессов. Спрашиваются они
скрытым вводом, а в profiles.toml уходит только ссылка на хранилище секретов.`,
		run: runProfile,
	}
}

// profileSubcommands — подкоманды в порядке помощи. Имя у каждой полное
// («profile set»): им подписываются ошибки и по нему же ищется подкоманда.
func profileSubcommands() []*command {
	return []*command{
		profileListCommand(),
		profileShowCommand(),
		profileSetCommand(),
		profileRmCommand(),
	}
}

func profileSubNames() string {
	subs := profileSubcommands()
	names := make([]string, 0, len(subs))
	for _, sub := range subs {
		names = append(names, strings.TrimPrefix(sub.name, "profile "))
	}
	return strings.Join(names, ", ")
}

func runProfile(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return usagef("не названа подкоманда; бывают: %s", profileSubNames())
	}

	if isHelpFlag(args[0]) {
		fs := newFlagSet(profileCommand())
		fs.SetOutput(e.stdout)
		fs.Usage()
		return flag.ErrHelp
	}
	if strings.HasPrefix(args[0], "-") {
		// Своих флагов у profile нет, и «flag provided but not defined»
		// на месте подкоманды объясняло бы не то, что произошло.
		return usagef("флаги ставятся после подкоманды: tt profile <подкоманда> [флаги]")
	}

	for _, sub := range profileSubcommands() {
		if sub.name == "profile "+args[0] {
			return sub.run(ctx, e, args[1:])
		}
	}
	return usagef("неизвестная подкоманда %q; бывают: %s", args[0], profileSubNames())
}

// --- profile list ---

func profileListCommand() *command {
	return &command{
		name:    "profile list",
		args:    "[флаги]",
		summary: "перечислить профили из profiles.toml",
		run:     runProfileList,
	}
}

func runProfileList(_ context.Context, e *env, args []string) error {
	cmd := profileListCommand()
	fs := newFlagSet(cmd)
	configPath := addConfigFlag(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return subUsagef(cmd, "лишний аргумент %q: list аргументов не принимает", rest[0])
	}

	path, file, err := loadProfiles(*configPath)
	if err != nil {
		return err
	}

	names := file.Names()
	if len(names) == 0 {
		fmt.Fprintf(e.stdout, "В %s нет ни одного профиля. Заведите: tt profile set <имя>\n", path)
		return nil
	}

	width := 0
	for _, name := range names {
		if len(name) > width {
			width = len(name)
		}
	}

	marked := false
	for _, name := range names {
		p := file.Profiles[name]
		marker := " "
		if isDefaultProfile(file, name) {
			marker = "*"
			marked = true
		}
		fmt.Fprintf(e.stdout, "%s %-*s  %-22s %s\n",
			marker, width, name, p.Peer, effective(p.Datapath, config.DatapathSocks))
	}
	if marked {
		fmt.Fprintln(e.stdout, "\n* — профиль по умолчанию")
	}
	return nil
}

// isDefaultProfile повторяет правило File.Profile: сначала default_profile,
// а если его нет — единственный профиль в файле. Список обязан показывать то,
// что произойдёт при `tt connect` без имени.
func isDefaultProfile(f *config.File, name string) bool {
	if def := strings.TrimSpace(f.DefaultProfile); def != "" {
		return def == name
	}
	return len(f.Profiles) == 1
}

// --- profile show ---

func profileShowCommand() *command {
	return &command{
		name:    "profile show",
		args:    "<имя> [флаги]",
		summary: "показать настройки профиля; секреты — замаскированными",
		description: `Значения, которых в профиле нет, показаны с пометкой «умолчание» — это то,
с чем профиль будет работать.

Пароль и хеши не печатаются: показывается только ссылка на хранилище и то,
читается ли по ней значение на этой машине.`,
		run: runProfileShow,
	}
}

func runProfileShow(_ context.Context, e *env, args []string) error {
	cmd := profileShowCommand()
	fs := newFlagSet(cmd)
	configPath := addConfigFlag(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	name, err := profileNameArg(cmd, rest)
	if err != nil {
		return err
	}

	path, file, err := loadProfiles(*configPath)
	if err != nil {
		return err
	}
	p, err := file.Profile(name)
	if err != nil {
		return err
	}

	def := core.DefaultConfig()
	store := config.NewSecretStoreFor(path)

	fmt.Fprintf(e.stdout, "профиль %s", p.Name)
	if isDefaultProfile(file, p.Name) {
		fmt.Fprint(e.stdout, " (по умолчанию)")
	}
	fmt.Fprintf(e.stdout, "\n%s\n\n", path)

	printField(e.stdout, "peer", p.Peer, "")
	printSetting(e.stdout, "datapath", p.Datapath, config.DatapathSocks)
	printSetting(e.stdout, "workers", numberOrEmpty(p.Workers), fmt.Sprint(def.Workers))
	printSetting(e.stdout, "obfs", p.Obfs, def.ObfsMode)
	printSetting(e.stdout, "turn_transport", p.TurnTransport, "udp")
	if p.DTLS == nil {
		printSetting(e.stdout, "dtls", "", "true")
	} else {
		printSetting(e.stdout, "dtls", fmt.Sprint(*p.DTLS), "")
	}
	printSetting(e.stdout, "dns", p.DNS, def.DNS)
	printSetting(e.stdout, "socks.listen", p.Socks.Listen, datapath.DefaultSocksListen)

	fmt.Fprintln(e.stdout)
	printField(e.stdout, "password", mask, secretNote(store, p.PasswordRef, false))
	printField(e.stdout, "vk_hashes", mask, secretNote(store, p.VkHashesRef, true))
	return nil
}

// secretNote описывает секрет, не показывая его: откуда он берётся, читается ли
// на этой машине и сколько в нём хешей. Число хешей не секрет, а самая частая
// причина заглянуть в профиль.
func secretNote(store config.SecretStore, ref string, hashes bool) string {
	if strings.TrimSpace(ref) == "" {
		return "(не задан)"
	}

	value, err := store.Get(ref)
	if err != nil {
		return fmt.Sprintf("(%s, не читается: %v)", ref, err)
	}
	if !hashes {
		return fmt.Sprintf("(%s, прочитан)", ref)
	}
	return fmt.Sprintf("(%s, прочитан, хешей: %d)", ref, len(core.ParseHashes(value)))
}

// --- profile set ---

func profileSetCommand() *command {
	return &command{
		name:    "profile set",
		args:    "<имя> [флаги]",
		summary: "создать или изменить профиль",
		description: `Флагом меняется одно поле профиля, остальные остаются как были. Новый
профиль заводится с адресом сервера (-peer) и сразу спрашивает пароль и хеши.

У существующего профиля пароль и хеши перезаписываются по -password и -hashes.
Значение спрашивается скрытым вводом; аргументом его передать нельзя, чтобы
секрет не попал в историю оболочки и в список процессов.

Файл настроек переписывается целиком: комментарии, если они там были,
не сохранятся.`,
		run: runProfileSet,
	}
}

func runProfileSet(_ context.Context, e *env, args []string) error {
	cmd := profileSetCommand()
	fs := newFlagSet(cmd)

	configPath := addConfigFlag(fs)
	peer := fs.String("peer", "", "адрес сервера в виде host:port")
	workers := fs.Int("workers", 0, "число воркеров: кратно девяти, потолок 108")
	obfs := fs.String("obfs", "", "режим RTP-маскировки: audio или video")
	turnTransport := fs.String("turn-transport", "", "транспорт до TURN-relay: udp или tcp")
	dtls := fs.Bool("dtls", true, "поднимать DTLS поверх TURN; -dtls=false — прямой RTP-obfs AEAD")
	dns := fs.String("dns", "", "резолвер для доменов VK: yandex, cloudflare, google, doh-*, custom:IP, doh:URL")
	datapath := fs.String("datapath", "", "как отдавать трафик: socks или external")
	socksListen := fs.String("socks-listen", "", "адрес локального SOCKS5 при datapath=socks")
	askPassword := fs.Bool("password", false, "спросить пароль подключения заново")
	askHashes := fs.Bool("hashes", false, "спросить хеши звонков VK заново")
	makeDefault := fs.Bool("default", false, "сделать профиль профилем по умолчанию")

	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	name, err := profileNameArg(cmd, rest)
	if err != nil {
		return err
	}
	named := setFlags(fs)

	path, file, err := loadProfilesForWrite(*configPath)
	if err != nil {
		return err
	}
	if file.Profiles == nil {
		file.Profiles = map[string]*config.Profile{}
	}

	p, existed := file.Profiles[name]
	if !existed {
		p = &config.Profile{Name: name}
	}

	if named["peer"] {
		p.Peer = *peer
	}
	if named["workers"] {
		p.Workers = *workers
	}
	if named["obfs"] {
		p.Obfs = *obfs
	}
	if named["turn-transport"] {
		p.TurnTransport = *turnTransport
	}
	if named["dtls"] {
		value := *dtls
		p.DTLS = &value
	}
	if named["dns"] {
		p.DNS = *dns
	}
	if named["datapath"] {
		p.Datapath = *datapath
	}
	if named["socks-listen"] {
		p.Socks.Listen = *socksListen
	}

	if !existed && strings.TrimSpace(p.Peer) == "" {
		return subUsagef(cmd, "новый профиль заводится с адресом сервера: добавьте -peer host:port")
	}
	if err := checkBeforeAsking(name, p); err != nil {
		return err
	}

	store := config.NewSecretStoreFor(path)
	wantPassword := *askPassword || strings.TrimSpace(p.PasswordRef) == ""
	wantHashes := *askHashes || strings.TrimSpace(p.VkHashesRef) == ""

	if wantPassword {
		value, err := e.askNewSecret(
			fmt.Sprintf("Пароль подключения для профиля %q: ", name),
			"Повторите пароль: ", "пароль подключения", true)
		if err != nil {
			return err
		}
		// Имя записи в хранилище — «<профиль>.password»: у каждого профиля
		// свои секреты, а Put по тому же имени перезаписывает прежнее значение
		// и не плодит осиротевших записей.
		ref, err := store.Put(name+".password", value)
		if err != nil {
			return err
		}
		p.PasswordRef = ref
	}

	if wantHashes {
		value, err := e.askNewSecret(
			fmt.Sprintf("Хеши звонков VK для профиля %q (через запятую или пробел): ", name),
			"", "хеши звонков VK", false)
		if err != nil {
			return err
		}
		if len(core.ParseHashes(value)) == 0 {
			return errors.New("хеши звонков VK: во введённом ни одного хеша не разобрано")
		}
		// Хранится строка ровно как её ввели: режет её на хеши ядро
		// (core.ParseHashes), и второго формата склейки заводить незачем.
		ref, err := store.Put(name+".vk_hashes", value)
		if err != nil {
			return err
		}
		p.VkHashesRef = ref
	}

	file.Profiles[name] = p
	if named["default"] {
		switch {
		case *makeDefault:
			file.DefaultProfile = name
		case file.DefaultProfile == name:
			file.DefaultProfile = ""
		}
	}

	if err := config.Save(path, file); err != nil {
		return err
	}

	verb := "изменён"
	if !existed {
		verb = "создан"
	}
	fmt.Fprintf(e.stdout, "Профиль %q %s: %s\n", name, verb, path)
	if wantPassword || wantHashes {
		fmt.Fprintf(e.stdout, "Секреты записаны в хранилище рядом с ним (%s)\n", config.SecretsFileName)
	}
	if file.DefaultProfile == name {
		fmt.Fprintf(e.stdout, "Профиль по умолчанию: %s\n", name)
	}
	return nil
}

// checkBeforeAsking проверяет настройки до того, как спрашивать секреты:
// ввести пароль дважды и услышать, что -obfs набран с опечаткой, обидно.
//
// Ссылки на секреты подставляются заглушками — их ещё нет, а проверка требует
// непустых. Всё остальное проверяется по-настоящему и теми же сообщениями,
// что и при чтении файла.
func checkBeforeAsking(name string, p *config.Profile) error {
	probe := *p
	if strings.TrimSpace(probe.PasswordRef) == "" {
		probe.PasswordRef = "проверка:подставится-ниже"
	}
	if strings.TrimSpace(probe.VkHashesRef) == "" {
		probe.VkHashesRef = "проверка:подставится-ниже"
	}
	return (&config.File{Profiles: map[string]*config.Profile{name: &probe}}).Validate()
}

// --- profile rm ---

func profileRmCommand() *command {
	return &command{
		name:    "profile rm",
		args:    "<имя> [флаги]",
		summary: "удалить профиль вместе с его секретами",
		description: `Удаление необратимо: пароль и хеши стираются из хранилища, и вернуть их
можно только вводом заново. Поэтому команда спрашивает подтверждение;
-y отвечает за вас.`,
		run: runProfileRm,
	}
}

func runProfileRm(_ context.Context, e *env, args []string) error {
	cmd := profileRmCommand()
	fs := newFlagSet(cmd)
	configPath := addConfigFlag(fs)
	yes := fs.Bool("y", false, "не спрашивать подтверждения")
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	name, err := profileNameArg(cmd, rest)
	if err != nil {
		return err
	}

	path, file, err := loadProfiles(*configPath)
	if err != nil {
		return err
	}
	p, ok := file.Profiles[name]
	if !ok {
		// Сообщение с перечнем имён уже написано в File.Profile.
		_, err := file.Profile(name)
		return err
	}

	if !*yes {
		agreed, err := e.confirm(fmt.Sprintf("Удалить профиль %q и его секреты?", name))
		if err != nil {
			return err
		}
		if !agreed {
			fmt.Fprintln(e.stdout, "Отменено, ничего не изменилось.")
			return nil
		}
	}

	store := config.NewSecretStoreFor(path)
	for _, ref := range []string{p.PasswordRef, p.VkHashesRef} {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		if err := store.Delete(ref); err != nil {
			// Секрет мог остаться от другой платформы или пропасть вручную.
			// Останавливаться нельзя: профиль остался бы удалённым наполовину.
			fmt.Fprintf(e.stderr, "Предупреждение: секрет %s не удалён: %v\n", ref, err)
		}
	}

	delete(file.Profiles, name)
	clearedDefault := false
	if file.DefaultProfile == name {
		file.DefaultProfile = ""
		clearedDefault = true
	}

	if err := config.Save(path, file); err != nil {
		return err
	}

	fmt.Fprintf(e.stdout, "Профиль %q удалён вместе с секретами: %s\n", name, path)
	if clearedDefault {
		fmt.Fprintln(e.stdout, "Он был профилем по умолчанию — теперь умолчания нет.")
	}
	return nil
}

// --- общее ---

func addConfigFlag(fs *flag.FlagSet) *string {
	return fs.String("config", "", "путь к profiles.toml вместо файла настроек по умолчанию")
}

// profileNameArg берёт имя профиля из позиционных аргументов. Оно обязательно
// у всех подкоманд, кроме list: `tt profile rm` без имени не должен угадывать,
// что удалять.
func profileNameArg(cmd *command, positional []string) (string, error) {
	switch len(positional) {
	case 0:
		return "", subUsagef(cmd, "не названо имя профиля: tt %s <имя>", cmd.name)
	case 1:
		return positional[0], nil
	default:
		return "", subUsagef(cmd, "лишний аргумент %q: профиль называется один", positional[1])
	}
}

// profilePath — какой файл настроек читать: названный флагом или тот, что
// полагается платформе.
func profilePath(flagValue string) (string, error) {
	if path := strings.TrimSpace(flagValue); path != "" {
		return path, nil
	}
	return config.Path()
}

// loadProfiles читает файл настроек. Отсутствие файла — ошибка с подсказкой:
// показать пустой список там, где файла нет вовсе, значит скрыть от человека,
// что он смотрит не туда.
func loadProfiles(flagValue string) (string, *config.File, error) {
	path, err := profilePath(flagValue)
	if err != nil {
		return "", nil, err
	}

	file, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("файла настроек нет: %s; заведите профиль: tt profile set <имя>", path)
	}
	if err != nil {
		return "", nil, err
	}
	return path, file, nil
}

// loadProfilesForWrite — то же для `set`, которому отсутствие файла не помеха:
// первый профиль заводится в пустоте.
func loadProfilesForWrite(flagValue string) (string, *config.File, error) {
	path, err := profilePath(flagValue)
	if err != nil {
		return "", nil, err
	}

	file, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, &config.File{Profiles: map[string]*config.Profile{}}, nil
	}
	if err != nil {
		return "", nil, err
	}
	return path, file, nil
}

func printField(w io.Writer, key, value, note string) {
	fmt.Fprintf(w, "  %-14s %s", key, value)
	if note != "" {
		fmt.Fprintf(w, "  %s", note)
	}
	fmt.Fprintln(w)
}

// printSetting печатает настройку и, если в профиле её нет, показывает
// умолчание с пометкой: важно то, с чем профиль будет работать.
func printSetting(w io.Writer, key, value, fallback string) {
	if strings.TrimSpace(value) == "" {
		printField(w, key, fallback, "(умолчание)")
		return
	}
	printField(w, key, value, "")
}

func effective(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func numberOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}
