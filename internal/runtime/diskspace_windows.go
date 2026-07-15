//go:build windows

package runtime

import "golang.org/x/sys/windows"

func freeDiskSpace(path string) (uint64, error) {
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}
