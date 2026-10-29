// Package catalogsync keeps a Console's clone of a git Catalog (plan C9)
// at the head of its branch. The clone is the Console's own copy: nothing in
// it is kept across a sync but what the repository holds.
package catalogsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// Sync brings dir to the head of url's branch (the repository's default
// branch when empty): a shallow clone the first time, then a fetch and a hard
// reset. It returns the commit dir is at.
func Sync(ctx context.Context, url, branch, dir string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err == nil && !sameRemote(repo, url) {
		// Another repository was cloned here: start again.
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		err = git.ErrRepositoryNotExists
	}
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return clone(ctx, url, branch, dir)
	}
	if err != nil {
		return "", err
	}
	if branch == "" {
		head, err := repo.Head()
		if err != nil {
			return "", err
		}
		branch = head.Name().Short()
	}
	remote := plumbing.NewRemoteReferenceName("origin", branch)
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf("+refs/heads/%s:%s", branch, remote))},
		Depth:    1,
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	ref, err := repo.Reference(remote, true)
	if err != nil {
		return "", err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		return "", err
	}
	if err := wt.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

func clone(ctx context.Context, url, branch, dir string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	opts := &git.CloneOptions{URL: url, Depth: 1, SingleBranch: true}
	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}
	repo, err := git.PlainCloneContext(ctx, dir, false, opts)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("clone %s: %w", url, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

func sameRemote(repo *git.Repository, url string) bool {
	r, err := repo.Remote("origin")
	return err == nil && len(r.Config().URLs) > 0 && r.Config().URLs[0] == url
}
