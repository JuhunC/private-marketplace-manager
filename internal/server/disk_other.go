//go:build !linux && !darwin

package server

// diskUsage is unavailable here; the manager ships as a Linux container.
func diskUsage(string) (diskSpace, [2]int32, bool) { return diskSpace{}, [2]int32{}, false }
