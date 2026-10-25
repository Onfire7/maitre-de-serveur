package depot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// writeLayout builds a minimal OCI layout with one tagged manifest and one layer.
func writeLayout(t *testing.T, dir, tag string) (manifestDigest, layerDigest string) {
	t.Helper()
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(b []byte) string {
		sum := sha256.Sum256(b)
		d := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(blobs, d), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return "sha256:" + d
	}
	layerDigest = put([]byte("layer bytes"))
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"layers": []map[string]any{{"digest": layerDigest, "size": 11, "mediaType": "application/vnd.oci.image.layer.v1.tar"}},
	})
	manifestDigest = put(manifest)
	index, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": manifestDigest, "size": len(manifest),
			"annotations": map[string]string{"org.opencontainers.image.ref.name": tag},
		}},
	})
	if err := os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRegistry(t *testing.T) {
	root := t.TempDir()
	md, ld := writeLayout(t, filepath.Join(root, "maitre-server"), "dev")
	srv := httptest.NewServer(&Registry{Dir: root})
	defer srv.Close()

	get := func(p string) (*http.Response, []byte) {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, b
	}
	if res, _ := get("/v2/"); res.StatusCode != 200 || res.Header.Get("Docker-Distribution-API-Version") != "registry/2.0" {
		t.Errorf("/v2/ = %v", res.Status)
	}
	res, b := get("/v2/maitre-server/manifests/dev")
	if res.StatusCode != 200 || res.Header.Get("Docker-Content-Digest") != md || res.Header.Get("Content-Type") != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("manifest by tag: %v %v %s", res.Status, res.Header, b)
	}
	if res, _ := get("/v2/maitre-server/manifests/" + md); res.StatusCode != 200 {
		t.Errorf("manifest by digest: %v", res.Status)
	}
	if res, b := get("/v2/maitre-server/blobs/" + ld); res.StatusCode != 200 || string(b) != "layer bytes" || res.Header.Get("Docker-Content-Digest") != ld {
		t.Errorf("blob: %v %q", res.Status, b)
	}
	if res, b := get("/v2/maitre-server/tags/list"); res.StatusCode != 200 || string(b) != "{\"name\":\"maitre-server\",\"tags\":[\"dev\"]}\n" {
		t.Errorf("tags: %v %s", res.Status, b)
	}
	if res, _ := get("/v2/maitre-server/manifests/nope"); res.StatusCode != 404 {
		t.Errorf("unknown tag: %v", res.Status)
	}
	if res, _ := get("/v2/other/manifests/dev"); res.StatusCode != 404 {
		t.Errorf("unknown name: %v", res.Status)
	}
	if res, _ := get("/v2/maitre-server/blobs/../../index.json"); res.StatusCode == 200 {
		t.Errorf("path traversal served")
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v2/maitre-server/manifests/dev", nil)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 405 {
		t.Errorf("PUT: %v %v", res, err)
	}
	imgs, err := (&Registry{Dir: root}).Images()
	if err != nil || len(imgs["maitre-server"]) != 1 || imgs["maitre-server"][0] != "dev" {
		t.Errorf("Images = %v %v", imgs, err)
	}
}
