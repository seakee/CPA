//go:build darwin || linux

package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginstore"
	"gopkg.in/yaml.v3"
)

const (
	identityPluginID    = "sample-provider"
	identityRegistryURL = "https://registry.example/identity.json"
	identitySourceURL   = "https://downloads.example/identity.zip"
)

func identityFixture(t *testing.T, content string) *Handler {
	t.Helper()
	return identityFixtureBytes(t, []byte(content))
}

func identityFixtureBytes(t *testing.T, content []byte) *Handler {
	t.Helper()
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{Dir: t.TempDir()},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: identityRegistryURL,
	}
	identitySetArchiveBytes(t, h, content)
	return h
}

func identitySetArchive(t *testing.T, h *Handler, content string) {
	t.Helper()
	identitySetArchiveBytes(t, h, []byte(content))
}

func identitySetArchiveBytes(t *testing.T, h *Handler, content []byte) {
	t.Helper()
	archive := makeManagementPluginStoreZip(t, identityPluginID+managementPluginExtension(runtime.GOOS), string(content))
	digest := sha256.Sum256(archive)
	h.pluginStoreHTTPClient = fakePluginStoreHTTPClient{
		identityRegistryURL: directRegistryJSON(identitySourceURL, hex.EncodeToString(digest[:])),
		identitySourceURL:   archive,
	}
}

func buildLoadedManagementPlugin(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatalf("loaded-plugin C fixture is not supported on %s", runtime.GOOS)
	}
	cc, errCC := exec.LookPath("cc")
	if errCC != nil {
		t.Fatalf("C compiler unavailable: %v", errCC)
	}
	out := filepath.Join(t.TempDir(), "loaded-skip-plugin"+managementPluginExtension(runtime.GOOS))
	args := make([]string, 0, 6)
	if runtime.GOOS == "darwin" {
		args = append(args, "-dynamiclib")
	} else {
		args = append(args, "-shared", "-fPIC")
	}
	args = append(args, "-o", out, filepath.Join("testdata", "loaded-skip-plugin", "main.c"))
	cmd := exec.Command(cc, args...)
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build loaded-plugin C fixture: %v\n%s", errBuild, output)
	}
	content, errRead := os.ReadFile(out)
	if errRead != nil {
		t.Fatalf("read loaded-plugin C fixture: %v", errRead)
	}
	return content
}

func identityInstallRecorder(h *Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: identityPluginID}}
	c.Set(ConfigV8ContextKey, true)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/plugin-store/"+identityPluginID+"/install", nil)
	h.InstallPluginFromStore(c)
	return rec
}

func identityRequest(t *testing.T, h *Handler, deleting bool, want int) *httptest.ResponseRecorder {
	t.Helper()
	var rec *httptest.ResponseRecorder
	if deleting {
		rec = httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: identityPluginID}}
		c.Set(ConfigV8ContextKey, true)
		c.Request = httptest.NewRequest(http.MethodDelete, "/api/plugins/"+identityPluginID, nil)
		h.DeletePlugin(c)
	} else {
		rec = identityInstallRecorder(h)
	}
	if rec.Code != want {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	return rec
}

func identityRecord(t *testing.T, h *Handler) pluginstore.InstallRecord {
	t.Helper()
	item, ok := h.cfg.Plugins.Configs[identityPluginID]
	if !ok {
		t.Fatalf("plugin config missing")
	}
	record, ok, errRecord := pluginstore.DecodeInstallRecord(yamlMappingValue(&item.Raw, "install"))
	if errRecord != nil || !ok {
		t.Fatalf("decode install record: ok=%v err=%v", ok, errRecord)
	}
	return record
}

func identityBlockSave(t *testing.T, h *Handler) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(blocker, []byte("block"), 0o600); errWrite != nil {
		t.Fatalf("write blocker: %v", errWrite)
	}
	h.configFilePath = filepath.Join(blocker, "config.yaml")
}

func identityRestarted(t *testing.T, configPath string) *Handler {
	t.Helper()
	data, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		t.Fatalf("parse config: %v", errParse)
	}
	return &Handler{cfg: cfg}
}

func identityReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	return data
}

func TestInstallIdentityQualifiesAndSurvivesRestart(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	if !first.Qualified || first.InstanceID == "" || first.Version != "0.4.0" {
		t.Fatalf("first installation context = %+v", first)
	}
	restarted := identityRestarted(t, h.configFilePath)
	second := restarted.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID != first.InstanceID || second.Path != first.Path || second.SourceID != first.SourceID {
		t.Fatalf("restarted installation context = %+v, first = %+v", second, first)
	}
	if errWrite := os.WriteFile(first.Path, []byte("tampered-bytes"), 0o600); errWrite != nil {
		t.Fatalf("tamper artifact: %v", errWrite)
	}
	if third := restarted.PluginInstallationContext(identityPluginID); third.Qualified {
		t.Fatalf("changed bytes still qualified: %+v", third)
	}
}

func TestInstallIdentityUnchangedRepeatInstallKeepsInstance(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	identityRequest(t, h, false, 200)
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID != first.InstanceID {
		t.Fatalf("unchanged reinstall rotated instance: first=%+v second=%+v", first, second)
	}
}

func TestInstallIdentityDeleteReinstallRotates(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	firstBytes := identityReadFile(t, first.Path)
	identityRequest(t, h, true, 200)
	if deleted := h.PluginInstallationContext(identityPluginID); deleted.Qualified {
		t.Fatalf("deleted installation still qualified: %+v", deleted)
	}
	identityRequest(t, h, false, 200)
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID == first.InstanceID {
		t.Fatalf("reinstall did not rotate instance: first=%+v second=%+v", first, second)
	}
	if !bytes.Equal(firstBytes, identityReadFile(t, second.Path)) {
		t.Fatal("reinstalled artifact bytes differ")
	}
	if second.Version != first.Version || second.SourceID != first.SourceID || second.Path != first.Path {
		t.Fatalf("source/version/path changed unexpectedly: first=%+v second=%+v", first, second)
	}
}

func TestInstallIdentityConfigSaveFailureInvalidatesNewArtifact(t *testing.T) {
	h := identityFixture(t, "old-plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	original := h.configFilePath
	identitySetArchive(t, h, "new-plugin-bytes")
	identityBlockSave(t, h)
	identityRequest(t, h, false, 500)
	if record := identityRecord(t, h); record.InstanceID != first.InstanceID {
		t.Fatalf("in-memory config was not rolled back: %+v", record)
	}
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("failed install produced a qualified instance: %+v", context)
	}
	restarted := identityRestarted(t, original)
	if context := restarted.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("durable config accepted the uncommitted artifact: %+v", context)
	}
}

func TestInstallIdentityDeleteConfigSaveFailureInvalidatesOldInstance(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	original := h.configFilePath
	identityBlockSave(t, h)
	rec := identityRequest(t, h, true, 500)
	var response struct {
		Error       string
		FileDeleted bool `json:"file_deleted"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode delete response: %v", errUnmarshal)
	}
	if response.Error != "config_save_failed" || !response.FileDeleted {
		t.Fatalf("unexpected delete response: %+v", response)
	}
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("in-memory deleted installation still qualified: %+v", context)
	}
	restarted := identityRestarted(t, original)
	if context := restarted.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("stale durable record qualified after file delete: %+v", context)
	}
	if _, errStat := os.Stat(first.Path); !os.IsNotExist(errStat) {
		t.Fatalf("artifact still present after delete: %v", errStat)
	}
}

func TestInstallIdentityHomeDeleteThenStoreReinstallRotates(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	report := homeplugins.DeleteWithReport(context.Background(), h.cfg, nil, 1, identityPluginID)
	if !report.OK {
		t.Fatalf("home delete report = %+v", report)
	}
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("home-deleted installation still qualified: %+v", context)
	}
	identityRequest(t, h, false, 200)
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID == first.InstanceID {
		t.Fatalf("store reinstall after home delete did not rotate: first=%+v second=%+v", first, second)
	}
}

func TestInstallIdentityManualSameBytesSkipRejectedThenExplicitReinstallRotates(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	original := identityReadFile(t, first.Path)
	if errRemove := os.Remove(first.Path); errRemove != nil {
		t.Fatalf("remove artifact: %v", errRemove)
	}
	time.Sleep(2 * time.Millisecond)
	if errWrite := os.WriteFile(first.Path, original, 0o600); errWrite != nil {
		t.Fatalf("rewrite identical artifact: %v", errWrite)
	}
	rewrittenInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat rewritten artifact: %v", errStat)
	}
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("manual same-byte rewrite remained qualified: %+v", context)
	}
	// Discovery still selects the file: file discovery is not a trust boundary.
	files, errDiscover := pluginhost.DiscoverPluginFiles(h.cfg.Plugins.Dir)
	if errDiscover != nil || len(files) != 1 || files[0].Path != first.Path {
		t.Fatalf("discovery after manual rewrite = %+v, err=%v", files, errDiscover)
	}
	beforeRecord := identityRecord(t, h)
	beforeConfig := identityReadFile(t, h.configFilePath)
	rec := identityInstallRecorder(h)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode rejection: %v", errUnmarshal)
	}
	if body.Error != "plugin_install_unowned_existing_file" {
		t.Fatalf("unexpected rejection: %+v", body)
	}
	if afterBytes := identityReadFile(t, first.Path); !bytes.Equal(original, afterBytes) {
		t.Fatal("rejection changed the artifact bytes")
	}
	afterInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat after rejection: %v", errStat)
	}
	if !os.SameFile(rewrittenInfo, afterInfo) {
		t.Fatal("rejection replaced the artifact file instance")
	}
	if afterConfig := identityReadFile(t, h.configFilePath); !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("rejection changed the persisted config")
	}
	if afterRecord := identityRecord(t, h); !reflect.DeepEqual(beforeRecord, afterRecord) {
		t.Fatalf("rejection changed the receipt: before=%+v after=%+v", beforeRecord, afterRecord)
	}
	if h.PluginInstallationContext(identityPluginID).Qualified {
		t.Fatal("rejection qualified the invalid receipt")
	}

	// Explicit DeletePlugin is the only recovery path; it is a separate user
	// action, not an atomicity claim.
	identityRequest(t, h, true, 200)
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("explicit delete left a qualified receipt: %+v", context)
	}
	identityRequest(t, h, false, 200)
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID == first.InstanceID {
		t.Fatalf("explicit delete + install did not rotate: first=%+v second=%+v", first, second)
	}
	if !bytes.Equal(original, identityReadFile(t, second.Path)) {
		t.Fatal("explicit delete + install did not restore the verified artifact bytes")
	}
}

func TestInstallIdentityMissingRecordIsUnqualified(t *testing.T) {
	pluginsDir := t.TempDir()
	path, errPath := pluginstore.InstallPath(pluginsDir, runtime.GOOS, runtime.GOARCH, identityPluginID, "0.0.1")
	if errPath != nil {
		t.Fatalf("resolve install path: %v", errPath)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("create plugin dir: %v", errMkdir)
	}
	if errWrite := os.WriteFile(path, []byte("plugin-bytes"), 0o600); errWrite != nil {
		t.Fatalf("write artifact: %v", errWrite)
	}
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{identityPluginID: pluginConfigWithStoreSource(t, "official", "https://store.example/source")},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	context := h.PluginInstallationContext(identityPluginID)
	if context.Qualified || context.Reason != "install_record_missing" {
		t.Fatalf("missing record context = %+v", context)
	}
}

func TestInstallIdentitySourceChangeRequiresReinstall(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	secondRegistryURL := "https://registry.example/identity-other.json"
	archive := makeManagementPluginStoreZip(t, identityPluginID+managementPluginExtension(runtime.GOOS), "plugin-bytes")
	digest := sha256.Sum256(archive)
	h.pluginStoreRegistryURL = secondRegistryURL
	h.pluginStoreHTTPClient = fakePluginStoreHTTPClient{
		secondRegistryURL: directRegistryJSON(identitySourceURL, hex.EncodeToString(digest[:])),
		identitySourceURL: archive,
	}
	// The existing source-switch guard rejects an implicit source change.
	identityRequest(t, h, false, 409)
	if after := h.PluginInstallationContext(identityPluginID); !after.Qualified || after.InstanceID != first.InstanceID {
		t.Fatalf("rejected source change altered the recorded instance: first=%+v after=%+v", first, after)
	}
	// Even if the store manifest is edited in place, the recorded source binding
	// no longer matches and the installation stops being qualified.
	item := h.cfg.Plugins.Configs[identityPluginID]
	node := pluginConfigNode(item)
	storeNode := yamlMappingValue(node, "store")
	setYAMLMappingValue(storeNode, "source-url", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: secondRegistryURL})
	updated, errConfig := pluginInstanceConfigFromNode(node)
	if errConfig != nil {
		t.Fatalf("update store manifest: %v", errConfig)
	}
	h.cfg.Plugins.Configs[identityPluginID] = updated
	if tampered := h.PluginInstallationContext(identityPluginID); tampered.Qualified {
		t.Fatalf("edited store source remained qualified: %+v", tampered)
	}
	// A real uninstall followed by an install from the new source issues a new
	// instance bound to the new source.
	identityRequest(t, h, true, 200)
	identityRequest(t, h, false, 200)
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID == first.InstanceID {
		t.Fatalf("source change after reinstall did not rotate: first=%+v second=%+v", first, second)
	}
	if second.SourceURL == first.SourceURL {
		t.Fatalf("source url did not change: first=%+v second=%+v", first, second)
	}
}

func TestInstallIdentityFirstInstallConfigFailureLeavesNoInstance(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	original := h.configFilePath
	identityBlockSave(t, h)
	identityRequest(t, h, false, 500)
	if _, ok := h.cfg.Plugins.Configs[identityPluginID]; ok {
		t.Fatal("failed first install left an in-memory config entry")
	}
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("failed first install produced a qualified instance: %+v", context)
	}
	restarted := identityRestarted(t, original)
	if context := restarted.PluginInstallationContext(identityPluginID); context.Qualified {
		t.Fatalf("durable config contains a failed first install: %+v", context)
	}
}

func identityConfigWrite(t *testing.T, h *Handler, method string, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: identityPluginID}}
	c.Set(ConfigV8ContextKey, true)
	c.Request = httptest.NewRequest(method, "/api/plugins/"+identityPluginID+"/config", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodPut {
		h.PutPluginConfig(c)
	} else {
		h.PatchPluginConfig(c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("config write status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestInstallIdentityPluginConfigWritePreservesRecord(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	item := h.cfg.Plugins.Configs[identityPluginID]
	body, errBody := pluginConfigJSONObject(item)
	if errBody != nil {
		t.Fatalf("plugin config JSON: %v", errBody)
	}
	delete(body, "install")
	body["custom-setting"] = "kept"
	encoded, errJSON := json.Marshal(body)
	if errJSON != nil {
		t.Fatalf("encode config body: %v", errJSON)
	}
	identityConfigWrite(t, h, http.MethodPut, string(encoded))
	second := h.PluginInstallationContext(identityPluginID)
	if !second.Qualified || second.InstanceID != first.InstanceID {
		t.Fatalf("PUT dropped the installation record: first=%+v second=%+v", first, second)
	}
	identityConfigWrite(t, h, http.MethodPatch, `{"install":{"contract":1,"instance-id":"forged"}}`)
	third := h.PluginInstallationContext(identityPluginID)
	if !third.Qualified || third.InstanceID != first.InstanceID {
		t.Fatalf("PATCH replaced the installation record: first=%+v third=%+v", first, third)
	}
	identityConfigWrite(t, h, http.MethodPatch, `{"install":null}`)
	fourth := h.PluginInstallationContext(identityPluginID)
	if !fourth.Qualified || fourth.InstanceID != first.InstanceID {
		t.Fatalf("PATCH removed the installation record: first=%+v fourth=%+v", first, fourth)
	}
}

func TestInstallIdentityConcurrentInstallConverges(t *testing.T) {
	h := identityFixture(t, "plugin-bytes")
	var wg sync.WaitGroup
	statuses := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- identityInstallRecorder(h).Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusInternalServerError && status != http.StatusConflict {
			t.Fatalf("concurrent install status = %d", status)
		}
	}
	// Concurrency may leave the last receipt stale relative to the winner of
	// the file rename; that must be unqualified, never a false positive.
	if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
		record := identityRecord(t, h)
		if errVerify := record.Verify(identityPluginID, context.Path, context.Version, context.SourceID, context.SourceURL); errVerify != nil {
			t.Fatalf("qualified concurrent result does not verify: %v context=%+v", errVerify, context)
		}
	}
	// With no auto-remove, recovery after an unqualified concurrent result is an
	// explicit DeletePlugin followed by a fresh install.
	if !h.PluginInstallationContext(identityPluginID).Qualified {
		files, errDiscover := pluginhost.DiscoverPluginFiles(h.cfg.Plugins.Dir)
		if errDiscover != nil {
			t.Fatalf("discover recovery artifact: %v", errDiscover)
		}
		_, configured := h.cfg.Plugins.Configs[identityPluginID]
		if len(files) > 0 || configured {
			identityRequest(t, h, true, 200)
		}
		identityRequest(t, h, false, 200)
	}
	if final := h.PluginInstallationContext(identityPluginID); !final.Qualified {
		t.Fatalf("explicit-delete recovery did not converge: %+v", final)
	}
}

func TestInstallIdentityLoadedSameByteSkipRejected(t *testing.T) {
	pluginBytes := buildLoadedManagementPlugin(t)
	h := identityFixtureBytes(t, pluginBytes)
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	if !first.Qualified {
		t.Fatalf("first installation is not qualified: %+v", first)
	}
	h.cfg.Plugins.Enabled = true
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	host.ApplyConfig(context.Background(), h.cfg)
	if !host.PluginLoaded(identityPluginID) {
		t.Fatalf("host did not load the C fixture; config=%+v", h.cfg.Plugins.Configs[identityPluginID])
	}
	h.pluginHost = host

	beforeBytes := identityReadFile(t, first.Path)
	beforeRecord := identityRecord(t, h)
	beforeConfig := identityReadFile(t, h.configFilePath)
	if before := h.PluginInstallationContext(identityPluginID); !before.Qualified || before.InstanceID != first.InstanceID {
		t.Fatalf("precondition context = %+v, first = %+v", before, first)
	}

	// Invalidate the receipt without changing the artifact bytes, while the
	// Host keeps the already loaded image resident.
	if errRemove := os.Remove(first.Path); errRemove != nil {
		t.Fatalf("remove artifact: %v", errRemove)
	}
	time.Sleep(2 * time.Millisecond)
	if errWrite := os.WriteFile(first.Path, beforeBytes, 0o600); errWrite != nil {
		t.Fatalf("rewrite identical artifact: %v", errWrite)
	}
	beforeRejectInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat rewritten artifact: %v", errStat)
	}
	if invalid := h.PluginInstallationContext(identityPluginID); invalid.Qualified {
		t.Fatalf("rewritten artifact is still qualified: %+v", invalid)
	}
	if !host.PluginLoaded(identityPluginID) {
		t.Fatal("host lost the loaded image before the rejection")
	}

	rec := identityInstallRecorder(h)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode rejection: %v", errUnmarshal)
	}
	if body.Error != "plugin_install_unowned_existing_file" {
		t.Fatalf("unexpected rejection: %+v", body)
	}

	if afterBytes := identityReadFile(t, first.Path); !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("rejection changed the artifact bytes")
	}
	afterRejectInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat after rejection: %v", errStat)
	}
	if !os.SameFile(beforeRejectInfo, afterRejectInfo) {
		t.Fatal("rejection replaced the artifact file instance")
	}
	if afterConfig := identityReadFile(t, h.configFilePath); !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("rejection changed the persisted config")
	}
	if afterRecord := identityRecord(t, h); !reflect.DeepEqual(beforeRecord, afterRecord) {
		t.Fatalf("rejection changed the installation receipt: before=%+v after=%+v", beforeRecord, afterRecord)
	}
	if after := h.PluginInstallationContext(identityPluginID); after.Qualified {
		t.Fatalf("rejection qualified the invalid receipt: %+v", after)
	}
	if !host.PluginLoaded(identityPluginID) {
		t.Fatal("rejection unloaded the already loaded plugin")
	}
}

func TestInstallIdentityUnownedSkipWithConcurrentApplyLeavesFile(t *testing.T) {
	pluginBytes := buildLoadedManagementPlugin(t)
	h := identityFixtureBytes(t, pluginBytes)
	identityRequest(t, h, false, 200)
	first := h.PluginInstallationContext(identityPluginID)
	if !first.Qualified {
		t.Fatalf("first installation is not qualified: %+v", first)
	}
	original := identityReadFile(t, first.Path)
	if errRemove := os.Remove(first.Path); errRemove != nil {
		t.Fatalf("remove artifact: %v", errRemove)
	}
	time.Sleep(2 * time.Millisecond)
	if errWrite := os.WriteFile(first.Path, original, 0o600); errWrite != nil {
		t.Fatalf("rewrite identical artifact: %v", errWrite)
	}
	rewrittenInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat rewritten artifact: %v", errStat)
	}
	if h.PluginInstallationContext(identityPluginID).Qualified {
		t.Fatal("rewritten artifact is still qualified")
	}
	beforeRecord := identityRecord(t, h)
	beforeConfig := identityReadFile(t, h.configFilePath)

	h.cfg.Plugins.Enabled = true
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	h.pluginHost = host
	applyDone := make(chan struct{})
	go func() {
		defer close(applyDone)
		host.ApplyConfig(context.Background(), h.cfg)
	}()
	rec := identityInstallRecorder(h)
	<-applyDone

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode rejection: %v", errUnmarshal)
	}
	if body.Error != "plugin_install_unowned_existing_file" {
		t.Fatalf("unexpected rejection: %+v", body)
	}
	if afterBytes := identityReadFile(t, first.Path); !bytes.Equal(original, afterBytes) {
		t.Fatal("concurrent apply + install changed the artifact bytes")
	}
	afterInfo, errStat := os.Stat(first.Path)
	if errStat != nil {
		t.Fatalf("stat after rejection: %v", errStat)
	}
	if !os.SameFile(rewrittenInfo, afterInfo) {
		t.Fatal("concurrent apply + install replaced the artifact file instance")
	}
	if afterConfig := identityReadFile(t, h.configFilePath); !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("concurrent apply + install changed the persisted config")
	}
	if afterRecord := identityRecord(t, h); !reflect.DeepEqual(beforeRecord, afterRecord) {
		t.Fatalf("concurrent apply + install changed the receipt: before=%+v after=%+v", beforeRecord, afterRecord)
	}
	if !host.PluginLoaded(identityPluginID) {
		t.Fatal("host did not load the plugin during the concurrent apply")
	}
}

// gatedPluginStoreHTTPClient blocks the artifact download once, so a test can
// take h.mu after the install request has taken its config snapshot.
type gatedPluginStoreHTTPClient struct {
	responses fakePluginStoreHTTPClient
	gateURL   string
	reached   chan struct{}
	release   chan struct{}
}

func (c *gatedPluginStoreHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if req.URL.String() == c.gateURL {
		close(c.reached)
		<-c.release
	}
	return c.responses.Do(req)
}

// identityWaitForBytes waits until the installer has renamed its artifact into
// place. Ordering is enforced by h.mu, not by timing: the request cannot mint
// its receipt while the test holds the lock.
func identityWaitForBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if data, errRead := os.ReadFile(path); errRead == nil && bytes.Equal(data, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("installer never wrote %s", path)
		}
		runtime.Gosched()
	}
}

// An alternate writer replaces the target after the controlled installer
// wrote it but before the receipt is minted. The issuer must not bind the
// replacement file (different bytes, or same bytes as a new file instance) to
// this install's source.
func TestInstallIdentityAlternateWriterDuringMintFailsClosed(t *testing.T) {
	installerBytes := []byte("installer-bytes")
	cases := []struct {
		name    string
		replace func(t *testing.T, pluginsDir, path string) []byte
	}{
		{
			name: "shared installer writes different bytes",
			replace: func(t *testing.T, pluginsDir, _ string) []byte {
				other := []byte("alternate-writer-bytes")
				archive := makeManagementPluginStoreZip(t, identityPluginID+managementPluginExtension(runtime.GOOS), string(other))
				if _, errInstall := pluginstore.InstallArchive(archive, pluginstore.Plugin{ID: identityPluginID, Version: "0.4.0"}, pluginstore.InstallOptions{PluginsDir: pluginsDir}); errInstall != nil {
					t.Fatalf("alternate installer write: %v", errInstall)
				}
				return other
			},
		},
		{
			name: "manual rewrite of identical bytes",
			replace: func(t *testing.T, _ string, path string) []byte {
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatalf("manual remove: %v", errRemove)
				}
				if errWrite := os.WriteFile(path, installerBytes, 0o600); errWrite != nil {
					t.Fatalf("manual rewrite: %v", errWrite)
				}
				return installerBytes
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := identityFixtureBytes(t, installerBytes)
			gate := &gatedPluginStoreHTTPClient{
				responses: h.pluginStoreHTTPClient.(fakePluginStoreHTTPClient),
				gateURL:   identitySourceURL,
				reached:   make(chan struct{}),
				release:   make(chan struct{}),
			}
			h.pluginStoreHTTPClient = gate
			pluginsDir, errDir := config.ResolvePluginsDir(h.cfg.Plugins.Dir)
			if errDir != nil {
				t.Fatalf("resolve plugins dir: %v", errDir)
			}
			path, errPath := pluginstore.InstallPath(pluginsDir, runtime.GOOS, runtime.GOARCH, identityPluginID, "0.4.0")
			if errPath != nil {
				t.Fatalf("install path: %v", errPath)
			}

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- identityInstallRecorder(h) }()
			<-gate.reached
			h.mu.Lock()
			close(gate.release)
			identityWaitForBytes(t, path, installerBytes)
			wantBytes := tc.replace(t, pluginsDir, path)
			replaced, errStat := os.Stat(path)
			if errStat != nil {
				h.mu.Unlock()
				t.Fatalf("stat replacement: %v", errStat)
			}
			h.mu.Unlock()
			rec := <-done

			if rec.Code != http.StatusConflict {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
				t.Fatalf("decode rejection: %v", errUnmarshal)
			}
			if body.Error != "plugin_install_source_unverified" {
				t.Fatalf("unexpected rejection: %s", rec.Body.String())
			}
			if _, configured := h.cfg.Plugins.Configs[identityPluginID]; configured {
				t.Fatal("source-binding failure persisted a plugin config/receipt")
			}
			if context := h.PluginInstallationContext(identityPluginID); context.Qualified {
				t.Fatalf("source-binding failure produced a qualified installation: %+v", context)
			}
			if after := identityReadFile(t, path); !bytes.Equal(after, wantBytes) {
				t.Fatalf("fail-closed path changed the replacement bytes: %q", after)
			}
			if afterInfo, errStat := os.Stat(path); errStat != nil || !os.SameFile(replaced, afterInfo) {
				t.Fatalf("fail-closed path removed or replaced the other writer's file: err=%v", errStat)
			}
		})
	}
}

func TestInstallIdentityConcurrentCrossSourceNoFalseQualified(t *testing.T) {
	const (
		registryA = "https://registry-a.example/store.json"
		registryB = "https://registry-b.example/store.json"
		artifactA = "https://downloads-a.example/plugin.zip"
		artifactB = "https://downloads-b.example/plugin.zip"
	)
	bytesA := []byte("plugin-bytes-from-source-a")
	bytesB := []byte("plugin-bytes-from-source-b")
	name := identityPluginID + managementPluginExtension(runtime.GOOS)
	archiveA := makeManagementPluginStoreZip(t, name, string(bytesA))
	archiveB := makeManagementPluginStoreZip(t, name, string(bytesB))
	digestA := sha256.Sum256(archiveA)
	digestB := sha256.Sum256(archiveB)
	sourceA := pluginstore.SourceID(registryA)
	sourceB := pluginstore.SourceID(registryB)
	expectedBytes := map[string][]byte{
		sourceA: bytesA,
		sourceB: bytesB,
	}

	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir:          t.TempDir(),
			StoreSources: []string{registryA, registryB},
		}},
		configFilePath: writeTestConfigFile(t),
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			registryA: directRegistryJSON(artifactA, hex.EncodeToString(digestA[:])),
			registryB: directRegistryJSON(artifactB, hex.EncodeToString(digestB[:])),
			artifactA: archiveA,
			artifactB: archiveB,
		},
	}

	installSource := func(sourceID string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: identityPluginID}}
		c.Set(ConfigV8ContextKey, true)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/plugin-store/"+identityPluginID+"/install?source="+sourceID, nil)
		h.InstallPluginFromStore(c)
		return rec
	}

	var wg sync.WaitGroup
	for _, src := range []string{sourceA, sourceB, sourceA, sourceB} {
		src := src
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := installSource(src)
			if rec.Code != http.StatusOK && rec.Code != http.StatusConflict {
				t.Errorf("install(%s) status=%d body=%s", src, rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if ctx := h.PluginInstallationContext(identityPluginID); ctx.Qualified {
		want, ok := expectedBytes[ctx.SourceID]
		if !ok {
			t.Fatalf("qualified installation has unknown source %q", ctx.SourceID)
		}
		if onDisk := identityReadFile(t, ctx.Path); !bytes.Equal(onDisk, want) {
			t.Fatalf("false-qualified installation: source=%s want=%q got=%q", ctx.SourceID, want, onDisk)
		}
	}
}
