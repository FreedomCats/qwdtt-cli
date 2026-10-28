//go:build windows

// webview2_devtools_windows.go — вызов методов DevTools у окна WebView2.
//
// Автоклику нужен доверенный клик: событие мыши, которое страница получает
// с isTrusted=true, а с ним — траектория курсора, которую страница капчи
// записывает в check. Изнутри страницы такого не сделать, а у WebView2 есть
// ICoreWebView2::CallDevToolsProtocolMethod: тот же протокол, что у
// браузера по CDP на Linux (Input.dispatchMouseEvent, Runtime.evaluate).
//
// В go-webview2 обёртки этого метода нет, и сам ICoreWebView2 библиотека
// наружу не отдаёт: он лежит в неэкспортируемом поле окна. Поэтому:
//
//   - объект edge.Chromium достаётся из окна отражением (chromiumOf). Версия
//     библиотеки закреплена в go.mod (replace на форк), поле проверяется по
//     имени и типу: не нашлось — автоклика нет, окно сразу показывается
//     человеку, как раньше;
//   - сам ICoreWebView2 даёт экспортируемый Chromium.GetICoreWebView2_3, а
//     метод зовётся по своему месту в таблице ICoreWebView2 (devtoolsSlot);
//   - обработчик завершения — свой COM-объект: таблица из четырёх обратных
//     вызовов, одна на процесс (у windows.NewCallback предел числа вызовов),
//     и по объекту на вызов, прибитому runtime.Pinner, пока WebView2 его
//     держит.
//
// Всё это — только на нити окна: и вызов, и ответ (WebView2 зовёт обработчик
// из насоса сообщений окна).
package captcha

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// devtoolsSlot — место CallDevToolsProtocolMethod в таблице ICoreWebView2:
// три метода IUnknown и 33 метода интерфейса до него (WebView2.h; тот же
// порядок — iCoreWebView2Vtbl в go-webview2).
const devtoolsSlot = 36

// iidDevtoolsHandler — ICoreWebView2CallDevToolsProtocolMethodCompletedHandler.
var (
	iidUnknown         = windows.GUID{Data1: 0x00000000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidDevtoolsHandler = windows.GUID{Data1: 0x5c4889f0, Data2: 0x5ef6, Data3: 0x4c5a,
		Data4: [8]byte{0x95, 0x2c, 0xd8, 0xf1, 0xb9, 0x2d, 0x05, 0x74}}
)

// devtools — методы DevTools одного окна. Звать только на нити окна.
type devtools struct {
	wv *edge.ICoreWebView2_3
}

// chromiumOf достаёт edge.Chromium из окна go-webview2: поле browser
// неэкспортируемой структуры окна. nil — библиотека устроена иначе.
func chromiumOf(w webview2.WebView) *edge.Chromium {
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() || f.Kind() != reflect.Interface || !f.CanAddr() {
		return nil
	}
	f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	c, _ := f.Interface().(*edge.Chromium)
	return c
}

// newDevtools — методы DevTools окна w. Только на нити окна и после того,
// как окно создано.
func newDevtools(w webview2.WebView) (*devtools, error) {
	c := chromiumOf(w)
	if c == nil {
		return nil, errors.New("go-webview2 не отдаёт движок окна")
	}
	wv := c.GetICoreWebView2_3()
	if wv == nil {
		return nil, errors.New("WebView2 без ICoreWebView2_3")
	}
	return &devtools{wv: wv}, nil
}

// release отпускает ICoreWebView2_3 (его счётчик поднял QueryInterface).
func (d *devtools) release() {
	if d == nil || d.wv == nil {
		return
	}
	vt := *(**[3]uintptr)(unsafe.Pointer(d.wv))
	syscall.SyscallN(vt[2], uintptr(unsafe.Pointer(d.wv)))
	d.wv = nil
}

// devtoolsReply — ответ DevTools: JSON результата или ошибка.
type devtoolsReply struct {
	result json.RawMessage
	err    error
}

// call начинает вызов метода DevTools; ответ придёт в done (из насоса
// сообщений окна, на его нити). Ошибка — вызов не начался, done не придёт.
func (d *devtools) call(method string, params any, done func(devtoolsReply)) error {
	if d.wv == nil {
		return errors.New("окно закрыто")
	}
	if params == nil {
		params = struct{}{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	m, err := windows.UTF16PtrFromString(method)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(string(body))
	if err != nil {
		return err
	}
	h := newDevtoolsHandler(done)
	defer h.release() // своя ссылка; WebView2 держит свою, пока не ответит
	vt := *(**[devtoolsSlot + 1]uintptr)(unsafe.Pointer(d.wv))
	hr, _, _ := syscall.SyscallN(vt[devtoolsSlot], uintptr(unsafe.Pointer(d.wv)),
		uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(h)))
	if int32(hr) < 0 {
		h.forget()
		return fmt.Errorf("CallDevToolsProtocolMethod(%s): HRESULT %#08x", method, uint32(hr))
	}
	return nil
}

// devtoolsHandler — обработчик завершения одного вызова: COM-объект в памяти
// Go, прибитый, пока на него есть ссылки.
type devtoolsHandler struct {
	vtbl *[4]uintptr // первым полем: так его видит COM
	refs int32
	id   uint64
}

// devtoolsCall — то, что объект обработчика держит в Go: ответ и булавка.
type devtoolsCall struct {
	h    *devtoolsHandler
	pin  *runtime.Pinner
	done func(devtoolsReply)
}

var (
	devtoolsVtblOnce sync.Once
	devtoolsVtbl     [4]uintptr

	devtoolsMu    sync.Mutex
	devtoolsSeq   uint64
	devtoolsCalls = map[uint64]*devtoolsCall{}
)

func newDevtoolsHandler(done func(devtoolsReply)) *devtoolsHandler {
	devtoolsVtblOnce.Do(func() {
		devtoolsVtbl = [4]uintptr{
			windows.NewCallback(devtoolsQueryInterface),
			windows.NewCallback(devtoolsAddRef),
			windows.NewCallback(devtoolsRelease),
			windows.NewCallback(devtoolsInvoke),
		}
	})
	devtoolsMu.Lock()
	defer devtoolsMu.Unlock()
	devtoolsSeq++
	h := &devtoolsHandler{vtbl: &devtoolsVtbl, refs: 1, id: devtoolsSeq}
	pin := new(runtime.Pinner)
	pin.Pin(h)
	devtoolsCalls[h.id] = &devtoolsCall{h: h, pin: pin, done: done}
	return h
}

// release — COM Release: последняя ссылка отпускает объект.
func (h *devtoolsHandler) release() uintptr {
	n := atomic.AddInt32(&h.refs, -1)
	if n == 0 {
		devtoolsMu.Lock()
		if c := devtoolsCalls[h.id]; c != nil {
			delete(devtoolsCalls, h.id)
			c.pin.Unpin()
		}
		devtoolsMu.Unlock()
	}
	return uintptr(max(n, 0))
}

// forget — ответа не будет (вызов не начался): done не позовётся.
func (h *devtoolsHandler) forget() {
	devtoolsMu.Lock()
	defer devtoolsMu.Unlock()
	if c := devtoolsCalls[h.id]; c != nil {
		c.done = nil
	}
}

func devtoolsQueryInterface(h *devtoolsHandler, riid *windows.GUID, out *uintptr) uintptr {
	if *riid == iidUnknown || *riid == iidDevtoolsHandler {
		*out = uintptr(unsafe.Pointer(h))
		atomic.AddInt32(&h.refs, 1)
		return 0
	}
	*out = 0
	return 0x80004002 // E_NOINTERFACE
}

func devtoolsAddRef(h *devtoolsHandler) uintptr {
	return uintptr(atomic.AddInt32(&h.refs, 1))
}

func devtoolsRelease(h *devtoolsHandler) uintptr { return h.release() }

func devtoolsInvoke(h *devtoolsHandler, hr uintptr, result *uint16) uintptr {
	devtoolsMu.Lock()
	c := devtoolsCalls[h.id]
	var done func(devtoolsReply)
	if c != nil {
		done, c.done = c.done, nil
	}
	devtoolsMu.Unlock()
	if done == nil {
		return 0
	}
	var r devtoolsReply
	text := windows.UTF16PtrToString(result)
	if int32(hr) < 0 {
		r.err = fmt.Errorf("DevTools: HRESULT %#08x: %s", uint32(hr), text)
	} else {
		r.result = json.RawMessage(text)
	}
	done(r)
	return 0
}
