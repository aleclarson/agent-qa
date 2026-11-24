package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

func (a app) listTargets(ctx context.Context, cwd string) error {
	repo, err := findRepository(ctx, cwd)
	if err != nil {
		return err
	}
	targets, err := loadConfig(repo.root)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.out, "Git common directory:", repo.common); err != nil {
		return err
	}
	out := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "TARGET\tPREP\tMANAGED SERVER\tAPP URL\tDEVICE")
	for _, name := range sortedKeys(targets) {
		t := targets[name]
		var steps []string
		if t.Dependencies != nil {
			steps = append(steps, "dependencies")
		}
		if t.Build != nil {
			steps = append(steps, "build")
		}
		prep := strings.Join(steps, ", ")
		if prep == "" {
			prep = "-"
		}
		server, appURL := "no", "-"
		if t.Server != nil {
			server, appURL = "yes", t.Server.appURL()
		}
		device := "-"
		if selector := t.Settings["device"]; selector != "" {
			device = strconv.Quote(selector)
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", name, prep, server, appURL, device)
	}
	return out.Flush()
}

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
		argv := op.argv
		if slices.ContainsFunc(argv, placeholder.MatchString) {
			if targets == nil {
				targets, err = loadConfig(repo.root)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			settings := targets[name].Settings
			argv = slices.Clone(argv)
			for i, arg := range argv {
				argv[i] = placeholder.ReplaceAllStringFunc(arg, func(token string) string {
					if value, ok := settings[token[2:len(token)-1]]; ok {
						return value
					}
					return token
				})
			}
		}
		return a.runCommand(ctx, w.tree, argv, nil)
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
