//go:build linux

package secretservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// HelperCommand — скрытая команда qwdtt-cli: одна операция со связкой ключей
// от имени того, кто вызвал sudo. Запрос — JSON в stdin, ответ — JSON в
// stdout: значения секретов не попадают в аргументы, которые видит ps.
// Вызывать её руками незачем, но и вреда нет: процесс получит только связку
// своего же пользователя.
const HelperCommand = "__secret-service"

// Open подключается к связке ключей. Под sudo — через HelperCommand от имени
// вызвавшего, иначе — напрямую. notify — куда сказать про окно разблокировки.
func Open(notify io.Writer) (Keyring, error) {
	u, err := sudo.Invoker()
	if err != nil {
		return nil, err
	}
	if u != nil {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("связка ключей: не найти исполняемый файл qwdtt-cli: %w", err)
		}
		return &helper{user: u, exe: exe, stderr: notify}, nil
	}
	return dial(os.Getenv, notify)
}

type request struct {
	Op    string `json:"op"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
}

type response struct {
	Value       string `json:"value,omitempty"`
	Err         string `json:"err,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
	NotFound    bool   `json:"not_found,omitempty"`
}

// RunHelper выполняет HelperCommand: читает запрос, делает операцию со связкой
// своего сеанса и пишет ответ. Код возврата ненулевой, только если ответа нет
// вовсе; ошибка самой операции едет в ответе.
func RunHelper(stdin io.Reader, stdout, stderr io.Writer) int {
	var req request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		fmt.Fprintf(stderr, "%s: запрос: %v\n", HelperCommand, err)
		return 2
	}
	return answer(req, stdout, stderr, func() (Keyring, error) { return dial(os.Getenv, stderr) })
}

func answer(req request, stdout, stderr io.Writer, connect func() (Keyring, error)) int {
	resp := serve(req, connect)
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		fmt.Fprintf(stderr, "%s: ответ: %v\n", HelperCommand, err)
		return 1
	}
	return 0
}

// serve — одна операция запроса над связкой, которую даёт connect.
func serve(req request, connect func() (Keyring, error)) response {
	k, err := connect()
	if err != nil {
		return failure(err)
	}
	defer k.Close()

	switch req.Op {
	case "put":
		err = k.Put(req.ID, req.Label, req.Value)
	case "get":
		var value string
		if value, err = k.Get(req.ID); err == nil {
			return response{Value: value}
		}
	case "delete":
		err = k.Delete(req.ID)
	default:
		err = fmt.Errorf("неизвестная операция %q", req.Op)
	}
	if err != nil {
		return failure(err)
	}
	return response{}
}

func failure(err error) response {
	return response{
		Err:         err.Error(),
		Unavailable: errors.Is(err, ErrUnavailable),
		NotFound:    errors.Is(err, ErrNotFound),
	}
}

// helper — связка ключей того, кто вызвал sudo: каждая операция — свой запуск
// HelperCommand от его имени, с окружением его сеанса.
type helper struct {
	user   *sudo.User
	exe    string
	stderr io.Writer
	// run запускает команду; подменяется в тестах.
	run func(*exec.Cmd) error
}

func (h *helper) Put(id, label, value string) error {
	_, err := h.call(request{Op: "put", ID: id, Label: label, Value: value})
	return err
}

func (h *helper) Get(id string) (string, error) {
	resp, err := h.call(request{Op: "get", ID: id})
	return resp.Value, err
}

func (h *helper) Delete(id string) error {
	_, err := h.call(request{Op: "delete", ID: id})
	return err
}

func (h *helper) Close() error { return nil }

func (h *helper) call(req request) (response, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return response{}, err
	}
	var out bytes.Buffer
	cmd := exec.Command(h.exe, HelperCommand)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: h.user.Uid, Gid: h.user.Gid, Groups: h.user.Groups},
	}
	cmd.Env = sudo.SessionEnv(os.Environ(), h.user)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &out, h.stderr

	run := h.run
	if run == nil {
		run = (*exec.Cmd).Run
	}
	if err := run(cmd); err != nil {
		return response{}, fmt.Errorf("связка ключей от имени %s: %w", h.user.Name, err)
	}

	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return response{}, fmt.Errorf("связка ключей от имени %s: ответ: %w", h.user.Name, err)
	}
	switch {
	case resp.Unavailable:
		return resp, fmt.Errorf("пользователь %s: %w", h.user.Name, remoteError{resp.Err, ErrUnavailable})
	case resp.NotFound:
		return resp, ErrNotFound
	case resp.Err != "":
		return resp, errors.New(resp.Err)
	}
	return resp, nil
}

// remoteError — ошибка из ответа HelperCommand: текст оттуда, а errors.Is
// узнаёт в ней ту же ошибку пакета, что была на той стороне.
type remoteError struct {
	msg string
	is  error
}

func (e remoteError) Error() string { return e.msg }
func (e remoteError) Unwrap() error { return e.is }
