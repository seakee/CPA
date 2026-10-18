//go:build darwin

package pluginstore

import (
	"fmt"
	"os"
	"syscall"
)

const witnessKind = "darwin"

func fileWitnessFromInfo(info os.FileInfo) (FileWitness, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return FileWitness{}, fmt.Errorf("install record: unexpected darwin stat type")
	}
	return FileWitness{
		Kind:         witnessKind,
		Device:       uint64(uint32(stat.Dev)),
		Inode:        stat.Ino,
		ChangeTimeNS: stat.Ctimespec.Nano(),
		BirthTimeNS:  stat.Birthtimespec.Nano(),
		Size:         stat.Size,
	}, nil
}
