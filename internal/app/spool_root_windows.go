//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	ownerSecurityInformation           = 0x00000001
	daclSecurityInformation            = 0x00000004
	protectedDACLInformation           = 0x80000000
	seFileObject                       = 1
	accessAllowedAceType               = 0
	accessDeniedAceType                = 1
	accessAllowedObjectAceType         = 5
	accessDeniedObjectAceType          = 6
	accessAllowedCallbackAceType       = 9
	accessDeniedCallbackAceType        = 10
	accessAllowedCallbackObjectAceType = 11
	accessDeniedCallbackObjectAceType  = 12
	aclSizeInformation                 = 2
	tokenQuery                         = 0x0008
	tokenUser                          = 1
	aceObjectTypePresent               = 0x00000001
	aceInheritedObjectTypePresent      = 0x00000002
	fileWriteData                      = 0x00000002
	fileAppendData                     = 0x00000004
	fileWriteEA                        = 0x00000010
	fileWriteAttributes                = 0x00000100
	fileDeleteChild                    = 0x00000040
	deleteAccess                       = 0x00010000
	writeDAC                           = 0x00040000
	writeOwner                         = 0x00080000
	genericWrite                       = 0x40000000
	genericAll                         = 0x10000000
)

const windowsWriteAccessMask = fileWriteData |
	fileAppendData |
	fileWriteEA |
	fileWriteAttributes |
	fileDeleteChild |
	deleteAccess |
	writeDAC |
	writeOwner |
	genericWrite |
	genericAll

type windowsACLSizeInformation struct {
	AceCount uint32
	_        [2]uint32
}

type windowsACEHeader struct {
	AceType  uint8
	AceFlags uint8
	AceSize  uint16
}

var (
	advapi32                   = syscall.NewLazyDLL("advapi32.dll")
	getSecurityInfo            = advapi32.NewProc("GetSecurityInfo")
	setSecurityInfo            = advapi32.NewProc("SetSecurityInfo")
	getACLInformation          = advapi32.NewProc("GetAclInformation")
	getACE                     = advapi32.NewProc("GetAce")
	initializeACL              = advapi32.NewProc("InitializeAcl")
	addAccessAllowedACE        = advapi32.NewProc("AddAccessAllowedAce")
	openProcessToken           = advapi32.NewProc("OpenProcessToken")
	getTokenInformation        = advapi32.NewProc("GetTokenInformation")
	equalSID                   = advapi32.NewProc("EqualSid")
	localFree                  = kernel32.NewProc("LocalFree")
	getCurrentProcess          = kernel32.NewProc("GetCurrentProcess")
	closeHandle                = kernel32.NewProc("CloseHandle")
	createFileW                = kernel32.NewProc("CreateFileW")
	getFileInformationByHandle = kernel32.NewProc("GetFileInformationByHandle")
)

var (
	windowsSystemSID = [...]byte{
		1, 2, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0,
	}
	windowsAdministratorsSID = [...]byte{
		1, 2, 0, 0, 0, 0, 0, 5, 32, 0, 0, 0, 32, 2, 0, 0,
	}
	windowsCreatorOwnerSID = [...]byte{
		1, 1, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0,
	}
)

func validateWebhookSpoolOwner(file *os.File) error {
	if file == nil {
		return errors.New("webhook spool path has no security handle")
	}
	if err := validateWindowsSpoolHandle(file); err != nil {
		return err
	}
	ownerSID, dacl, descriptor, err := windowsSecurityDescriptor(file)
	if err != nil {
		return err
	}
	defer localFree.Call(descriptor)

	currentSID, sidBuffer, releaseSID, err := windowsCurrentUserSID()
	if err != nil {
		return err
	}
	defer func() {
		runtime.KeepAlive(sidBuffer)
		releaseSID()
	}()
	if !windowsTrustedOwner(ownerSID, currentSID) {
		return errors.New("webhook spool path has unsafe owner")
	}
	if err := validateWindowsSpoolDACL(dacl, currentSID, ownerSID); err != nil {
		return err
	}
	return nil
}
func validateWindowsSpoolPathComponent(path string, final bool) error {
	directory, err := openWindowsSpoolDirectory(path)
	if err != nil {
		return fmt.Errorf("open webhook spool path component: %w", err)
	}
	defer directory.Close()
	if err := validateWindowsSpoolHandle(directory); err != nil {
		return err
	}
	if !final {
		return nil
	}
	ownerSID, dacl, descriptor, err := windowsSecurityDescriptor(directory)
	if err != nil {
		return err
	}
	defer localFree.Call(descriptor)
	currentSID, sidBuffer, releaseSID, err := windowsCurrentUserSID()
	if err != nil {
		return err
	}
	defer func() {
		runtime.KeepAlive(sidBuffer)
		releaseSID()
	}()
	if final && !windowsTrustedOwner(ownerSID, currentSID) {
		return errors.New("webhook spool path has unsafe owner")
	}
	return validateWindowsSpoolDACL(dacl, currentSID, ownerSID)
}

func windowsTrustedOwner(ownerSID, currentSID uintptr) bool {
	return windowsSIDEqual(ownerSID, currentSID) ||
		windowsSIDEqual(ownerSID, uintptr(unsafe.Pointer(&windowsSystemSID[0]))) ||
		windowsSIDEqual(ownerSID, uintptr(unsafe.Pointer(&windowsAdministratorsSID[0])))
}

func validateWindowsSpoolHandle(file *os.File) error {
	var information syscall.ByHandleFileInformation
	result, _, callErr := getFileInformationByHandle.Call(
		uintptr(file.Fd()),
		uintptr(unsafe.Pointer(&information)),
	)
	if result == 0 {
		return fmt.Errorf("inspect webhook spool handle: %w", callErr)
	}
	if information.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("webhook spool path contains a reparse point")
	}
	return nil
}

type windowsACLHeader struct {
	AclRevision uint8
	Sbz1        uint8
	AclSize     uint16
	AceCount    uint16
	Sbz2        uint16
}

const windowsACLRevision = 2

func hardenWindowsSpoolDirectory(path string) error {
	directory, err := openWindowsSpoolHandle(
		path,
		syscall.GENERIC_READ|writeDAC,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS,
	)
	if err != nil {
		return err
	}
	defer directory.Close()
	return hardenWindowsSpoolHandle(directory)
}

func hardenWindowsSpoolHandle(directory *os.File) error {
	if directory == nil {
		return errors.New("webhook spool security handle is unavailable")
	}
	if err := validateWindowsSpoolHandle(directory); err != nil {
		return err
	}
	currentSID, sidBuffer, releaseSID, err := windowsCurrentUserSID()
	if err != nil {
		return err
	}
	defer func() {
		runtime.KeepAlive(sidBuffer)
		releaseSID()
	}()
	sids := []uintptr{
		currentSID,
		uintptr(unsafe.Pointer(&windowsSystemSID[0])),
		uintptr(unsafe.Pointer(&windowsAdministratorsSID[0])),
	}
	aclSize := uint32(unsafe.Sizeof(windowsACLHeader{}))
	for _, sid := range sids {
		aclSize += uint32(unsafe.Sizeof(windowsACEHeader{})) + 4 + windowsSIDLength(sid)
	}
	acl := make([]byte, aclSize)
	if result, _, callErr := initializeACL.Call(
		uintptr(unsafe.Pointer(&acl[0])), uintptr(len(acl)), windowsACLRevision,
	); result == 0 {
		return fmt.Errorf("initialize webhook spool DACL: %w", callErr)
	}
	for _, sid := range sids {
		if result, _, callErr := addAccessAllowedACE.Call(
			uintptr(unsafe.Pointer(&acl[0])), windowsACLRevision, genericAll, sid,
		); result == 0 {
			return fmt.Errorf("build webhook spool DACL: %w", callErr)
		}
	}
	status, _, _ := setSecurityInfo.Call(
		uintptr(directory.Fd()), seFileObject,
		daclSecurityInformation|protectedDACLInformation,
		0, 0, uintptr(unsafe.Pointer(&acl[0])), 0,
	)
	if status != 0 {
		return fmt.Errorf("protect webhook spool DACL: %w", syscall.Errno(status))
	}
	return nil
}

func windowsSIDLength(sid uintptr) uint32 {
	if sid == 0 {
		return 0
	}
	return 8 + 4*uint32(*(*uint8)(unsafe.Pointer(sid + 1)))
}

func windowsSecurityDescriptor(file *os.File) (ownerSID, dacl, descriptor uintptr, err error) {
	var status uintptr
	status, _, _ = getSecurityInfo.Call(
		uintptr(file.Fd()),
		seFileObject,
		ownerSecurityInformation|daclSecurityInformation,
		uintptr(unsafe.Pointer(&ownerSID)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if status != 0 {
		return 0, 0, 0, fmt.Errorf("inspect webhook spool security descriptor: %w", syscall.Errno(status))
	}
	if ownerSID == 0 || descriptor == 0 {
		return 0, 0, 0, errors.New("webhook spool security descriptor has no owner")
	}
	if dacl == 0 {
		localFree.Call(descriptor)
		return 0, 0, 0, errors.New("webhook spool path has an unrestricted DACL")
	}
	return ownerSID, dacl, descriptor, nil
}

func windowsCurrentUserSID() (uintptr, []byte, func(), error) {
	process, _, callErr := getCurrentProcess.Call()
	if process == 0 {
		return 0, nil, nil, fmt.Errorf("get current process: %w", callErr)
	}
	var token uintptr
	result, _, callErr := openProcessToken.Call(
		process,
		tokenQuery,
		uintptr(unsafe.Pointer(&token)),
	)
	if result == 0 {
		return 0, nil, nil, fmt.Errorf("open current process token: %w", callErr)
	}
	release := func() {
		closeHandle.Call(token)
	}

	var required uint32
	_, _, queryErr := getTokenInformation.Call(
		token,
		tokenUser,
		0,
		0,
		uintptr(unsafe.Pointer(&required)),
	)
	if required < uint32(unsafe.Sizeof(uintptr(0))) || required > 1<<20 {
		release()
		return 0, nil, nil, fmt.Errorf("query current user SID size: %w", queryErr)
	}
	buffer := make([]byte, required)
	result, _, callErr = getTokenInformation.Call(
		token,
		tokenUser,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&required)),
	)
	if result == 0 {
		release()
		return 0, nil, nil, fmt.Errorf("query current user SID: %w", callErr)
	}
	sid := *(*uintptr)(unsafe.Pointer(&buffer[0]))
	if sid == 0 {
		release()
		return 0, nil, nil, errors.New("current process token has no user SID")
	}
	return sid, buffer, release, nil
}

func validateWindowsSpoolDACL(dacl, currentSID, ownerSID uintptr) error {
	var information windowsACLSizeInformation
	result, _, callErr := getACLInformation.Call(
		dacl,
		uintptr(unsafe.Pointer(&information)),
		unsafe.Sizeof(information),
		aclSizeInformation,
	)
	if result == 0 {
		return fmt.Errorf("inspect webhook spool DACL: %w", callErr)
	}
	if information.AceCount == 0 {
		return errors.New("webhook spool path has an empty DACL")
	}
	for index := uint32(0); index < information.AceCount; index++ {
		var ace uintptr
		result, _, callErr = getACE.Call(
			dacl,
			uintptr(index),
			uintptr(unsafe.Pointer(&ace)),
		)
		if result == 0 || ace == 0 {
			return fmt.Errorf("inspect webhook spool DACL ACE %d: %w", index, callErr)
		}
		header := (*windowsACEHeader)(unsafe.Pointer(ace))
		if header.AceSize < uint16(unsafe.Sizeof(windowsACEHeader{})) {
			return fmt.Errorf("webhook spool DACL ACE %d is malformed", index)
		}
		switch header.AceType {
		case accessDeniedAceType, accessDeniedObjectAceType,
			accessDeniedCallbackAceType, accessDeniedCallbackObjectAceType:
			continue
		case accessAllowedAceType, accessAllowedObjectAceType,
			accessAllowedCallbackAceType, accessAllowedCallbackObjectAceType:
			mask, sid, err := windowsAllowedACEData(ace, header)
			if err != nil {
				return fmt.Errorf("inspect webhook spool DACL ACE %d: %w", index, err)
			}
			if mask&windowsWriteAccessMask == 0 {
				continue
			}
			if windowsSIDEqual(sid, currentSID) ||
				windowsSIDEqual(sid, uintptr(unsafe.Pointer(&windowsSystemSID[0]))) ||
				windowsSIDEqual(sid, uintptr(unsafe.Pointer(&windowsAdministratorsSID[0]))) ||
				windowsSIDEqual(sid, uintptr(unsafe.Pointer(&windowsCreatorOwnerSID[0]))) ||
				windowsTrustedOwner(ownerSID, currentSID) && windowsSIDEqual(sid, ownerSID) {
				continue
			}
			return fmt.Errorf("webhook spool DACL grants write access to an untrusted SID (ACE %d)", index)
		default:
			return fmt.Errorf("webhook spool DACL contains unsupported ACE type %d", header.AceType)
		}
	}
	return nil
}

func windowsAllowedACEData(ace uintptr, header *windowsACEHeader) (mask uint32, sid uintptr, err error) {
	mask = *(*uint32)(unsafe.Pointer(ace + unsafe.Offsetof(struct {
		Header windowsACEHeader
		Mask   uint32
	}{}.Mask)))
	sidOffset := uintptr(unsafe.Sizeof(windowsACEHeader{})) + unsafe.Sizeof(mask)
	if header.AceType == accessAllowedObjectAceType ||
		header.AceType == accessAllowedCallbackObjectAceType {
		if uintptr(header.AceSize) < sidOffset+unsafe.Sizeof(uint32(0)) {
			return 0, 0, errors.New("ACE is too small")
		}
		flags := *(*uint32)(unsafe.Pointer(ace + sidOffset))
		sidOffset += unsafe.Sizeof(flags)
		if flags&aceObjectTypePresent != 0 {
			sidOffset += 16
		}
		if flags&aceInheritedObjectTypePresent != 0 {
			sidOffset += 16
		}
	}
	if uintptr(header.AceSize) < sidOffset+unsafe.Sizeof(uint32(0)) {
		return 0, 0, errors.New("ACE has no SID")
	}
	return mask, ace + sidOffset, nil
}

func windowsSIDEqual(first, second uintptr) bool {
	if first == 0 || second == 0 {
		return false
	}
	result, _, _ := equalSID.Call(first, second)
	return result != 0
}

func prepareWebhookSpoolDir(path string) error {
	if err := validateWebhookSpoolPathComponents(path); err != nil {
		return err
	}
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	if volume == "" {
		return errors.New("webhook spool path has no volume")
	}
	rootPath := volume + string(filepath.Separator)
	relative, err := filepath.Rel(rootPath, cleaned)
	if err != nil || relative == "." || relative == "" {
		return errors.New("webhook spool path must not be filesystem root")
	}
	filesystemRoot, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open webhook spool filesystem root: %w", err)
	}
	defer filesystemRoot.Close()
	current := filesystemRoot
	defer func() {
		if current != filesystemRoot {
			_ = current.Close()
		}
	}()
	parts := strings.Split(relative, string(filepath.Separator))
	finalCreated := false
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("webhook spool path contains unsafe component")
		}
		mkdirErr := current.Mkdir(part, 0o700)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create webhook spool directory component: %w", mkdirErr)
		}
		if index == len(parts)-1 {
			finalCreated = mkdirErr == nil
		}
		info, statErr := current.Lstat(part)
		if statErr != nil {
			return fmt.Errorf("inspect webhook spool directory component: %w", statErr)
		}
		if !info.IsDir() || windowsFileInfoIsReparsePoint(info) {
			return errors.New("webhook spool path must be a directory")
		}
		next, openErr := current.OpenRoot(part)
		if openErr != nil {
			return fmt.Errorf("open webhook spool directory component: %w", openErr)
		}
		openedInfo, openedErr := next.Stat(".")
		pathInfo, pathErr := current.Lstat(part)
		if openedErr != nil || pathErr != nil ||
			openedErr == nil && pathErr == nil && !os.SameFile(openedInfo, pathInfo) {
			_ = next.Close()
			return errors.New("webhook spool path component changed during inspection")
		}
		if current != filesystemRoot {
			_ = current.Close()
		}
		current = next
		if index == len(parts)-1 {
			if finalCreated {
				if err := hardenWindowsSpoolDirectory(cleaned); err != nil {
					_ = current.Close()
					_ = next.Close()
					return err
				}
			}
			directory, openErr := current.Open(".")
			if openErr != nil {
				return fmt.Errorf("open webhook spool directory: %w", openErr)
			}
			ownerErr := validateWebhookSpoolOwner(directory)
			closeErr := directory.Close()
			if ownerErr != nil {
				return fmt.Errorf("unsafe webhook spool directory: %w", ownerErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close webhook spool directory: %w", closeErr)
			}
		}
	}
	return validateWebhookSpoolPathComponents(path)
}

func validateWebhookSpoolPathComponents(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if windowsFileInfoIsReparsePoint(info) {
				return errors.New("webhook spool path contains a reparse point")
			}
			if err := validateWindowsSpoolPathComponent(current, current == path); err != nil {
				return fmt.Errorf("unsafe webhook spool path component: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect webhook spool path: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
func windowsFileInfoIsReparsePoint(info os.FileInfo) bool {
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular|os.ModeSocket) != 0
}

func openWindowsSpoolDirectory(path string) (*os.File, error) {
	return openWindowsSpoolHandle(
		path,
		syscall.GENERIC_READ,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS,
	)
}

func openWebhookSpoolOwnerFile(path string) (*os.File, error) {
	return openWindowsSpoolHandle(
		path,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.OPEN_ALWAYS,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT,
	)
}

func openWindowsSpoolHandle(path string, access, creation, flags uint32) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := createFileW.Call(
		uintptr(unsafe.Pointer(name)),
		uintptr(access),
		uintptr(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE),
		0,
		uintptr(creation),
		uintptr(flags),
		0,
	)
	if syscall.Handle(handle) == syscall.InvalidHandle {
		return nil, callErr
	}
	return os.NewFile(handle, path), nil
}
