package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type repository struct {
	root   string
	common string
}

type workspace struct {
	dir  string
	tree string
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	args = append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = projectEnv(dir)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", context.Cause(ctx)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[4], strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[4], err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

func projectEnv(dir string) []string {
	// Git hooks can export repository-local variables pointing back at the
	// calling checkout. Child commands should discover their own worktree.
	local := []string{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_DIR", "GIT_GRAFT_FILE",
		"GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_NO_REPLACE_OBJECTS",
		"GIT_OBJECT_DIRECTORY", "GIT_PREFIX", "GIT_REPLACE_REF_BASE",
		"GIT_SHALLOW_FILE", "GIT_WORK_TREE",
	}
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return key == "PWD" || slices.Contains(local, key)
	})
	return append(env, "PWD="+dir)
}

func findRepository(ctx context.Context, cwd string) (repository, error) {
	root, err := git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return repository{}, err
	}
	common, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repository{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	return repository{root: root, common: common}, err
}

func (r repository) target(name string) workspace {
	dir := filepath.Join(r.common, "agent-qa", name)
	return workspace{dir: dir, tree: filepath.Join(dir, "worktree")}
}

func (w workspace) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(w.dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(w.dir, "reservation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if ctx.Err() != nil {
			f.Close()
			return nil, context.Cause(ctx)
		}
		acquired, err := tryLock(f)
		if acquired && err == nil {
			return f, nil
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		if err := pause(ctx, 75*time.Millisecond); err != nil {
			f.Close()
			return nil, err
		}
	}
}

func tryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch err {
		case nil:
			return true, nil
		case syscall.EWOULDBLOCK:
			return false, nil
		case syscall.EINTR:
			continue
		default:
			return false, err
		}
	}
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func (w workspace) exists(ctx context.Context) (bool, error) {
	if _, err := os.Lstat(w.tree); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	common, err := git(ctx, w.tree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return false, err
	}
	if common != filepath.Dir(filepath.Dir(w.dir)) {
		return false, fmt.Errorf("%s belongs to a different Git repository", w.tree)
	}
	root, err := git(ctx, w.tree, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, err
	}
	if root != w.tree {
		return false, fmt.Errorf("%s is not the root of the QA worktree", w.tree)
	}
	return true, nil
}

func (w workspace) requireClean(ctx context.Context) error {
	status, err := git(ctx, w.tree, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("QA worktree is dirty at %s:\n%s", w.tree, status)
	}
	return nil
}

func resolveCommit(ctx context.Context, dir, ref string) (string, error) {
	return git(ctx, dir, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

func fingerprint(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err) // Only internal JSON-compatible structs are fingerprinted.
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type preparation struct {
	step      *step
	statePath string
	key       string
	needed    bool
}

func (w workspace) planStep(ctx context.Context, name string, s *step, head, dependencyKey string) (preparation, error) {
	if s == nil {
		return preparation{}, nil
	}
	inputs := head
	if len(s.Inputs) > 0 {
		// ls-files applies Git's glob pathspec semantics and includes each
		// matching tracked entry's mode and object ID (including gitlinks).
		args := []string{"ls-files", "--stage", "-z", "--"}
		for _, glob := range s.Inputs {
			args = append(args, ":(top,glob)"+glob)
		}
		var err error
		inputs, err = git(ctx, w.tree, args...)
		if err != nil {
			return preparation{}, err
		}
	}
	p := preparation{
		step: s, statePath: filepath.Join(w.dir, name+".json"),
		key: fingerprint(struct {
			Schema     int
			Setup      *step
			Inputs     string
			Dependency string
		}{1, s, inputs, dependencyKey}),
	}
	var previous string
	p.needed = readJSON(p.statePath, &previous) != nil || previous != p.key || w.checkOutputs(s) != nil
	return p, nil
}

func (w workspace) checkOutputs(s *step) error {
	for _, path := range s.Outputs {
		if _, err := os.Stat(filepath.Join(w.tree, path)); err != nil {
			return fmt.Errorf("expected output %q: %w", path, err)
		}
	}
	return nil
}

func (a app) prepare(ctx context.Context, w workspace, name string, p preparation, head string) error {
	if !p.needed {
		return nil
	}
	// Invalidate prior success before a command can modify its outputs.
	if err := os.Remove(p.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(a.err, "agent-qa: preparing %s in %s\n", name, w.tree)
	if err := a.runCommand(ctx, w.tree, p.step.Command); err != nil {
		return fmt.Errorf("%s preparation: %w", name, err)
	}
	if err := w.requireClean(ctx); err != nil {
		return err
	}
	current, err := resolveCommit(ctx, w.tree, "HEAD")
	if err != nil {
		return err
	}
	if current != head {
		return fmt.Errorf("%s preparation changed the QA checkout from %s to %s", name, head, current)
	}
	if err := w.checkOutputs(p.step); err != nil {
		return err
	}
	return writeJSON(p.statePath, p.key, true, 0600)
}
