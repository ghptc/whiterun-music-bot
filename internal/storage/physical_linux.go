//go:build linux

package storage

// This shim delegates SQLite locking, reads, fsync and crash recovery to the
// pinned modernc unix VFS. Every path that can extend a file is gated BEFORE I/O.
// Keep the callback ABI aligned with modernc.org/sqlite v1.39.1.
import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite "modernc.org/sqlite/lib"
)

const DefaultPhysicalQuota int64 = 10 << 30
const physicalStateSize int64 = 8192

// Kept unallocated for native extent bookkeeping on the supported local FS.
const physicalAllocationReserve int64 = 64 << 10
const physicalSlot = 4096

type physicalEvent struct {
	Pending   bool
	Delivered bool
	Attempts  int
	Retry     int64
	Used      int64
	Channel   CatalogChannel
}
type physicalState struct {
	Sequence   uint64
	Limit      int64
	Base       int64
	Episode    int64
	Warn, Full physicalEvent
	Armed      bool
	Exhausted  bool
}
type PhysicalUsage struct {
	Files    int64
	Reserved int64
	Limit    int64
	Blocked  bool
	Peak     int64
}
type physicalQuota struct {
	mu               sync.Mutex
	path, name       string
	lock, state      *os.File
	saved            physicalState
	vfs, cname, base uintptr
	refs             int
	peak             int64
	limit            int64
}
type quotaFile struct {
	quota             *physicalQuota
	path              string
	original, methods uintptr
}

var quotaRegistry = struct {
	sync.Mutex
	paths map[string]*physicalQuota
	vfs   map[uintptr]*physicalQuota
	files map[uintptr]quotaFile
}{paths: map[string]*physicalQuota{}, vfs: map[uintptr]*physicalQuota{}, files: map[uintptr]quotaFile{}}

// nativePointer is the FFI boundary for addresses returned by the generated
// SQLite/libc ABI (C allocations and generated static method tables). Wrapper
// structs and cloned method tables are allocated/freed through libc. No arbitrary
// application Go heap pointer or captured closure is retained in a native table.
func nativePointer(address uintptr) unsafe.Pointer { return unsafe.Add(unsafe.Pointer(nil), address) }

func quotaPointer[T any](f T) uintptr { return *(*uintptr)(unsafe.Pointer(&f)) }
func quotaFunc[T any](p uintptr) T    { return *(*T)(unsafe.Pointer(&p)) }

func openPhysicalQuota(path string, limit int64) (q *physicalQuota, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	path = filepath.Join(parent, filepath.Base(path))
	if limit > DefaultPhysicalQuota {
		return nil, fmt.Errorf("physical quota cannot exceed 10 GiB")
	}
	quotaRegistry.Lock()
	defer quotaRegistry.Unlock()
	if old := quotaRegistry.paths[path]; old != nil {
		if limit != 0 && limit != old.limit {
			return nil, fmt.Errorf("physical quota differs from open database")
		}
		old.refs++
		return old, nil
	}
	if limit != 0 && limit < 512<<10 {
		return nil, fmt.Errorf("physical quota must be at least 512 KiB")
	}

	var filesystem syscall.Statfs_t
	if e := syscall.Statfs(filepath.Dir(path), &filesystem); e != nil {
		return nil, e
	}
	// These local filesystems provide the exact KEEP_SIZE allocation and flock
	// semantics used below. Unknown/COW/network filesystems fail closed.
	if (filesystem.Type != 0xef53 && filesystem.Type != 0x01021994) || filesystem.Bsize > 4096 {
		return nil, fmt.Errorf("physical quota requires ext4 or tmpfs with allocation blocks <=4096 bytes")
	}
	q = &physicalQuota{path: path, refs: 1}
	controlExisted := false
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".quota", ".quota-lock"} {
		if fi, e := os.Lstat(path + suffix); e == nil {
			if suffix == ".quota" {
				controlExisted = true
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("quota path must not be a symlink")
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
				return nil, fmt.Errorf("quota files must not have hard links")
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	q.lock, err = os.OpenFile(path+".quota-lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	q.state, err = os.OpenFile(path+".quota", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		q.lock.Close()
		return nil, err
	}
	created := q
	defer func() {
		if err != nil {
			if created.vfs != 0 {
				delete(quotaRegistry.vfs, created.vfs)
				tls := libc.NewTLS()
				libc.Xfree(tls, created.vfs)
				tls.Close()
			}
			if created.cname != 0 {
				tls := libc.NewTLS()
				libc.Xfree(tls, created.cname)
				tls.Close()
			}
			created.state.Close()
			created.lock.Close()
		}
	}()
	err = q.locked(func() error {
		info, e := q.state.Stat()
		if e != nil {
			return e
		}
		if info.Size() == 0 {
			if controlExisted {
				return fmt.Errorf("existing physical quota state is empty; refusing to guess a larger quota")
			}
			if limit == 0 {
				limit = DefaultPhysicalQuota
			}
			q.saved = physicalState{Limit: limit, Armed: true}
			if original, e := os.Stat(q.path); e == nil {
				q.saved.Base = rounded(original.Size())
			} else if !os.IsNotExist(e) {
				return e
			}

			before, e := q.usage("", 0)
			if e != nil {
				return e
			}
			if before.Files+physicalStateSize+physicalAllocationReserve > limit {
				return fmt.Errorf("existing files leave no room for physical quota metadata; files preserved")
			}
			if e = q.state.Truncate(physicalStateSize); e != nil {
				return e
			}
			if e = syscall.Fallocate(int(q.state.Fd()), 0, 0, physicalStateSize); e != nil {
				return fmt.Errorf("quota state requires bounded allocation: %w", e)
			}

			allocated, e := q.state.Stat()
			if e != nil {
				return e
			}
			if st, ok := allocated.Sys().(*syscall.Stat_t); !ok || st.Blocks*512 != physicalStateSize {
				return fmt.Errorf("unsupported allocation geometry; ext4 bigalloc is not supported")
			}

			if e = q.save(); e != nil {
				return e
			}
			directory, e := os.Open(filepath.Dir(q.path))
			if e != nil {
				return e
			}
			e = directory.Sync()
			_ = directory.Close()
			if e != nil {
				return e
			}
		} else {
			if e = q.load(); e != nil {
				return e
			}
			if limit != 0 && limit != q.saved.Limit {
				return fmt.Errorf("physical quota is persistent: configured=%d stored=%d", limit, q.saved.Limit)
			}
		}

		if q.saved.Limit > DefaultPhysicalQuota {
			return fmt.Errorf("persisted physical quota exceeds 10 GiB")
		}
		stateInfo, e := q.state.Stat()
		if e != nil {
			return e
		}
		if st, ok := stateInfo.Sys().(*syscall.Stat_t); !ok || st.Blocks*512 != physicalStateSize {
			return fmt.Errorf("unsupported allocation geometry; physical quota metadata preserved")
		}
		u, e := q.usage("", 0)
		if e != nil {
			return e
		}
		if u.Files > q.saved.Limit {
			return fmt.Errorf("existing database exceeds physical quota: files=%d reserved=%d limit=%d base=%d; preserve files", u.Files, u.Reserved, u.Limit, q.saved.Base)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tls := libc.NewTLS()
	defer tls.Close()
	sqlite.Xsqlite3_initialize(tls)
	unixName, e := libc.CString("unix")
	if e != nil {
		return nil, e
	}
	q.base = sqlite.Xsqlite3_vfs_find(tls, unixName)
	libc.Xfree(tls, unixName)
	if q.base == 0 {
		return nil, fmt.Errorf("SQLite unix VFS unavailable")
	}
	q.limit = q.saved.Limit
	q.name = fmt.Sprintf("music-quota-%x", sha256.Sum256([]byte(path)))
	q.cname, err = libc.CString(q.name)
	if err != nil {
		return nil, err
	}
	q.vfs = libc.Xmalloc(tls, uint64(unsafe.Sizeof(sqlite.Tsqlite3_vfs{})))
	if q.vfs == 0 {
		return nil, fmt.Errorf("VFS allocation failed")
	}
	v := (*sqlite.Tsqlite3_vfs)(nativePointer(q.vfs))
	*v = *(*sqlite.Tsqlite3_vfs)(nativePointer(q.base))
	v.FzName = q.cname
	v.FpNext = 0
	v.FxOpen = quotaPointer(quotaOpen)
	quotaRegistry.vfs[q.vfs] = q
	if rc := sqlite.Xsqlite3_vfs_register(tls, q.vfs, 0); rc != sqlite.SQLITE_OK {
		return nil, fmt.Errorf("register quota VFS: %d", rc)
	}
	quotaRegistry.paths[path] = q
	return q, nil
}
func (q *physicalQuota) close() {
	quotaRegistry.Lock()
	defer quotaRegistry.Unlock()
	q.refs--
	q.cleanupLocked()
}

// sql.DB.Close may leave an in-flight Rows/Tx owning a native connection. Its
// callbacks must remain registered until the last native file has closed.
func (q *physicalQuota) cleanupLocked() {
	if q.refs != 0 {
		return
	}
	for _, f := range quotaRegistry.files {
		if f.quota == q {
			return
		}
	}
	tls := libc.NewTLS()
	defer tls.Close()
	sqlite.Xsqlite3_vfs_unregister(tls, q.vfs)
	libc.Xfree(tls, q.vfs)
	libc.Xfree(tls, q.cname)
	delete(quotaRegistry.vfs, q.vfs)
	delete(quotaRegistry.paths, q.path)
	q.state.Close()
	q.lock.Close()
}
func (q *physicalQuota) locked(fn func() error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e := syscall.Flock(int(q.lock.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(q.lock.Fd()), syscall.LOCK_UN)
	return fn()
}
func (q *physicalQuota) load() error {
	var best physicalState
	valid := false
	for i := range 2 {
		var b [physicalSlot]byte
		if _, e := q.state.ReadAt(b[:], int64(i*physicalSlot)); e != nil {
			continue
		}
		n := binary.LittleEndian.Uint32(b[8:12])
		if n > physicalSlot-16 {
			continue
		}
		raw := b[16 : 16+n]
		if crc32.ChecksumIEEE(raw) != binary.LittleEndian.Uint32(b[12:16]) {
			continue
		}
		var s physicalState
		if json.Unmarshal(raw, &s) == nil && s.Limit > 0 && (!valid || s.Sequence > best.Sequence) {
			best = s
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("physical quota state corrupt; writes refused")
	}
	q.saved = best
	return nil
}
func (q *physicalQuota) save() error {
	q.saved.Sequence++
	raw, e := json.Marshal(q.saved)
	if e != nil {
		return e
	}
	if len(raw) > physicalSlot-16 {
		return fmt.Errorf("physical quota state overflow")
	}
	var b [physicalSlot]byte
	binary.LittleEndian.PutUint64(b[:8], q.saved.Sequence)
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(raw)))
	binary.LittleEndian.PutUint32(b[12:16], crc32.ChecksumIEEE(raw))
	copy(b[16:], raw)
	if _, e = q.state.WriteAt(b[:], int64(q.saved.Sequence%2)*physicalSlot); e != nil {
		return e
	}
	return q.state.Sync()
}
func rounded(n int64) int64 {
	if n == 0 {
		return 0
	}
	return (n + 4095) / 4096 * 4096
}
func (q *physicalQuota) usage(target string, end int64) (u PhysicalUsage, err error) {
	sizes := map[string]int64{}
	lengths := map[string]int64{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".quota", ".quota-lock"} {
		p := q.path + suffix
		fi, e := os.Stat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return u, e
		}
		n := rounded(fi.Size())
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			n = max(n, st.Blocks*512)
		}
		sizes[p] = n
		lengths[p] = rounded(fi.Size())
	}
	if target != "" {
		sizes[target] += max(int64(0), rounded(end)-lengths[target])
	}
	for _, n := range sizes {
		u.Files += n
	}
	// Reserve checkpoint space while WAL is retained by readers. At the start of
	// each WAL cycle Base is the main file length. Admission reserves the worst
	// main-file copy as well as the WAL. Checkpoint may spend that reservation;
	// reads and overwrites do not need another reservation at capacity.
	main, wal := sizes[q.path], sizes[q.path+"-wal"]
	u.Reserved = u.Files + max(int64(0), q.saved.Base+wal-main) + physicalAllocationReserve
	u.Peak = q.peak
	u.Limit = q.saved.Limit
	u.Blocked = q.saved.Exhausted
	return u, nil
}
func (q *physicalQuota) observe(u PhysicalUsage, denied bool) error {
	changed := false
	if (denied || u.Reserved >= u.Limit) && !q.saved.Exhausted {
		q.saved.Exhausted = true
		changed = true
	}
	if !denied && u.Reserved < u.Limit*85/100 && q.saved.Exhausted {
		q.saved.Exhausted = false
		changed = true
	}
	if u.Reserved < u.Limit*85/100 && !q.saved.Armed {
		q.saved.Armed = true
		changed = true
	}
	if u.Reserved >= u.Limit*9/10 || denied {
		if q.saved.Armed && (!q.saved.Warn.Pending || q.saved.Warn.Delivered) && (!q.saved.Full.Pending || q.saved.Full.Delivered) {
			q.saved.Episode++
			q.saved.Warn = physicalEvent{}
			q.saved.Full = physicalEvent{}
			q.saved.Armed = false
			changed = true
		}
		if u.Reserved >= u.Limit*9/10 && !q.saved.Warn.Pending {
			q.saved.Warn = physicalEvent{Pending: true, Used: u.Reserved}
			changed = true
		}
	}
	if (denied || u.Reserved >= u.Limit) && !q.saved.Full.Pending {
		q.saved.Full = physicalEvent{Pending: true, Used: u.Reserved}
		changed = true
	}
	if changed {
		return q.save()
	}
	return nil
}
func (q *physicalQuota) guard(path string, end int64, walReset bool, fn func() int32) int32 {
	result := int32(sqlite.SQLITE_IOERR)
	err := q.locked(func() error {
		if e := q.load(); e != nil {
			return e
		}
		oldBase := q.saved.Base
		if walReset {
			fi, e := os.Stat(q.path)
			if e != nil {
				return e
			}
			if q.saved.Base != rounded(fi.Size()) {
				q.saved.Base = rounded(fi.Size())

			}
		}
		u, e := q.usage(path, end)
		if e != nil {
			return e
		}

		current, e := q.usage("", 0)
		if e != nil {
			return e
		}
		grows := u.Files > current.Files
		// Existing bytes remain writable/readable at capacity. Checkpoint may
		// spend its reserved main-file budget; WAL growth must reserve that
		// budget before its very first write.
		admitted := u.Reserved <= u.Limit || (!grows && q.saved.Base == oldBase) || (path == q.path && u.Files+physicalAllocationReserve <= u.Limit)
		if !admitted || u.Files > u.Limit || (grows && u.Files+physicalAllocationReserve > u.Limit) {
			q.saved.Base = oldBase
			actual, e := q.usage("", 0)
			if e != nil {
				return e
			}
			if e = q.observe(actual, true); e != nil {
				return e
			}
			result = sqlite.SQLITE_FULL
			return nil
		}
		if q.saved.Base != oldBase {
			if e = q.save(); e != nil {
				return e
			}
		}
		if e = q.observe(u, false); e != nil {
			return e
		}
		result = fn()
		actual, e := q.usage("", 0)
		if e != nil {
			return e
		}
		q.peak = max(q.peak, actual.Files)
		if actual.Files > q.saved.Limit {
			return fmt.Errorf("physical allocation invariant violated")
		}
		return q.observe(actual, false)
	})
	if err != nil {
		return sqlite.SQLITE_IOERR
	}
	return result
}
func quotaOpen(tls *libc.TLS, vfs, name, file uintptr, flags int32, out uintptr) int32 {
	quotaRegistry.Lock()
	q := quotaRegistry.vfs[vfs]
	quotaRegistry.Unlock()
	if name == 0 {
		return sqlite.SQLITE_FULL
	}
	path := libc.GoString(name)
	if path != q.path && path != q.path+"-wal" && path != q.path+"-journal" {
		return sqlite.SQLITE_CANTOPEN
	}
	native := (*sqlite.Tsqlite3_vfs)(nativePointer(q.base))
	rc := quotaFunc[func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32](native.FxOpen)(tls, q.base, name, file, flags, out)
	if rc != sqlite.SQLITE_OK {
		return rc
	}
	original := (*sqlite.Tsqlite3_file)(nativePointer(file)).FpMethods
	methods := libc.Xmalloc(tls, uint64(unsafe.Sizeof(sqlite.Tsqlite3_io_methods{})))
	if methods == 0 {
		return sqlite.SQLITE_NOMEM
	}
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(methods))
	*m = *(*sqlite.Tsqlite3_io_methods)(nativePointer(original))
	m.FxSync = quotaPointer(quotaSync)
	m.FxWrite = quotaPointer(quotaWrite)
	m.FxTruncate = quotaPointer(quotaTruncate)
	m.FxClose = quotaPointer(quotaClose)
	m.FxFileControl = quotaPointer(quotaControl)
	if m.FxShmMap != 0 {
		m.FxShmMap = quotaPointer(quotaShm)
	}
	quotaRegistry.Lock()
	quotaRegistry.files[file] = quotaFile{q, path, original, methods}
	quotaRegistry.Unlock()
	(*sqlite.Tsqlite3_file)(nativePointer(file)).FpMethods = methods
	return rc
}
func fileQuota(file uintptr) quotaFile {
	quotaRegistry.Lock()
	defer quotaRegistry.Unlock()
	return quotaRegistry.files[file]
}
func quotaWrite(tls *libc.TLS, file, buf uintptr, n int32, offset int64) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))

	return f.quota.guard(f.path, offset+int64(n), f.path == f.quota.path+"-wal" && offset == 0, func() int32 {
		if rc := allocateNative((*sqlite.TunixFile)(nativePointer(file)).Fh, offset, int64(n)); rc != sqlite.SQLITE_OK {
			return rc
		}
		return quotaFunc[func(*libc.TLS, uintptr, uintptr, int32, int64) int32](m.FxWrite)(tls, file, buf, n, offset)
	})
}
func quotaSync(tls *libc.TLS, file uintptr, flags int32) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))
	return f.quota.guard("", 0, false, func() int32 { return quotaFunc[func(*libc.TLS, uintptr, int32) int32](m.FxSync)(tls, file, flags) })
}
func quotaTruncate(tls *libc.TLS, file uintptr, size int64) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))
	fn := func() int32 { return quotaFunc[func(*libc.TLS, uintptr, int64) int32](m.FxTruncate)(tls, file, size) }
	if info, e := os.Stat(f.path); e == nil && size <= info.Size() {
		rc := int32(sqlite.SQLITE_IOERR)
		if e = f.quota.locked(func() error {
			rc = fn()
			if e := f.quota.load(); e != nil {
				return e
			}
			u, e := f.quota.usage("", 0)
			if e != nil {
				return e
			}
			return f.quota.observe(u, false)
		}); e != nil {
			return sqlite.SQLITE_IOERR
		}
		return rc
	}
	return f.quota.guard(f.path, size, false, fn)
}

// KEEP_SIZE allocates only the admitted range, before native pwrite. In
// particular, it prevents ext4 append heuristics from preallocating an extent
// beyond the charged range. Unsupported allocation semantics fail closed.
func allocateNative(fd int32, offset, size int64) int32 {
	start := offset / 4096 * 4096
	length := rounded(offset+size) - start
	if length <= 0 {
		return sqlite.SQLITE_OK
	}
	if e := syscall.Fallocate(int(fd), 1, start, length); e != nil {
		if e == syscall.ENOSPC || e == syscall.EDQUOT {
			return sqlite.SQLITE_FULL
		}
		return sqlite.SQLITE_IOERR_WRITE
	}
	return sqlite.SQLITE_OK
}
func quotaShm(tls *libc.TLS, file uintptr, region, size, extend int32, out uintptr) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))
	native := quotaFunc[func(*libc.TLS, uintptr, int32, int32, int32, uintptr) int32](m.FxShmMap)
	end := int64(4096)
	if extend != 0 {
		end = int64(region+1) * int64(size)
		mappingUnit := max(int64(size), int64(os.Getpagesize()))
		end = (end + mappingUnit - 1) / mappingUnit * mappingUnit
	}
	return f.quota.guard(f.quota.path+"-shm", end, false, func() int32 {
		rc := native(tls, file, region, size, 0, out)
		if rc != sqlite.SQLITE_OK || extend == 0 {
			return rc
		}
		shm := (*sqlite.TunixFile)(nativePointer(file)).FpShm
		if shm != 0 {
			node := (*sqlite.TunixShm)(nativePointer(shm)).FpShmNode
			if node != 0 {
				fd := (*sqlite.TunixShmNode)(nativePointer(node)).FhShm
				if fd >= 0 {
					if rc = allocateNative(fd, 0, end); rc != sqlite.SQLITE_OK {
						return rc
					}
				}
			}
		}
		return native(tls, file, region, size, extend, out)
	})
}
func quotaControl(tls *libc.TLS, file uintptr, op int32, arg uintptr) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))
	switch op {
	case sqlite.SQLITE_FCNTL_SIZE_HINT:
		return sqlite.SQLITE_OK
	case sqlite.SQLITE_FCNTL_CHUNK_SIZE:
		return sqlite.SQLITE_NOTFOUND
	case sqlite.SQLITE_FCNTL_MMAP_SIZE:
		if arg != 0 {
			*(*int64)(nativePointer(arg)) = 0
		}
	}
	return quotaFunc[func(*libc.TLS, uintptr, int32, uintptr) int32](m.FxFileControl)(tls, file, op, arg)
}
func quotaClose(tls *libc.TLS, file uintptr) int32 {
	f := fileQuota(file)
	m := (*sqlite.Tsqlite3_io_methods)(nativePointer(f.original))
	(*sqlite.Tsqlite3_file)(nativePointer(file)).FpMethods = f.original
	rc := quotaFunc[func(*libc.TLS, uintptr) int32](m.FxClose)(tls, file)
	quotaRegistry.Lock()
	delete(quotaRegistry.files, file)
	f.quota.cleanupLocked()
	quotaRegistry.Unlock()
	libc.Xfree(tls, f.methods)
	return rc
}
func (s *DB) PhysicalUsage(ctx context.Context) (u PhysicalUsage, err error) {
	err = s.physical.locked(func() error {
		if e := s.physical.load(); e != nil {
			return e
		}
		var e error
		u, e = s.physical.usage("", 0)
		return e
	})
	return
}
func (m *CatalogAlertMonitor) pollPhysical(ctx context.Context, now time.Time) (handled bool, result error) {
	q := m.DB.physical
	if q == nil {
		return false, nil
	}
	var event physicalEvent
	var threshold int
	var episode int64
	err := q.locked(func() error {
		if e := q.load(); e != nil {
			return e
		}
		u, e := q.usage("", 0)
		if e != nil {
			return e
		}
		if e = q.observe(u, false); e != nil {
			return e
		}
		episode = q.saved.Episode
		switch {
		case q.saved.Warn.Pending && !q.saved.Warn.Delivered && q.saved.Warn.Retry <= now.Unix():
			threshold = 90
			event = q.saved.Warn
			q.saved.Warn.Attempts++
			q.saved.Warn.Retry = now.Add(time.Minute).Unix()
		case q.saved.Full.Pending && !q.saved.Full.Delivered && q.saved.Full.Retry <= now.Unix():
			threshold = 100
			event = q.saved.Full
			q.saved.Full.Attempts++
			q.saved.Full.Retry = now.Add(time.Minute).Unix()
		default:
			return nil
		}
		return q.save()
	})
	if err != nil {
		return true, err
	}
	if threshold == 0 {
		return false, nil
	}
	defer func() {
		e := q.locked(func() error {
			if e := q.load(); e != nil {
				return e
			}
			var target *physicalEvent
			if threshold == 90 {
				target = &q.saved.Warn
			} else {
				target = &q.saved.Full
			}
			if result == nil {
				target.Delivered = true
			} else {
				target.Retry = now.Add(min(time.Hour, time.Second*time.Duration(1<<min(event.Attempts, 12)))).Unix()
			}
			target.Channel = event.Channel
			return q.save()
		})
		result = errors.Join(result, e)
	}()
	channels, err := m.DB.CatalogChannels(ctx, now)
	if err != nil {
		return true, err
	}
	if event.Channel.Channel != "" {
		channels = append([]CatalogChannel{event.Channel}, channels...)
	}
	for _, c := range channels {
		ok, e := m.Notifier.Eligible(ctx, c)
		if e != nil {
			return true, e
		}
		if !ok {
			continue
		}
		event.Channel = c
		if err = q.locked(func() error {
			if e := q.load(); e != nil {
				return e
			}
			if threshold == 90 {
				q.saved.Warn.Channel = c
			} else {
				q.saved.Full.Channel = c
			}
			return q.save()
		}); err != nil {
			return true, err
		}
		if m.Log != nil {
			if threshold == 100 {
				m.Log.Error("catalog_physical_threshold", "episode", episode, "threshold", threshold, "used_and_reserved_bytes", event.Used, "limit_bytes", q.limit, "attempt", event.Attempts+1)
			} else {
				m.Log.Warn("catalog_physical_threshold", "episode", episode, "threshold", threshold, "used_and_reserved_bytes", event.Used, "limit_bytes", q.limit, "attempt", event.Attempts+1)
			}
		}
		message := fmt.Sprintf("Music physical storage %d%% admission threshold: %d/%d bytes including checkpoint reservation. Existing music is preserved.", threshold, event.Used, q.limit)
		if threshold == 100 {
			message += " New allocations blocked: physical quota exhausted."
		}
		return true, m.Notifier.Send(ctx, c, fmt.Sprintf("physical-%d-%d", episode, threshold), message)
	}
	return true, fmt.Errorf("no eligible physical quota alert channel")
}
