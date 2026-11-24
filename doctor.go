package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type doctorReport struct {
	OK           bool           `json:"ok"`
	Repository   string         `json:"repository,omitempty"`
	ConfigErrors []string       `json:"config_errors"`
	MissingTools []string       `json:"missing_tools"`
	Targets      []targetReport `json:"targets"`
}

type targetReport struct {
	Name          string        `json:"name"`
	Worktree      string        `json:"worktree"`
	Head          string        `json:"head,omitempty"`
	WorktreeReady bool          `json:"worktree_ready"`
	Dependencies  string        `json:"dependencies,omitempty"`
	Build         string        `json:"build,omitempty"`
	Ready         bool          `json:"ready"`
	MissingTools  []string      `json:"missing_tools"`
	Errors        []string      `json:"errors"`
	Server        *serverReport `json:"server,omitempty"`
}

type serverReport struct {
	Status       string `json:"status"`
	ReadinessURL string `json:"readiness_url"`
	Log          string `json:"log"`
	Owned        bool   `json:"owned"`
	PortConflict bool   `json:"port_conflict"`
}

func (a app) doctor(ctx context.Context, cwd string) error {
	report := doctorReport{OK: true, ConfigErrors: []string{}, MissingTools: []string{}, Targets: []targetReport{}}
	if _, err := exec.LookPath("git"); err != nil {
		report.MissingTools = append(report.MissingTools, "git")
		report.OK = false
	} else if repo, err := findRepository(ctx, cwd); err != nil {
		report.ConfigErrors = append(report.ConfigErrors, err.Error())
		report.OK = false
	} else {
		report.Repository = repo.root
		targets, err := loadConfig(repo.root)
		if err != nil {
			report.ConfigErrors = append(report.ConfigErrors, err.Error())
			report.OK = false
		} else {
			for _, name := range sortedKeys(targets) {
				t := inspectTarget(ctx, repo, name, targets[name])
				report.Targets = append(report.Targets, t)
				if len(t.Errors)+len(t.MissingTools) > 0 || t.Server != nil &&
					(t.Server.PortConflict || t.Server.Owned && t.Server.Status != "ready") {
					report.OK = false
				}
			}
		}
	}
	data, err := encodeJSON(report)
	if err != nil {
		return err
	}
	if _, err := a.out.Write(data); err != nil {
		return err
	}
	if !report.OK {
		return commandExit(1)
	}
	return nil
}

func inspectTarget(ctx context.Context, repo repository, name string, t target) targetReport {
	w := repo.target(name)
	report := targetReport{Name: name, Worktree: w.tree, MissingTools: []string{}, Errors: []string{}}
	exists, err := w.exists(ctx)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	toolDir := repo.root
	if exists {
		toolDir = w.tree
		report.Head, err = resolveCommit(ctx, w.tree, "HEAD")
		if err == nil {
			err = w.requireClean(ctx)
		}
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
		} else {
			report.WorktreeReady = true
		}
	}
	var dependencyKey string
	for _, item := range []struct {
		name   string
		step   *step
		status *string
	}{{"dependencies", t.Dependencies, &report.Dependencies}, {"build", t.Build, &report.Build}} {
		if item.step == nil {
			continue
		}
		*item.status = "pending"
		if !report.WorktreeReady {
			continue
		}
		p, err := w.planStep(ctx, item.name, item.step, report.Head, dependencyKey)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		if !p.needed && (item.name == "dependencies" || report.Dependencies != "pending") {
			*item.status = "ready"
		}
		if item.name == "dependencies" {
			dependencyKey = p.key
		}
	}
	tools := map[string]bool{}
	if t.Dependencies != nil {
		tools[t.Dependencies.Command[0]] = true
	}
	if t.Build != nil {
		tools[t.Build.Command[0]] = true
	}
	if t.Server != nil {
		tools[t.Server.Command[0]] = true
		report.Server, err = inspectServer(ctx, w, report.Head, t.Server)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
	}
	for _, name := range sortedKeys(tools) {
		if !toolAvailable(toolDir, name) {
			report.MissingTools = append(report.MissingTools, name)
		}
	}
	report.Ready = report.WorktreeReady && len(report.Errors)+len(report.MissingTools) == 0 &&
		report.Dependencies != "pending" && report.Build != "pending" &&
		(report.Server == nil || report.Server.Status == "ready")
	return report
}

func toolAvailable(dir, name string) bool {
	if strings.ContainsRune(name, os.PathSeparator) && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func inspectServer(ctx context.Context, w workspace, head string, s *serverConfig) (*serverReport, error) {
	report := &serverReport{Status: "stopped", ReadinessURL: s.ReadinessURL, Log: w.serverLog()}
	state, err := w.ownedServer()
	if err != nil {
		report.Status = "unknown"
		return report, fmt.Errorf("inspect server: %w", err)
	}
	address, _ := readinessAddress(s.ReadinessURL)
	report.Owned = state != nil
	report.PortConflict = (state == nil || state.Address != address) && portOccupied(ctx, address)
	switch {
	case report.PortConflict:
		report.Status = "port_conflict"
	case state == nil:
	case state.Identity != serverIdentity(head, s):
		report.Status = "stale"
	case !state.request(ctx, http.MethodGet, "/status"):
		report.Status = "unreachable"
	default:
		report.Status = "unready"
		if ready, _ := localRequest(ctx, http.MethodGet, s.ReadinessURL, ""); ready {
			report.Status = "ready"
		}
	}
	return report, nil
}
