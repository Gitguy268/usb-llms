package drive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModeDefaultsFollowModels(t *testing.T) {
	d, err := Find(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !d.Online() {
		t.Error("a drive with no models should be online so it can fetch one")
	}
	manifest := filepath.Join(d.Models(), "manifests", "registry", "lib", "m", "latest")
	os.MkdirAll(filepath.Dir(manifest), 0o755)
	os.WriteFile(manifest, []byte("{}"), 0o644)
	if d.Online() {
		t.Error("a drive with a model should default to offline")
	}
	if err := d.SaveConfig(Config{Mode: "online"}); err != nil {
		t.Fatal(err)
	}
	if !d.Online() {
		t.Error("an explicit online choice should win")
	}
}

func TestDamagedConfigMeansOffline(t *testing.T) {
	d, _ := Find(t.TempDir())
	os.WriteFile(d.ConfigPath(), []byte("{not json"), 0o644)
	if d.Online() {
		t.Error("a damaged config must fail closed")
	}
}

func TestCleanAppleFiles(t *testing.T) {
	d, _ := Find(t.TempDir())
	junk := filepath.Join(d.Models(), "manifests", "._latest")
	keep := filepath.Join(d.Models(), "manifests", "latest")
	os.MkdirAll(filepath.Dir(junk), 0o755)
	os.WriteFile(junk, nil, 0o644)
	os.WriteFile(keep, nil, 0o644)
	d.CleanAppleFiles()
	if _, err := os.Stat(junk); err == nil {
		t.Error("._ file should be gone")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("real manifest should stay")
	}
}
