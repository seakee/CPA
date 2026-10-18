//go:build darwin || linux

package pluginstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func installIdentityArtifact(t *testing.T, pluginsDir, content string) InstallResult {
	t.Helper()
	archive := makeZip(t, map[string]string{
		"sample-provider" + pluginExtension(runtime.GOOS): content,
	})
	result, errInstall := InstallArchive(archive, Plugin{ID: "sample-provider", Version: "0.4.0"}, InstallOptions{
		PluginsDir: pluginsDir,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	})
	if errInstall != nil {
		t.Fatalf("InstallArchive() error = %v", errInstall)
	}
	return result
}

func TestInstallRecordVerifiesRecordedArtifact(t *testing.T) {
	result := installIdentityArtifact(t, t.TempDir(), "plugin-bytes")
	record, errRecord := NewInstallRecordFromOutcome("sample-provider", result, "official", "https://store.example/registry.json")
	if errRecord != nil {
		t.Fatalf("NewInstallRecordFromOutcome() error = %v", errRecord)
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "official", "https://store.example/registry.json"); errVerify != nil {
		t.Fatalf("Verify() error = %v", errVerify)
	}
	node, errEncode := EncodeInstallRecord(record)
	if errEncode != nil {
		t.Fatalf("EncodeInstallRecord() error = %v", errEncode)
	}
	decoded, ok, errDecode := DecodeInstallRecord(node)
	if errDecode != nil || !ok {
		t.Fatalf("DecodeInstallRecord() = %v, %v", ok, errDecode)
	}
	if decoded.InstanceID != record.InstanceID || decoded.SHA256 != record.SHA256 || decoded.Witness != record.Witness {
		t.Fatalf("decoded record differs from encoded record: %#v vs %#v", decoded, record)
	}
}

func TestInstallRecordRejectsSameBytesRewrite(t *testing.T) {
	result := installIdentityArtifact(t, t.TempDir(), "identical-plugin-bytes")
	record, errRecord := NewInstallRecordFromOutcome("sample-provider", result, "official", "https://store.example/registry.json")
	if errRecord != nil {
		t.Fatalf("NewInstallRecordFromOutcome() error = %v", errRecord)
	}
	original, errRead := os.ReadFile(result.Path)
	if errRead != nil {
		t.Fatalf("read artifact: %v", errRead)
	}
	if errRemove := os.Remove(result.Path); errRemove != nil {
		t.Fatalf("remove artifact: %v", errRemove)
	}
	time.Sleep(2 * time.Millisecond)
	if errWrite := os.WriteFile(result.Path, original, 0o600); errWrite != nil {
		t.Fatalf("rewrite artifact: %v", errWrite)
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "official", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted identical bytes written by a different file instance")
	}
}

func TestInstallRecordRejectsChangedContentAndBinding(t *testing.T) {
	result := installIdentityArtifact(t, t.TempDir(), "plugin-bytes")
	record, errRecord := NewInstallRecordFromOutcome("sample-provider", result, "official", "https://store.example/registry.json")
	if errRecord != nil {
		t.Fatalf("NewInstallRecordFromOutcome() error = %v", errRecord)
	}
	if errAppend := os.WriteFile(result.Path, []byte("plugin-bytes-changed"), 0o600); errAppend != nil {
		t.Fatalf("change artifact: %v", errAppend)
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "official", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted changed artifact bytes")
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.5.0", "official", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted a changed version")
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "other", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted a changed source id")
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "official", "https://other.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted a changed source url")
	}
	if errVerify := record.Verify("sample-provider", filepath.Join(t.TempDir(), "other.dylib"), "0.4.0", "official", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted a changed path")
	}
}

func TestInstallRecordRejectsMissingArtifact(t *testing.T) {
	result := installIdentityArtifact(t, t.TempDir(), "plugin-bytes")
	record, errRecord := NewInstallRecordFromOutcome("sample-provider", result, "official", "https://store.example/registry.json")
	if errRecord != nil {
		t.Fatalf("NewInstallRecordFromOutcome() error = %v", errRecord)
	}
	if errRemove := os.Remove(result.Path); errRemove != nil {
		t.Fatalf("remove artifact: %v", errRemove)
	}
	if errVerify := record.Verify("sample-provider", result.Path, "0.4.0", "official", "https://store.example/registry.json"); errVerify == nil {
		t.Fatal("Verify() accepted a missing artifact")
	}
}

func TestInstallRecordFromOutcomeRejectsReplacedArtifactBeforeIssuance(t *testing.T) {
	pluginsDir := t.TempDir()
	resultA := installIdentityArtifact(t, pluginsDir, "plugin-bytes-source-a")
	resultB := installIdentityArtifact(t, pluginsDir, "plugin-bytes-source-b")
	if _, errRecordA := NewInstallRecordFromOutcome("sample-provider", resultA, "source-a", "https://store-a.example/registry.json"); !errors.Is(errRecordA, ErrInstallArtifactReplaced) {
		t.Fatalf("NewInstallRecordFromOutcome(resultA after B overwrite) error = %v, want ErrInstallArtifactReplaced", errRecordA)
	}
	recordB, errRecordB := NewInstallRecordFromOutcome("sample-provider", resultB, "source-b", "https://store-b.example/registry.json")
	if errRecordB != nil {
		t.Fatalf("NewInstallRecordFromOutcome(resultB) error = %v", errRecordB)
	}
	if errVerify := recordB.Verify("sample-provider", resultB.Path, "0.4.0", "source-b", "https://store-b.example/registry.json"); errVerify != nil {
		t.Fatalf("recordB.Verify() error = %v", errVerify)
	}
}

func TestInstallRecordFromOutcomeRejectsSameBytesDifferentInstanceBeforeIssuance(t *testing.T) {
	pluginsDir := t.TempDir()
	resultA := installIdentityArtifact(t, pluginsDir, "identical-plugin-bytes")
	if errRemove := os.Remove(resultA.Path); errRemove != nil {
		t.Fatalf("remove artifact A: %v", errRemove)
	}
	resultB := installIdentityArtifact(t, pluginsDir, "identical-plugin-bytes")
	if _, errRecordA := NewInstallRecordFromOutcome("sample-provider", resultA, "source-a", "https://store-a.example/registry.json"); !errors.Is(errRecordA, ErrInstallArtifactReplaced) {
		t.Fatalf("NewInstallRecordFromOutcome(resultA after same-byte B write) error = %v, want ErrInstallArtifactReplaced", errRecordA)
	}
	recordB, errRecordB := NewInstallRecordFromOutcome("sample-provider", resultB, "source-b", "https://store-b.example/registry.json")
	if errRecordB != nil {
		t.Fatalf("NewInstallRecordFromOutcome(resultB) error = %v", errRecordB)
	}
	if errVerify := recordB.Verify("sample-provider", resultB.Path, "0.4.0", "source-b", "https://store-b.example/registry.json"); errVerify != nil {
		t.Fatalf("recordB.Verify() error = %v", errVerify)
	}
}
