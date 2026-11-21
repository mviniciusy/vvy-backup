package main

import "syscall"

// diskUsage devolve o tamanho e o espaço livre do sistema de arquivos de path.
func diskUsage(path string) (total, free int64, err error) {
	var s syscall.Statfs_t
	if err = syscall.Statfs(path, &s); err != nil {
		return
	}
	return int64(s.Blocks) * int64(s.Bsize), int64(s.Bavail) * int64(s.Bsize), nil
}
