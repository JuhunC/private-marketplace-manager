package server

import "golang.org/x/sys/unix"

// diskUsage reads the filesystem holding path and its identity.
func diskUsage(path string) (diskSpace, [2]int32, bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return diskSpace{}, [2]int32{}, false
	}
	unit := uint64(st.Bsize)
	return diskSpace{Total: st.Blocks * unit, Used: (st.Blocks - st.Bfree) * unit, Free: st.Bavail * unit}, st.Fsid.Val, true
}
