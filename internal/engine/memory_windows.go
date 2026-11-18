package engine

import (
	"fmt"
	"syscall"
	"unsafe"
)

var globalMemoryStatus = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// AutomaticMemory leaves room for Windows and accounts for the heaps already
// promised to running games, even if those JVMs have not committed them yet.
func AutomaticMemory(modified bool, reserved int) (int, error) {
	var memory struct {
		length, load                                                                                uint32
		total, available, totalPage, availablePage, totalVirtual, availableVirtual, extendedVirtual uint64
	}
	memory.length = uint32(unsafe.Sizeof(memory))
	ok, _, err := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&memory)))
	if ok == 0 {
		return 0, fmt.Errorf("无法读取系统内存：%w", err)
	}
	return gameMemory(int(memory.total>>20), int(memory.available>>20), modified, reserved)
}

func gameMemory(total, available int, modified bool, reserved int) (int, error) {
	limit := 4096
	if modified {
		limit = 8192
	}
	// Keep at least 1 GiB of free physical memory for the OS and native code.
	heap := min(limit, available-1024, total*3/4-reserved) / 256 * 256
	if heap < 1024 {
		return 0, fmt.Errorf("可用内存不足以启动另一个 Minecraft 实例")
	}
	return heap, nil
}
