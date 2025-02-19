// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"io"
	"runtime"
	"syscall"
	"unsafe"
)

// Auxiliary information if the File describes a directory
type dirInfo struct {
	dir uintptr // Pointer to DIR structure from dirent.h
}

func (d *dirInfo) close() {
	if d.dir == 0 {
		return
	}
	closedir(d.dir)
	d.dir = 0
}

func (f *File) readdir(n int, mode readdirMode) (names []string, dirents []DirEntry, infos []FileInfo, err error) {
	if f.dirinfo == nil {
		dir, call, errno := darwinOpenDir(syscallFd(f.handle.(unixFileHandle)))
		if errno != nil {
			return nil, nil, nil, &PathError{Op: call, Path: f.name, Err: errno}
		}
		f.dirinfo = &dirInfo{
			dir: dir,
		}
	}
	d := f.dirinfo

	size := n
	if size <= 0 {
		size = 100
		n = -1
	}

	var dirent syscall.Dirent
	var entptr *syscall.Dirent
	for len(names)+len(dirents)+len(infos) < size || n == -1 {
		if errno := readdir_r(d.dir, &dirent, &entptr); errno != 0 {
			if errno == syscall.EINTR {
				continue
			}
			return names, dirents, infos, &PathError{Op: "readdir", Path: f.name, Err: errno}
		}
		if entptr == nil { // EOF
			break
		}
		if dirent.Ino == 0 {
			continue
		}
		name := (*[len(syscall.Dirent{}.Name)]byte)(unsafe.Pointer(&dirent.Name))[:]
		for i, c := range name {
			if c == 0 {
				name = name[:i]
				break
			}
		}
		// Check for useless names before allocating a string.
		if string(name) == "." || string(name) == ".." {
			continue
		}
		if mode == readdirName {
			names = append(names, string(name))
		} else if mode == readdirDirEntry {
			de, err := newUnixDirent(f.name, string(name), dtToType(dirent.Type))
			if IsNotExist(err) {
				// File disappeared between readdir and stat.
				// Treat as if it didn't exist.
				continue
			}
			if err != nil {
				return nil, dirents, nil, err
			}
			dirents = append(dirents, de)
		} else {
			info, err := lstat(f.name + "/" + string(name))
			if IsNotExist(err) {
				// File disappeared between readdir + stat.
				// Treat as if it didn't exist.
				continue
			}
			if err != nil {
				return nil, nil, infos, err
			}
			infos = append(infos, info)
		}
		runtime.KeepAlive(f)
	}

	if n > 0 && len(names)+len(dirents)+len(infos) == 0 {
		return nil, nil, nil, io.EOF
	}
	return names, dirents, infos, nil
}

func dtToType(typ uint8) FileMode {
	switch typ {
	case syscall.DT_BLK:
		return ModeDevice
	case syscall.DT_CHR:
		return ModeDevice | ModeCharDevice
	case syscall.DT_DIR:
		return ModeDir
	case syscall.DT_FIFO:
		return ModeNamedPipe
	case syscall.DT_LNK:
		return ModeSymlink
	case syscall.DT_REG:
		return 0
	case syscall.DT_SOCK:
		return ModeSocket
	}
	return ^FileMode(0)
}

// darwinOpenDir returns a pointer to a DIR structure suitable for
// ReadDir. In case of an error, the name of the failed
// syscall is returned along with a syscall.Errno.
// Borrowed from upstream's internal/poll/fd_opendir_darwin.go
func darwinOpenDir(fd syscallFd) (uintptr, string, error) {
	// fdopendir(3) takes control of the file descriptor,
	// so use a dup.
	fd2, err := syscall.Dup(fd)
	if err != nil {
		return 0, "dup", err
	}
	var dir uintptr
	for {
		dir, err = fdopendir(fd2)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		syscall.Close(fd2)
		return 0, "fdopendir", err
	}
	return dir, "", nil
}

// Implemented in syscall/syscall_libc_darwin_*.go.

//go:linkname fdopendir syscall.fdopendir
func fdopendir(fd int) (dir uintptr, err error)

//go:linkname closedir syscall.closedir
func closedir(dir uintptr) (err error)

//go:linkname readdir_r syscall.readdir_r
func readdir_r(dir uintptr, entry *syscall.Dirent, result **syscall.Dirent) (res syscall.Errno)
