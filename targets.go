package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type targetOperation struct {
	command string
	commit  string
	argv    []string
}

func (a app) executeTarget(ctx context.Context, repo repository, name string, op targetOperation, targets map[string]target) error {
	w := repo.target(name)
	if op.command == "run" {
		exists, err := w.exists(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("QA worktree is missing (run agent-qa up %s --ref <sha> first)", name)
		}
		return a.runCommand(ctx, w.tree, op.argv, nil)
	}
	if op.command == "stop" {
		// A missing configuration or target with no state needs no setup.
		if _, err := os.Stat(w.dir); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	lock, err := w.lockMutation(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if op.command == "stop" {
		return w.stopServer(ctx)
	}
	if targets == nil {
		targets, err = loadConfig(repo.root)
		if err != nil {
			return err
		}
	}
	t, ok := targets[name]
	if !ok {
		return fmt.Errorf("unknown target %q", name)
	}
	if op.command == "restart" {
		return a.restart(ctx, w, t)
	}
	return a.up(ctx, repo, w, name, t, op.commit)
}

func (a app) executeAll(ctx context.Context, repo repository, op targetOperation) error {
	var names []string
	var targets map[string]target
	var err error
	if op.command == "stop" {
		names, err = repo.storedTargets()
	} else {
		targets, err = loadConfig(repo.root)
		for _, name := range sortedKeys(targets) {
			if targets[name].Server != nil {
				names = append(names, name)
			}
		}
	}
	if err != nil {
		return err
	}
	var firstStatus int
	for _, name := range names {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if op.command == "stop" {
			active, err := repo.target(name).serverActive()
			if err == nil && !active {
				continue
			}
		}
		if op.command == "run" {
			fmt.Fprintf(a.err, "agent-qa: running target %s\n", name)
		}
		if err := a.executeTarget(ctx, repo, name, op, targets); err != nil {
			fmt.Fprintf(a.err, "agent-qa: %s: %v\n", name, err)
			if firstStatus == 0 {
				firstStatus = exitStatus(err)
			}
		}
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if firstStatus != 0 {
		return commandExit(firstStatus)
	}
	return nil
}
