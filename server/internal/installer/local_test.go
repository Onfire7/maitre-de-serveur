package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadLocalImageAndChoose(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "image"), 0o755)
	os.WriteFile(filepath.Join(dir, "image-ref.txt"), []byte("ghcr.io/onfire7/maitre-server:main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "image", "index.json"), []byte(`{"manifests":[{"digest":"sha256:abc","annotations":{"org.opencontainers.image.ref.name":"media"}}]}`), 0o644)
	li, err := readLocalImage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if li.Ref != "ghcr.io/onfire7/maitre-server:main" || li.Digest != "sha256:abc" || li.Source != "oci:"+filepath.Join(dir, "image")+":media" {
		t.Errorf("%+v", li)
	}
	// No skopeo reachable registry in tests: the media copy wins.
	src, why := ChooseSource(context.Background(), "127.0.0.1:1/x:y", li)
	if src != li.Source || why == "" {
		t.Errorf("%q %q", src, why)
	}
	if src, _ := ChooseSource(context.Background(), "a/b:c", nil); src != "docker://a/b:c" {
		t.Errorf("no local: %q", src)
	}
}
