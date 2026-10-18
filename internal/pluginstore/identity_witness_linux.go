//go:build linux

package pluginstore

import (
	"fmt"
	"os"
	"syscall"
)

const witnessKind = "linux"

func fileWitnessFromInfo(info os.FileInfo) (FileWitness, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return FileWitness{}, fmt.Errorf("install record: unexpected linux stat type")
	}
	return FileWitness{
		Kind:         witnessKind,
		Device:       stat.Dev,
		Inode:        stat.Ino,
		ChangeTimeNS: stat.Ctim.Nano(),
		Size:         stat.Size,
	}, nil
}
