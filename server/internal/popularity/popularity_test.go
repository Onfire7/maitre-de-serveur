package popularity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStars(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/repos/home-assistant/core":
			auth = r.Header.Get("Authorization")
			w.Write([]byte(`{"stargazers_count": 81234}`))
		case "/api/v1/repos/forgejo/forgejo":
			w.Write([]byte(`{"stars_count": 3000}`))
		case "/api/v4/projects/group%2Fsub%2Fproject":
			w.Write([]byte(`{"star_count": 12}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), GitHub: srv.URL, Codeberg: srv.URL, GitLab: srv.URL, GitHubToken: "t"}
	for project, want := range map[string]int{
		"https://github.com/home-assistant/core": 81234,
		"https://codeberg.org/forgejo/forgejo":   3000,
		"https://gitlab.com/group/sub/project":   12,
	} {
		if n, err := f.Stars(context.Background(), project); err != nil || n != want {
			t.Errorf("%s: %d, %v", project, n, err)
		}
	}
	if auth != "Bearer t" {
		t.Errorf("GitHub token not sent: %q", auth)
	}
	if _, err := f.Stars(context.Background(), "https://github.com/gone/away"); err == nil {
		t.Error("a missing repository has stars")
	}
}
