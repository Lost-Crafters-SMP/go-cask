package cask

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native FILE_RENAME_INFORMATION, not Win32 FILE_RENAME_INFO. Natural Go
// alignment supplies the native pointer padding on both 32- and 64-bit hosts.
type fileRenameInformation struct {
	replaceIfExists byte
	rootDirectory   windows.Handle
	fileNameLength  uint32
	fileName        [128]uint16 // Generated final names are at most 124 UTF-16 units.
}

func windowsError(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		// Keep NTSTATUS for diagnostics/As, and its Win32 mapping for Is.
		return errors.Join(err, status.Errno())
	}
	return err
}

func windowsPublicationError(err error) (publication, error) {
	if err == nil {
		return publication{consumed: true}, nil
	}
	mapped := windowsError(err)
	if errors.Is(mapped, windows.ERROR_FILE_EXISTS) || errors.Is(mapped, windows.ERROR_ALREADY_EXISTS) {
		return publication{exists: true}, nil
	}
	if errors.Is(mapped, windows.ERROR_NOT_SUPPORTED) || errors.Is(mapped, windows.ERROR_INVALID_FUNCTION) {
		return publication{}, errors.Join(ErrUnsupportedFilesystem, mapped)
	}
	return publication{}, mapped
}

func publishObject(dir *os.File, temp, final string) (result publication, retErr error) {
	if err := publicationNames(temp, final); err != nil {
		return publication{}, err
	}
	objectName, err := windows.NewNTUnicodeString(temp)
	if err != nil {
		return publication{}, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(dir.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_DONT_REPARSE,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var handle windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(dir)
	if err != nil {
		// Opening the source is not publication: even a collision-like open
		// error must not be normalized to destination-exists.
		return publication{}, fmt.Errorf("open staged rename handle: %w", windowsError(err))
	}
	defer func() { retErr = errors.Join(retErr, windows.CloseHandle(handle)) }()
	name, err := windows.UTF16FromString(final)
	if err != nil {
		return publication{}, err
	}
	var info fileRenameInformation
	if len(name)-1 > len(info.fileName) {
		return publication{}, windows.ERROR_FILENAME_EXCED_RANGE
	}
	info.rootDirectory = windows.Handle(dir.Fd())
	info.fileNameLength = uint32((len(name) - 1) * 2)
	copy(info.fileName[:], name[:len(name)-1])
	// replaceIfExists remains FALSE. Do not enable replacement, exchange,
	// POSIX replacement, or any copy-across-volume behavior.
	err = windows.NtSetInformationFile(handle, &iosb, (*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Offsetof(info.fileName))+info.fileNameLength, windows.FileRenameInformation)
	runtime.KeepAlive(dir)
	return windowsPublicationError(err)
}
