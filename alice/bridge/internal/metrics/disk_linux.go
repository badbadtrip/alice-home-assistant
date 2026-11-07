package metrics

import "syscall"

func diskKB(path string) (total, free int64) {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0, 0
	}
	return int64(fs.Blocks) * int64(fs.Bsize) / 1024, int64(fs.Bavail) * int64(fs.Bsize) / 1024
}
