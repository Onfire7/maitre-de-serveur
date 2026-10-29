// Package popularity reads the stars of a Catalog entry's project
// (docs/popularity-and-grants.md, P3): GitHub's, Codeberg's (Forgejo's API)
// or GitLab's count, the approximation of popularity the Console sorts by.
package popularity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Onfire7/maitre-de-serveur/server/internal/catalog"
)

// Fetcher asks the project hosts. The bases are swapped in tests.
type Fetcher struct {
	Client                   *http.Client
	GitHub, Codeberg, GitLab string
	// GitHubToken lifts GitHub's limit of 60 anonymous calls an hour.
	GitHubToken string
}

// Default asks the real hosts, with GITHUB_TOKEN when it is set.
func Default() *Fetcher {
	return &Fetcher{
		Client:      &http.Client{Timeout: 30 * time.Second},
		GitHub:      "https://api.github.com",
		Codeberg:    "https://codeberg.org",
		GitLab:      "https://gitlab.com",
		GitHubToken: os.Getenv("GITHUB_TOKEN"),
	}
}

// Stars is the star count of project (an entry's project: address).
func (f *Fetcher) Stars(ctx context.Context, project string) (int, error) {
	host, repo, err := catalog.ProjectRepo(project)
	if err != nil {
		return 0, err
	}
	var api, field string
	switch host {
	case "github.com":
		api, field = f.GitHub+"/repos/"+repo, "stargazers_count"
	case "codeberg.org":
		api, field = f.Codeberg+"/api/v1/repos/"+repo, "stars_count"
	case "gitlab.com":
		api, field = f.GitLab+"/api/v4/projects/"+url.PathEscape(repo), "star_count"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if host == "github.com" && f.GitHubToken != "" {
		req.Header.Set("Authorization", "Bearer "+f.GitHubToken)
	}
	res, err := f.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", project, res.Status)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("%s: %w", project, err)
	}
	n, ok := body[field].(float64)
	if !ok {
		return 0, fmt.Errorf("%s: no %s in the answer", project, field)
	}
	return int(n), nil
}
