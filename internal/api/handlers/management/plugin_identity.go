package management

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginstore"
)

// PluginInstallationContext is the host-owned, read-time view of one managed
// plugin installation. Qualified is true only when the persisted, owner-written
// install receipt still describes the artifact that is actually on disk and the
// recorded source/version still matches the store manifest.
//
// This is an installation snapshot, not a live execution fence: a plugin image
// that was already loaded from a replaced file is not revoked by this read.
type PluginInstallationContext struct {
	Contract   int
	PluginID   string
	InstanceID string
	Version    string
	SourceID   string
	SourceURL  string
	Path       string
	Qualified  bool
	Reason     string
}

// PluginInstallationContext returns a consistent installation-instance view.
// Missing configuration, a missing receipt, a changed source/version, changed
// bytes, or a replaced file instance all return Qualified=false.
func (h *Handler) PluginInstallationContext(id string) PluginInstallationContext {
	id = strings.TrimSpace(id)
	out := PluginInstallationContext{Contract: pluginstore.InstallRecordContract, PluginID: id}
	if h == nil {
		out.Reason = "handler_unavailable"
		return out
	}
	h.mu.Lock()
	cfg := h.cfg
	var item config.PluginInstanceConfig
	configured := false
	if cfg != nil {
		item, configured = cfg.Plugins.Configs[id]
	}
	h.mu.Unlock()
	if cfg == nil {
		out.Reason = "config_unavailable"
		return out
	}
	if id == "" || !configured {
		out.Reason = "plugin_not_configured"
		return out
	}
	storeNode := yamlMappingValue(&item.Raw, "store")
	if storeNode == nil || storeNode.Kind == 0 {
		out.Reason = "not_store_managed"
		return out
	}
	var manifest pluginstore.Manifest
	if errDecode := storeNode.Decode(&manifest); errDecode != nil {
		out.Reason = "store_manifest_invalid"
		return out
	}
	if strings.TrimSpace(manifest.ID) == "" {
		manifest.ID = id
	}
	record, ok, errRecord := pluginstore.DecodeInstallRecord(yamlMappingValue(&item.Raw, "install"))
	if errRecord != nil {
		out.Reason = "install_record_invalid"
		return out
	}
	if !ok {
		out.Reason = "install_record_missing"
		return out
	}
	out.InstanceID = record.InstanceID
	out.Version = record.Version
	out.SourceID = record.SourceID
	out.SourceURL = record.SourceURL
	out.Path = record.Path
	if strings.TrimSpace(manifest.ID) != id {
		out.Reason = "store_manifest_plugin_id_changed"
		return out
	}
	if strings.TrimSpace(manifest.Version) != strings.TrimSpace(record.Version) {
		out.Reason = "store_manifest_version_changed"
		return out
	}
	if strings.TrimSpace(manifest.SourceID) != strings.TrimSpace(record.SourceID) {
		out.Reason = "store_manifest_source_id_changed"
		return out
	}
	if strings.TrimSpace(manifest.SourceURL) != strings.TrimSpace(record.SourceURL) {
		out.Reason = "store_manifest_source_url_changed"
		return out
	}
	pluginsDir, errResolve := config.ResolvePluginsDir(normalizedPluginsDir(cfg.Plugins.Dir))
	if errResolve != nil {
		out.Reason = "plugin_directory_invalid"
		return out
	}
	expectedPath, errPath := pluginstore.InstallPath(pluginsDir, runtime.GOOS, runtime.GOARCH, id, record.Version)
	if errPath != nil {
		out.Reason = "install_path_invalid"
		return out
	}
	expectedAbs, errAbs := filepath.Abs(expectedPath)
	if errAbs != nil {
		out.Reason = "install_path_invalid"
		return out
	}
	if errVerify := record.Verify(id, filepath.Clean(expectedAbs), record.Version, manifest.SourceID, manifest.SourceURL); errVerify != nil {
		out.Reason = errVerify.Error()
		return out
	}
	out.Qualified = true
	return out
}
