package server

// diskSpace describes the filesystem holding a directory, in bytes. Free is the space the manager can
// use; Used excludes blocks the filesystem reserves, as df does.
type diskSpace struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
	Low   bool   `json:"low"`
}

// judge flags a disk as low when under a tenth of it is free or when less than reserve bytes remain.
func (d *diskSpace) judge(reserve uint64) { d.Low = d.Free < d.Total/10 || d.Free < reserve }

// storage reports the extension folder's filesystem and, when it differs, the state database's.
// An upload stages a full copy before publishing it, so the extension disk needs room for two of the
// largest uploads; SQLite needs working space for its journal and growth.
func (s *Server) storage() map[string]diskSpace {
	out := map[string]diskSpace{}
	ext, extFS, ok := diskUsage(s.cfg.Extensions)
	if !ok {
		return nil
	}
	ext.judge(2 * uint64(s.cfg.MaxUpload))
	out["extensions"] = ext
	if state, stateFS, ok := diskUsage(s.cfg.State); ok && (stateFS != extFS || stateFS == [2]int32{}) {
		state.judge(1 << 30)
		out["state"] = state
	}
	return out
}
