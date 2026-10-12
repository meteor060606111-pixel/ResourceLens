package main

import (
	"errors"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Acquire this before NewEngine: a second launch must never bind the proxy port.
// Local objects are scoped to the current interactive Windows session.
type desktopInstance struct {
	mutex windows.Handle
	event windows.Handle
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

func acquireDesktopInstance() (*desktopInstance, bool, error) {
	return acquireNamedInstance(`Local\ResourceLens.Desktop.v2`)
}

func acquireNamedInstance(name string) (*desktopInstance, bool, error) {
	mname, err := windows.UTF16PtrFromString(name + ".Mutex")
	if err != nil {
		return nil, false, err
	}
	mutex, err := windows.CreateMutex(nil, false, mname)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, false, err
	}
	existing := errors.Is(err, windows.ERROR_ALREADY_EXISTS)
	ename, _ := windows.UTF16PtrFromString(name + ".Activate")
	event, eventErr := windows.CreateEvent(nil, 0, 0, ename)
	if eventErr != nil && !errors.Is(eventErr, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return nil, false, eventErr
	}
	if existing {
		// The user launched this second process, so pass foreground permission to
		// the existing window before sending its activation event.
		windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow").Call(0xffffffff)
		err = windows.SetEvent(event)
		windows.CloseHandle(event)
		windows.CloseHandle(mutex)
		return nil, true, err
	}
	return &desktopInstance{mutex: mutex, event: event, stop: make(chan struct{}), done: make(chan struct{})}, false, nil
}

func activateCurrentWindow() {
	user32 := windows.NewLazySystemDLL("user32.dll")
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		visible, _, _ := user32.NewProc("IsWindowVisible").Call(hwnd)
		owner, _, _ := user32.NewProc("GetWindow").Call(hwnd, 4)
		if pid != uint32(windows.GetCurrentProcessId()) || visible == 0 || owner != 0 {
			return 1
		}
		user32.NewProc("ShowWindow").Call(hwnd, 9)
		user32.NewProc("SetForegroundWindow").Call(hwnd)
		return 0
	})
	user32.NewProc("EnumWindows").Call(callback, 0)
}

func (i *desktopInstance) listen(activate func()) {
	go func() {
		defer close(i.done)
		for {
			select {
			case <-i.stop:
				return
			default:
			}
			result, err := windows.WaitForSingleObject(i.event, 250)
			if err != nil {
				return
			}
			if result == windows.WAIT_OBJECT_0 {
				activate()
			}
		}
	}()
}

func (i *desktopInstance) Close() {
	if i == nil {
		return
	}
	i.once.Do(func() {
		close(i.stop)
		// Close is also valid if window startup failed before listen was installed.
		select {
		case <-i.done:
		case <-time.After(300 * time.Millisecond):
		}
		windows.CloseHandle(i.event)
		windows.CloseHandle(i.mutex)
	})
}
