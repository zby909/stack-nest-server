package storage

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpace = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// ReadUsage 读取 Windows 数据目录所在磁盘的可用空间，兼容用户配额。
func ReadUsage(path string) (Usage, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var total, available uint64
	ok, _, callErr := getDiskFreeSpace.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&available)), uintptr(unsafe.Pointer(&total)), 0)
	if ok == 0 {
		return Usage{}, callErr
	}
	return Usage{TotalBytes: total, FreeBytes: available}, nil
}
