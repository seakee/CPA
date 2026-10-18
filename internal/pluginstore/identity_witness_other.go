//go:build !darwin && !linux

package pluginstore

import (
	"fmt"
	"os"
)

// witnessKind is empty on platforms without a verified file-instance witness.
// Records may still be written so plugin management keeps working, but Verify
// always rejects them on read.
const witnessKind = witnessUnsupported

func fileWitnessFromInfo(_ os.FileInfo) (FileWitness, error) {
	return FileWitness{}, fmt.Errorf("install record: file witness is not supported on this platform")
}
