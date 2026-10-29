// Package icons copies an app's icon from an icon pack into a Catalog entry
// (plan C12): homarr-labs/dashboard-icons first, selfh.st/icons second, an
// SVG before a PNG, each from the commit its pack's main branch is at, so
// the URL recorded in the entry names exactly the file copied.
package icons

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

// Pack is an icon pack the Catalog may copy from.
type Pack struct {
	Name, Repo, Licence string
}

// Packs in the order they are tried.
var Packs = []Pack{
	{Name: "dashboard-icons", Repo: "homarr-labs/dashboard-icons", Licence: "Apache-2.0"},
	{Name: "selfh.st/icons", Repo: "selfhst/icons", Licence: "CC-BY-4.0"},
}

// ErrNotFound is an app no pack has an icon for.
var ErrNotFound = errors.New("no icon pack has this app")

// Fetcher reads icons over HTTPS. Base is GitHub's raw file host and API,
// swapped in tests.
type Fetcher struct {
	Client  *http.Client
	RawBase string // https://raw.githubusercontent.com
	APIBase string // https://api.github.com
}

// Default fetches from GitHub.
func Default() *Fetcher {
	return &Fetcher{Client: &http.Client{Timeout: 30 * time.Second}, RawBase: "https://raw.githubusercontent.com", APIBase: "https://api.github.com"}
}

// Icon is one copied icon: its bytes, its file name in the entry
// (icon.svg or icon.png), and the credit the manifest records.
type Icon struct {
	Body   []byte
	File   string
	Credit catalog.IconCredit
}

// maxIcon keeps the bundled index small; pack icons are a few kilobytes.
const maxIcon = 256 << 10

// Fetch finds slug (an app's name in the packs, such as "paperless-ngx").
func (f *Fetcher) Fetch(ctx context.Context, slug string) (*Icon, error) {
	if slug == "" || strings.ContainsAny(slug, "/.\\ ") {
		return nil, fmt.Errorf("icon name %q", slug)
	}
	for _, p := range Packs {
		sha, err := f.head(ctx, p.Repo)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		for _, ext := range []string{"svg", "png"} {
			url := fmt.Sprintf("%s/%s/%s/%s/%s.%s", f.RawBase, p.Repo, sha, ext, slug, ext)
			body, err := f.get(ctx, url, "")
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if len(body) > maxIcon {
				return nil, fmt.Errorf("%s is %d KiB; icons over %d KiB are not copied", url, len(body)>>10, maxIcon>>10)
			}
			return &Icon{Body: body, File: "icon." + ext, Credit: catalog.IconCredit{Pack: p.Name, Licence: p.Licence, URL: url}}, nil
		}
	}
	return nil, ErrNotFound
}

// head is the commit a pack's main branch is at.
func (f *Fetcher) head(ctx context.Context, repo string) (string, error) {
	b, err := f.get(ctx, f.APIBase+"/repos/"+repo+"/commits/main", "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(b))
	if len(sha) != 40 {
		return "", fmt.Errorf("unexpected commit %q", sha)
	}
	return sha, nil
}

func (f *Fetcher) get(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxIcon+1))
}
