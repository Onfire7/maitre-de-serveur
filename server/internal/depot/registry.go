// Package depot is the developer-side image and netboot server (decisions
// 79 to 88): one process, no containers. This file is the read-only OCI
// distribution registry, served straight from OCI image layouts on disk.
//
// Publishing is `podman push <image> oci:<dir>/images/<name>:<tag>` (or
// skopeo copy). Each image name is one OCI layout directory whose index.json
// lists manifests tagged through the org.opencontainers.image.ref.name
// annotation. Nodes pull with docker://<depot>:<port>/<name>:<tag>.
package depot

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Registry serves the pull half of the OCI distribution API from Dir.
type Registry struct {
	// Dir holds one OCI layout directory per image name.
	Dir string
}

// Images lists the image names and tags present in Dir.
func (r *Registry) Images() (map[string][]string, error) {
	out := map[string][]string{}
	entries, err := os.ReadDir(r.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		idx, err := r.index(e.Name())
		if err != nil {
			continue
		}
		var tags []string
		for _, m := range idx.Manifests {
			if t := m.Annotations["org.opencontainers.image.ref.name"]; t != "" {
				tags = append(tags, t)
			}
		}
		sort.Strings(tags)
		out[e.Name()] = tags
	}
	return out, nil
}

// TagInfo describes one tagged image for the Console.
type TagInfo struct {
	Tag     string `json:"tag"`
	Digest  string `json:"digest"`
	Version string `json:"version,omitempty"`
	Created string `json:"created,omitempty"`
}

// Describe lists every image's tags with version label and build time,
// read from each manifest's config blob.
func (r *Registry) Describe() (map[string][]TagInfo, error) {
	imgs, err := r.Images()
	if err != nil {
		return nil, err
	}
	out := map[string][]TagInfo{}
	for name, tags := range imgs {
		idx, err := r.index(name)
		if err != nil {
			continue
		}
		for _, tag := range tags {
			ti := TagInfo{Tag: tag}
			for _, m := range idx.Manifests {
				if m.Annotations["org.opencontainers.image.ref.name"] == tag {
					ti.Digest = m.Digest
					ti.Version, ti.Created = r.configInfo(name, m.Digest)
					break
				}
			}
			out[name] = append(out[name], ti)
		}
	}
	return out, nil
}

// configInfo follows a manifest to its config blob for the version label
// and creation time. Empty strings when anything is missing.
func (r *Registry) configInfo(name, manifestDigest string) (version, created string) {
	mb, err := os.ReadFile(r.blobPath(name, manifestDigest))
	if err != nil {
		return "", ""
	}
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(mb, &m) != nil || m.Config.Digest == "" {
		return "", ""
	}
	cb, err := os.ReadFile(r.blobPath(name, m.Config.Digest))
	if err != nil {
		return "", ""
	}
	var c struct {
		Created string `json:"created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if json.Unmarshal(cb, &c) != nil {
		return "", ""
	}
	return c.Config.Labels["org.opencontainers.image.version"], c.Created
}

type ociIndex struct {
	Manifests []ociDescriptor `json:"manifests"`
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

var (
	nameRE   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	tagRE    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

func (r *Registry) index(name string) (*ociIndex, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, name, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx ociIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("%s/index.json: %w", name, err)
	}
	return &idx, nil
}

func (r *Registry) blobPath(name, digest string) string {
	return filepath.Join(r.Dir, name, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

// ServeHTTP implements the /v2/ routes. Anything else is 404.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only registry", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	if path == "" || path == "/" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
		return
	}
	// <name>/manifests/<ref> | <name>/blobs/<digest> | <name>/tags/list
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		http.NotFound(w, req)
		return
	}
	ref := parts[len(parts)-1]
	kind := parts[len(parts)-2]
	name := strings.Join(parts[:len(parts)-2], "/")
	if !nameRE.MatchString(name) {
		registryError(w, http.StatusNotFound, "NAME_INVALID", "invalid repository name")
		return
	}
	switch kind {
	case "manifests":
		r.serveManifest(w, req, name, ref)
	case "blobs":
		r.serveBlob(w, req, name, ref)
	case "tags":
		if ref != "list" {
			http.NotFound(w, req)
			return
		}
		r.serveTags(w, name)
	default:
		http.NotFound(w, req)
	}
}

func (r *Registry) serveManifest(w http.ResponseWriter, req *http.Request, name, ref string) {
	idx, err := r.index(name)
	if err != nil {
		registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	var desc *ociDescriptor
	for i := range idx.Manifests {
		m := &idx.Manifests[i]
		if m.Digest == ref || m.Annotations["org.opencontainers.image.ref.name"] == ref {
			desc = m
			break
		}
	}
	if desc == nil && digestRE.MatchString(ref) {
		// Nested manifests of an index are not in index.json; serve them as
		// blobs and sniff the media type.
		if b, err := os.ReadFile(r.blobPath(name, ref)); err == nil {
			desc = &ociDescriptor{Digest: ref, Size: int64(len(b)), MediaType: sniffMediaType(b)}
		}
	}
	if desc == nil {
		if !tagRE.MatchString(ref) && !digestRE.MatchString(ref) {
			registryError(w, http.StatusNotFound, "MANIFEST_INVALID", "invalid reference")
			return
		}
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	f, err := os.Open(r.blobPath(name, desc.Digest))
	if err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest blob missing")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", desc.MediaType)
	w.Header().Set("Docker-Content-Digest", desc.Digest)
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	if req.Method == http.MethodHead {
		return
	}
	http.ServeContent(w, req, "", st.ModTime(), f)
}

func (r *Registry) serveBlob(w http.ResponseWriter, req *http.Request, name, digest string) {
	if !digestRE.MatchString(digest) {
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "provided digest did not match uploaded content")
		return
	}
	f, err := os.Open(r.blobPath(name, digest))
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	if req.Method == http.MethodHead {
		return
	}
	http.ServeContent(w, req, "", st.ModTime(), f)
}

func (r *Registry) serveTags(w http.ResponseWriter, name string) {
	imgs, err := r.Images()
	tags, ok := imgs[name]
	if err != nil || !ok {
		registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	if tags == nil {
		tags = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "tags": tags})
}

func sniffMediaType(b []byte) string {
	var m struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(b, &m) == nil && m.MediaType != "" {
		return m.MediaType
	}
	return "application/vnd.oci.image.manifest.v1+json"
}

func registryError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": msg}}})
}
