package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

type serverList struct {
	PrimaryCheckout string         `json:"primary_checkout"`
	GitCommonDir    string         `json:"git_common_dir"`
	ConfigError     string         `json:"config_error,omitempty"`
	Servers         []listedServer `json:"servers"`
}

type listedServer struct {
	Target     string `json:"target"`
	Configured bool   `json:"configured"`
	serverReport
	Error string `json:"error,omitempty"`
}

func (a app) servers(ctx context.Context, cwd string, jsonOutput bool) error {
	repo, err := findRepository(ctx, cwd)
	if err != nil {
		return err
	}
	primary, err := repo.primaryCheckout(ctx)
	if err != nil {
		return err
	}
	stored, err := repo.storedTargets()
	if err != nil {
		return err
	}
	targets, configErr := loadConfig(repo.root)
	list := serverList{PrimaryCheckout: primary, GitCommonDir: repo.common, Servers: []listedServer{}}
	if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
		list.ConfigError = configErr.Error()
	}
	configured := make(map[string]*serverConfig)
	for _, name := range stored {
		configured[name] = nil
	}
	for name, t := range targets {
		if t.Server != nil {
			configured[name] = t.Server
		}
	}
	for _, name := range sortedKeys(configured) {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if row := inspectListedServer(ctx, repo.target(name), name, configured[name]); row != nil {
			list.Servers = append(list.Servers, *row)
		}
	}
	if jsonOutput {
		data, err := encodeJSON(list)
		if err != nil {
			return err
		}
		_, err = a.out.Write(data)
		return err
	}
	out := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(out, "Primary checkout: %s\n", list.PrimaryCheckout)
	if list.ConfigError != "" {
		fmt.Fprintf(out, "Configuration: %s\n", list.ConfigError)
	}
	if len(list.Servers) == 0 {
		fmt.Fprintln(out, "No managed servers.")
	} else {
		fmt.Fprintln(out, "TARGET\tSTATUS\tADDRESS\tCONFIGURED\tLOG")
		for _, row := range list.Servers {
			fmt.Fprintf(out, "%s\t%s\t%s\t%t\t%s\n", row.Target, row.Status, row.Address, row.Configured, row.Log)
			if row.Error != "" {
				fmt.Fprintf(out, "%s: %s\n", row.Target, row.Error)
			}
		}
	}
	return out.Flush()
}

func (r repository) primaryCheckout(ctx context.Context) (string, error) {
	output, err := git(ctx, r.root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(output, "\n")
	primary, ok := strings.CutPrefix(line, "worktree ")
	if !ok || primary == "" {
		return "", errors.New("primary checkout missing from git worktree list")
	}
	if strings.HasPrefix(primary, `"`) {
		return strconv.Unquote(primary)
	}
	return primary, nil
}

func inspectListedServer(ctx context.Context, w workspace, name string, s *serverConfig) *listedServer {
	row := &listedServer{Target: name, Configured: s != nil}
	if s != nil {
		var head string
		_, headErr := os.Stat(w.tree)
		if headErr == nil {
			head, headErr = resolveCommit(ctx, w.tree, "HEAD")
		} else if errors.Is(headErr, os.ErrNotExist) {
			headErr = nil
		}
		report, err := inspectServer(ctx, w, head, s)
		row.serverReport = *report
		if err := errors.Join(headErr, err); err != nil {
			row.Error = err.Error()
		}
		return row
	}
	state, err := w.ownedServer()
	if err != nil {
		row.Status, row.Log, row.Error = "unknown", w.serverLog(), err.Error()
		return row
	}
	if state == nil {
		return nil
	}
	row.Owned, row.Address, row.Log = true, state.Address, w.serverLog()
	row.Status = "unreachable"
	if state.request(ctx, http.MethodGet, "/status") {
		row.Status = "running"
	}
	return row
}
