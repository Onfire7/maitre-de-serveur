package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Decision 76: installer media carries the image it was built from in a
// partition labelled maitre-image (an OCI layout at image/, tag "media",
// plus image-ref.txt). The Installer uses it when the registry has the same
// digest or cannot be reached, and pulls only when the registry is newer.

// LocalImageLabel is the partition label installer-media.sh writes.
const LocalImageLabel = "maitre-image"

// LocalImage is a baked image found on the boot media.
type LocalImage struct {
	// Source is the bootc --source-imgref for it, e.g. oci:/run/maitre-image/image:media.
	Source string
	// Ref is the registry reference it was built from.
	Ref string
	// Digest of its manifest (sha256:...).
	Digest string
}

var localMount = "/run/maitre-image"

// FindLocalImage mounts the maitre-image partition if present and reads it.
// Returns nil, nil when the media carries no image.
func FindLocalImage(ctx context.Context) (*LocalImage, error) {
	dev := filepath.Join("/dev/disk/by-label", LocalImageLabel)
	if _, err := os.Stat(dev); err != nil {
		return nil, nil
	}
	if err := os.MkdirAll(localMount, 0o755); err != nil {
		return nil, err
	}
	if out, err := exec.CommandContext(ctx, "mountpoint", "-q", localMount).CombinedOutput(); err != nil {
		_ = out
		if out, err := exec.CommandContext(ctx, "mount", "-o", "ro", dev, localMount).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("mount %s: %w: %s", dev, err, strings.TrimSpace(string(out)))
		}
	}
	return readLocalImage(localMount)
}

func readLocalImage(dir string) (*LocalImage, error) {
	refB, err := os.ReadFile(filepath.Join(dir, "image-ref.txt"))
	if err != nil {
		return nil, fmt.Errorf("media image: %w", err)
	}
	idxB, err := os.ReadFile(filepath.Join(dir, "image", "index.json"))
	if err != nil {
		return nil, fmt.Errorf("media image: %w", err)
	}
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(idxB, &idx); err != nil {
		return nil, fmt.Errorf("media image index: %w", err)
	}
	li := &LocalImage{Source: "oci:" + filepath.Join(dir, "image") + ":media", Ref: strings.TrimSpace(string(refB))}
	for _, m := range idx.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == "media" || li.Digest == "" {
			li.Digest = m.Digest
		}
	}
	if li.Digest == "" {
		return nil, fmt.Errorf("media image index lists no manifest")
	}
	return li, nil
}

// RegistryDigest asks the registry for the manifest digest of ref; "" with
// an error when it cannot be reached.
func RegistryDigest(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+ref).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ChooseSource decides where the install comes from (decision 76): the
// media's image when the registry agrees or is unreachable, else the
// registry. Returns the --source-imgref and a sentence for the screen.
func ChooseSource(ctx context.Context, ref string, local *LocalImage) (source, why string) {
	if local == nil {
		return "docker://" + ref, "downloading from " + ref
	}
	remote, err := RegistryDigest(ctx, ref)
	switch {
	case err != nil:
		return local.Source, "registry unreachable; installing the copy on this media"
	case remote == local.Digest:
		return local.Source, "this media already holds the current image; no download"
	default:
		return "docker://" + ref, "the registry has a newer image; downloading from " + ref
	}
}
