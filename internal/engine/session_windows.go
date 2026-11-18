package engine

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

type gameSession struct {
	dir    string
	mounts []string
}

func startGameSession(instanceDir string) (*gameSession, error) {
	dir, err := os.MkdirTemp(instanceDir, ".session-")
	if err != nil {
		return nil, err
	}
	session := &gameSession{dir: dir}
	for _, name := range []string{"saves", "mods", "config", "resourcepacks", "shaderpacks", "screenshots", "schematics", "crash-reports"} {
		source, target := filepath.Join(instanceDir, name), filepath.Join(dir, name)
		if err := os.MkdirAll(source, 0700); err != nil {
			session.close()
			return nil, err
		}
		if err := mountSessionDirectory(source, target); err != nil {
			session.close()
			return nil, fmt.Errorf("无法共享实例目录 %s：%w", name, err)
		}
		session.mounts = append(session.mounts, target)
	}
	for _, name := range []string{"options.txt", "servers.dat"} {
		data, err := os.ReadFile(filepath.Join(instanceDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), data, 0600)
		}
		if err != nil {
			session.close()
			return nil, err
		}
	}
	return session, nil
}

func (s *gameSession) close() {
	for _, mount := range s.mounts {
		if err := os.Remove(mount); err != nil && !os.IsNotExist(err) {
			return
		}
	}
	_ = os.RemoveAll(s.dir)
}

func mountSessionDirectory(source, target string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	substitute := `\??\` + source
	if strings.HasPrefix(source, `\\`) {
		substitute = `\??\UNC\` + strings.TrimPrefix(source, `\\`)
	}
	native, err := windows.UTF16FromString(substitute)
	if err != nil {
		return err
	}
	display, err := windows.UTF16FromString(source)
	if err != nil {
		return err
	}
	// Mount-point offsets and lengths are in bytes relative to PathBuffer.
	data := make([]byte, 16, 16+2*(len(native)+len(display)))
	binary.LittleEndian.PutUint32(data, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:], uint16(8+2*(len(native)+len(display))))
	binary.LittleEndian.PutUint16(data[10:], uint16(2*(len(native)-1)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*len(native)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*(len(display)-1)))
	for _, unit := range append(native, display...) {
		data = binary.LittleEndian.AppendUint16(data, unit)
	}
	targetName, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return err
	}
	handle, err := windows.CreateFile(targetName, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var written uint32
	return windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &data[0], uint32(len(data)), nil, 0, &written, nil)
}
