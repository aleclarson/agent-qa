package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

type app struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

const usage = `Usage:
  agent-qa init [--from -] [--update]
  agent-qa targets
  agent-qa doctor --json
  agent-qa up <target|all> --ref <sha>
  agent-qa run <target|all> -- <command> [args...]
  agent-qa restart <target|all>
  agent-qa stop <target|all>
  agent-qa servers [--json]
`

func main() {
	if len(os.Args) == 2 && os.Args[1] == supervisorArg {
		if err := serveManaged(); err != nil {
			fmt.Fprintln(os.Stderr, "agent-qa:", err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	if len(os.Args) > 1 && os.Args[1] != "init" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer signal.Stop(signals)
		go func() {
			select {
			case sig := <-signals:
				cancel(commandExit(128 + int(sig.(syscall.Signal))))
			case <-ctx.Done():
			}
		}()
	}
	a := app{os.Stdin, os.Stdout, os.Stderr}
	err := a.execute(ctx, os.Args[1:])
	cancel(nil)
	if err != nil {
		if _, silent := err.(commandExit); !silent {
			fmt.Fprintln(a.err, "agent-qa:", err)
		}
		os.Exit(exitStatus(err))
	}
}

func (a app) execute(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(a.out, usage)
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	command := args[0]
	switch command {
	case "doctor":
		if len(args) != 2 || args[1] != "--json" {
			return errors.New("usage: agent-qa doctor --json")
		}
		return a.doctor(ctx, cwd)
	case "init":
		return a.init(ctx, cwd, args[1:])
	case "targets":
		if len(args) != 1 {
			return errors.New("usage: agent-qa targets")
		}
		return a.listTargets(ctx, cwd)
	case "servers":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			return errors.New("usage: agent-qa servers [--json]")
		}
		return a.servers(ctx, cwd, len(args) == 2)
	case "restart", "stop":
		if len(args) != 2 {
			return fmt.Errorf("usage: agent-qa %s <target|all>", command)
		}
	case "up":
		if len(args) != 4 || args[2] != "--ref" || args[3] == "" {
			return errors.New("usage: agent-qa up <target|all> --ref <sha>")
		}
	case "run":
		if len(args) < 4 || args[2] != "--" {
			return errors.New("usage: agent-qa run <target|all> -- <command> [args...] (select a revision with up --ref)")
		}
	default:
		return fmt.Errorf("unknown command %q (see agent-qa --help)", command)
	}
	name := args[1]
	if !targetName.MatchString(name) {
		return fmt.Errorf("invalid target name %q", name)
	}
	repo, err := findRepository(ctx, cwd)
	if err != nil {
		return err
	}
	op := targetOperation{command: command}
	switch command {
	case "up":
		// Resolve moving refs once, including for up all.
		op.commit, err = resolveCommit(ctx, repo.root, args[3])
		if err != nil {
			return err
		}
	case "run":
		op.argv = args[3:]
	}
	if name == allTargets {
		return a.executeAll(ctx, repo, op)
	}
	return a.executeTarget(ctx, repo, name, op, nil)
}

func (a app) init(ctx context.Context, cwd string, flags []string) error {
	var fromStdin, update bool
	for len(flags) > 0 {
		switch flags[0] {
		case "--from":
			if fromStdin || len(flags) < 2 || flags[1] != "-" {
				return errors.New("usage: agent-qa init [--from -] [--update]")
			}
			fromStdin, flags = true, flags[2:]
		case "--update":
			if update {
				return errors.New("duplicate --update")
			}
			update, flags = true, flags[1:]
		default:
			return fmt.Errorf("unknown init option %q", flags[0])
		}
	}
	repo, err := findRepository(ctx, cwd)
	if err != nil {
		return err
	}
	path := filepath.Join(repo.root, configName)
	existsError := fmt.Errorf("%s already exists (use --update to replace it)", configName)
	if !update {
		if _, err := os.Lstat(path); err == nil {
			return existsError
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var c config
	if fromStdin {
		err = decodeJSON(a.in, &c)
	} else {
		c, err = interactiveConfig(a.in, a.err)
	}
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if _, err := c.resolve(localConfig{}); err != nil {
		return err
	}
	local, err := loadLocal(repo.root)
	if err != nil {
		return err
	}
	if _, err := c.resolve(local); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if err := writeJSON(path, c, update, 0644); errors.Is(err, os.ErrExist) {
		return existsError
	} else if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.out, path)
	return err
}

func (a app) up(ctx context.Context, repo repository, w workspace, name string, t target, desired string) error {
	exists, err := w.exists(ctx)
	if err != nil {
		return err
	}
	var head string
	if exists {
		if err := w.requireClean(ctx); err != nil {
			return err
		}
		head, err = resolveCommit(ctx, w.tree, "HEAD")
		if err != nil {
			return err
		}
	}
	owned, err := w.ownedServer()
	if err != nil {
		return err
	}
	if owned != nil && (head != desired || t.Server == nil || owned.Identity != serverIdentity(desired, t.Server)) {
		if err := w.stopServer(ctx); err != nil {
			return err
		}
	}
	if !exists {
		if _, err := git(ctx, repo.root, "worktree", "add", "--detach", w.tree, desired); err != nil {
			return err
		}
	} else {
		branch, err := git(ctx, w.tree, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		if desired != head || branch != "HEAD" {
			if _, err := git(ctx, w.tree, "checkout", "--no-overwrite-ignore", "--recurse-submodules", "--detach", desired); err != nil {
				return err
			}
		}
	}

	deps, err := w.planStep(ctx, "dependencies", t.Dependencies, desired, "")
	if err != nil {
		return err
	}
	if deps.needed {
		if err := w.stopServer(ctx); err != nil {
			return err
		}
		if err := a.prepare(ctx, w, "dependencies", deps, desired); err != nil {
			return err
		}
	}
	build, err := w.planStep(ctx, "build", t.Build, desired, deps.key)
	if err != nil {
		return err
	}
	if build.needed {
		if err := w.stopServer(ctx); err != nil {
			return err
		}
		if err := a.prepare(ctx, w, "build", build, desired); err != nil {
			return err
		}
	}
	if err := w.requireClean(ctx); err != nil {
		return err
	}
	if err := a.ensureServer(ctx, w, t.Server, desired); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.out, "Target: %s\nCommit: %s\nWorktree: %s\n", name, desired, w.tree); err != nil {
		return err
	}
	if t.Server != nil {
		_, err := fmt.Fprintln(a.out, "App URL:", t.Server.appURL())
		return err
	}
	return nil
}

func (a app) restart(ctx context.Context, w workspace, t target) error {
	if t.Server == nil {
		return errors.New("target has no managed server")
	}
	exists, err := w.exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("QA worktree is missing (run agent-qa up --ref first)")
	}
	if err := w.requireClean(ctx); err != nil {
		return err
	}
	head, err := resolveCommit(ctx, w.tree, "HEAD")
	if err != nil {
		return err
	}
	if err := w.stopServer(ctx); err != nil {
		return err
	}
	return a.ensureServer(ctx, w, t.Server, head)
}
