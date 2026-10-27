package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procGetForegroundWindow         = user32.NewProc("GetForegroundWindow")

	clsidShellLink      = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW       = windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile      = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	clsidFileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
	iidShellItem        = windows.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
)

// COM vtable slots, counting IUnknown's three methods.
const (
	slotRelease = 2

	shellLinkGetPath             = 3
	shellLinkSetDescription      = 7
	shellLinkSetWorkingDirectory = 9
	shellLinkSetIconLocation     = 17
	shellLinkSetPath             = 20

	persistFileLoad = 5
	persistFileSave = 6

	fileDialogShow       = 3
	fileDialogSetOptions = 9
	fileDialogSetFolder  = 12
	fileDialogSetTitle   = 17
	fileDialogGetResult  = 20

	shellItemGetDisplayName = 5
)

const (
	fosPickFolders      = 0x20
	fosForceFileSystem  = 0x40
	sigdnFileSysPath    = 0x80058000
	errorCancelled      = 0x800704c7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
	clsctxInprocServer  = 1
	uninstallKey        = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Nanolathe`
	shortcutName        = "Nanolathe.lnk"
	shortcutDescription = "Play Nanolathe using your Total Annihilation game assets"
)

// call invokes a COM method by vtable slot and returns its HRESULT as an
// error. A Go pointer passed as a uintptr is only an integer to the garbage
// collector: the buffer behind it could be freed, or moved with a growing
// stack, before Windows reads or writes it. go:uintptrescapes, as on
// windows.LazyProc.Call, keeps every pointer converted with
// uintptr(unsafe.Pointer(p)) directly in a call's argument list on the
// heap and alive until call returns, so callers convert there and nowhere
// else.
//
//go:uintptrescapes
func call(obj unsafe.Pointer, slot int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(obj)
	method := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	hr, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(obj)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("nanolathe: Windows shell call failed: HRESULT %#08x", uint32(hr))
	}
	return nil
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		_ = call(obj, slotRelease)
	}
}

// withCOM runs f on a locked thread in a single-threaded COM apartment, as
// the shell's objects require.
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// S_FALSE: COM was already initialized on this thread, which still
	// needs its own uninitialize.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || err == syscall.Errno(1) {
		defer windows.CoUninitialize()
	} else if err != syscall.Errno(windows.RPC_E_CHANGED_MODE) {
		return err
	}
	return f()
}

func create(clsid, iid *windows.GUID) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("nanolathe: Windows shell object unavailable: HRESULT %#08x", uint32(hr))
	}
	return obj, nil
}

func queryInterface(obj unsafe.Pointer, iid *windows.GUID) (unsafe.Pointer, error) {
	var out unsafe.Pointer
	err := call(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, err
}

// utf16 converts s for a Windows call, or gives nil when s holds a NUL.
// Pass it as uintptr(unsafe.Pointer(p)) only in a call's argument list.
func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// SaveShortcut writes a .lnk that starts target in workdir with target's
// own icon. It uses the Unicode shell link interface, which keeps paths
// outside the ANSI code page intact.
func SaveShortcut(path, target, workdir string) error {
	return withCOM(func() error {
		link, err := create(&clsidShellLink, &iidShellLinkW)
		if err != nil {
			return err
		}
		defer release(link)
		target16, workdir16, description16 := utf16(target), utf16(workdir), utf16(shortcutDescription)
		if err := call(link, shellLinkSetPath, uintptr(unsafe.Pointer(target16))); err != nil {
			return err
		}
		if err := call(link, shellLinkSetWorkingDirectory, uintptr(unsafe.Pointer(workdir16))); err != nil {
			return err
		}
		if err := call(link, shellLinkSetDescription, uintptr(unsafe.Pointer(description16))); err != nil {
			return err
		}
		if err := call(link, shellLinkSetIconLocation, uintptr(unsafe.Pointer(target16)), 0); err != nil {
			return err
		}
		file, err := queryInterface(link, &iidPersistFile)
		if err != nil {
			return err
		}
		defer release(file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return call(file, persistFileSave, uintptr(unsafe.Pointer(utf16(path))), 1)
	})
}

// ShortcutTarget reads a .lnk's target path.
func ShortcutTarget(path string) (string, error) {
	var target string
	err := withCOM(func() error {
		link, err := create(&clsidShellLink, &iidShellLinkW)
		if err != nil {
			return err
		}
		defer release(link)
		file, err := queryInterface(link, &iidPersistFile)
		if err != nil {
			return err
		}
		defer release(file)
		if err := call(file, persistFileLoad, uintptr(unsafe.Pointer(utf16(path))), 0); err != nil {
			return err
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		if err := call(link, shellLinkGetPath, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); err != nil {
			return err
		}
		target = windows.UTF16ToString(buf)
		return nil
	})
	return target, err
}

func shortcutPaths() (startMenu, desktop string, err error) {
	programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	if err != nil {
		return "", "", err
	}
	desk, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(programs, shortcutName), filepath.Join(desk, shortcutName), nil
}

// Register writes the Start Menu shortcut (and the desktop one when asked),
// replacing a source installer's shortcut of the same name, and the
// per-user Apps & Features entry that uninstalls through the launcher.
func Register(e Entries) error {
	startMenu, desktop, err := shortcutPaths()
	if err != nil {
		return err
	}
	if err := SaveShortcut(startMenu, e.Launcher, e.Base); err != nil {
		return err
	}
	if e.Desktop {
		if err := SaveShortcut(desktop, e.Launcher, e.Base); err != nil {
			return err
		}
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	for name, value := range map[string]string{
		"DisplayName":     "Nanolathe",
		"DisplayVersion":  e.Version,
		"DisplayIcon":     e.Launcher + ",0",
		"Publisher":       "Nanolathe",
		"InstallLocation": e.Base,
		"URLInfoAbout":    "https://nanolathe.gg/",
		"UninstallString": `"` + e.Launcher + `" --uninstall`,
	} {
		if err := key.SetStringValue(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]uint32{"NoModify": 1, "NoRepair": 1} {
		if err := key.SetDWordValue(name, value); err != nil {
			return err
		}
	}
	return nil
}

// Unregister removes the shortcuts and the Apps & Features entry. A
// shortcut is removed only while it still starts this installation.
func Unregister(e Entries) error {
	startMenu, desktop, err := shortcutPaths()
	if err != nil {
		return err
	}
	for _, path := range []string{startMenu, desktop} {
		if target, err := ShortcutTarget(path); err == nil && (target == e.Launcher || filepath.Dir(target) == e.Base ||
			filepath.Base(target) == "powershell.exe") {
			_ = os.Remove(path)
		}
	}
	if err := registry.DeleteKey(registry.CURRENT_USER, uninstallKey); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// HasDesktopShortcut reports whether the desktop holds a Nanolathe shortcut.
func HasDesktopShortcut() bool {
	_, desktop, err := shortcutPaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(desktop)
	return err == nil
}

// Owner is the window a dialog belongs to: the launcher's own, which is in
// front when the player clicks a button.
func Owner() uintptr {
	hwnd, _, _ := procGetForegroundWindow.Call()
	return hwnd
}

// PickFolder shows the Explorer folder picker, starting in start when it
// is a folder. ok is false when the player cancels.
func PickFolder(owner uintptr, title, start string) (folder string, ok bool, err error) {
	err = withCOM(func() error {
		dialog, err := create(&clsidFileOpenDialog, &iidFileOpenDialog)
		if err != nil {
			return err
		}
		defer release(dialog)
		if err := call(dialog, fileDialogSetOptions, fosPickFolders|fosForceFileSystem); err != nil {
			return err
		}
		_ = call(dialog, fileDialogSetTitle, uintptr(unsafe.Pointer(utf16(title))))
		if info, statErr := os.Stat(start); statErr == nil && info.IsDir() {
			var item unsafe.Pointer
			hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(utf16(start))), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item)))
			if int32(hr) >= 0 {
				_ = call(dialog, fileDialogSetFolder, uintptr(item))
				release(item)
			}
		}
		vtbl := *(*unsafe.Pointer)(dialog)
		show := *(*uintptr)(unsafe.Add(vtbl, uintptr(fileDialogShow)*unsafe.Sizeof(uintptr(0))))
		hr, _, _ := syscall.SyscallN(show, uintptr(dialog), owner)
		if uint32(hr) == errorCancelled {
			return nil
		} else if int32(hr) < 0 {
			return fmt.Errorf("nanolathe: folder picker failed: HRESULT %#08x", uint32(hr))
		}
		var item unsafe.Pointer
		if err := call(dialog, fileDialogGetResult, uintptr(unsafe.Pointer(&item))); err != nil {
			return err
		}
		defer release(item)
		var name *uint16
		if err := call(item, shellItemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); err != nil {
			return err
		}
		defer windows.CoTaskMemFree(unsafe.Pointer(name))
		folder, ok = windows.UTF16PtrToString(name), true
		return nil
	})
	return folder, ok, err
}

// Open shows a folder in Explorer.
func Open(path string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(path), nil, nil, windows.SW_SHOWNORMAL)
}

// Confirm asks a yes-or-no question in a standard Windows message box.
func Confirm(owner uintptr, title, text string) bool {
	const yesNo, question, yes = 0x4, 0x20, 6
	ret, _ := windows.MessageBox(windows.HWND(owner), windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(title), yesNo|question)
	return ret == yes
}

// Notify shows a message box with an OK button.
func Notify(owner uintptr, title, text string) {
	const information = 0x40
	_, _ = windows.MessageBox(windows.HWND(owner), windows.StringToUTF16Ptr(text), windows.StringToUTF16Ptr(title), information)
}

// SmartAppControl reads Smart App Control's state; a system without it
// reports AppControlOff.
func SmartAppControl() AppControl {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\CI\Policy`, registry.QUERY_VALUE)
	if err != nil {
		return AppControlOff
	}
	defer key.Close()
	switch state, _, err := key.GetIntegerValue("VerifiedAndReputablePolicyState"); {
	case err != nil:
		return AppControlOff
	case state == 1:
		return AppControlOn
	case state == 2:
		return AppControlEvaluation
	}
	return AppControlOff
}
