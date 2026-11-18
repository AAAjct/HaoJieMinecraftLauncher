package main

import (
	"unsafe"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
)

func removeWindowBorder(window *mygo.Window) {
	set := windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	hwnd := window.NativeHandle()
	if windows.RtlGetVersion().BuildNumber < 22000 {
		const immersiveDarkMode = 20
		dark := uint32(1)
		set.Call(hwnd, immersiveDarkMode, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
		return
	}
	const borderColor = 34 // DWMWA_BORDER_COLOR
	none := uint32(0xfffffffe)
	set.Call(hwnd, borderColor, uintptr(unsafe.Pointer(&none)), unsafe.Sizeof(none))
}
