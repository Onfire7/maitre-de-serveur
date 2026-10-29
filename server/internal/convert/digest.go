package convert

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Resolver pins an image reference to the digest its tag names now.
type Resolver interface {
	Digest(ctx context.Context, image string) (string, error)
}

// Registry resolves against the image's registry, with the credentials
// `podman login` or `docker login` left in the usual places.
type Registry struct{}

// Digest returns sha256:... for image's tag: the multi-platform index when
// there is one, so a Node of any architecture pulls its own.
func (Registry) Digest(ctx context.Context, image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
}

// pin returns image as repository:tag@sha256:... An image already pinned
// keeps its digest; one without a tag is pinned at latest, as Compose pulls.
func pin(ctx context.Context, r Resolver, image string) (string, error) {
	if strings.Contains(image, "@sha256:") {
		return image, nil
	}
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", image, err)
	}
	// Registry and tag made explicit (docker.io/library/..., :latest), in
	// the words people use: docker.io, not the index.docker.io it serves from.
	full := ref.Name()
	if rest, ok := strings.CutPrefix(full, name.DefaultRegistry+"/"); ok {
		full = "docker.io/" + rest
	}
	digest, err := r.Digest(ctx, full)
	if err != nil {
		return "", fmt.Errorf("image %s: %w", full, err)
	}
	return full + "@" + digest, nil
}

// Tags lists the tags of image's repository.
func (Registry) Tags(ctx context.Context, image string) ([]string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return nil, err
	}
	return remote.List(ref.Context(), remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

// Labeller reads an image's labels. Registry is one; a Resolver that is
// not (as in tests) leaves the entry's project for a person.
type Labeller interface {
	Labels(ctx context.Context, image string) (map[string]string, error)
}

// Labels are the labels of image's configuration: for a multi-platform
// index, those of the platform this runs on, which publishers label alike.
func (Registry) Labels(ctx context.Context, image string) (map[string]string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return nil, err
	}
	img, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	return cfg.Config.Labels, nil
}
