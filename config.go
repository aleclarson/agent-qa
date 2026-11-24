package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const configName = ".agent-qa.json"
const localName = ".agent-qa.local.json"
const allTargets = "all"

type config struct {
	Version  int               `json:"version"`
	Settings map[string]string `json:"settings,omitempty"`
	Targets  map[string]target `json:"targets"`
}

type target struct {
	Settings     map[string]string `json:"settings,omitempty"`
	Dependencies *step             `json:"dependencies,omitempty"`
	Build        *step             `json:"build,omitempty"`
	Server       *serverConfig     `json:"server,omitempty"`
}

type step struct {
	Command []string `json:"command"`
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
}

type serverConfig struct {
	Command      []string `json:"command"`
	ReadinessURL string   `json:"readiness_url"`
	Timeout      string   `json:"timeout,omitempty"`
	AppURL       string   `json:"app_url,omitempty"`
}

func (s serverConfig) appURL() string {
	if s.AppURL != "" {
		return s.AppURL
	}
	// A readiness route is often a health check. Hand off its origin.
	u, _ := url.Parse(s.ReadinessURL)
	return u.Scheme + "://" + u.Host
}

type localConfig struct {
	Settings map[string]string      `json:"settings,omitempty"`
	Targets  map[string]localTarget `json:"targets,omitempty"`
}

type localTarget struct {
	Settings map[string]string `json:"settings"`
}

var targetName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var settingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var placeholder = regexp.MustCompile(`\$\{([^{}]*)\}`)

func decodeJSON(r io.Reader, dst any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("expected one JSON value")
		}
		return err
	}
	return nil
}

func readJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return decodeJSON(f, dst)
}

func loadConfig(root string) (map[string]target, error) {
	var cfg config
	if err := readJSON(filepath.Join(root, configName), &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", configName, err)
	}
	local, err := loadLocal(root)
	if err != nil {
		return nil, err
	}
	return cfg.resolve(local)
}

func loadLocal(root string) (localConfig, error) {
	var local localConfig
	if err := readJSON(filepath.Join(root, localName), &local); err != nil && !errors.Is(err, os.ErrNotExist) {
		return local, fmt.Errorf("%s: %w", localName, err)
	}
	return local, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c config) resolve(local localConfig) (map[string]target, error) {
	if c.Version != 1 {
		return nil, fmt.Errorf("unsupported config version %d (expected 1)", c.Version)
	}
	if len(c.Targets) == 0 {
		return nil, errors.New("declare at least one target")
	}
	for _, name := range sortedKeys(local.Targets) {
		if _, ok := c.Targets[name]; !ok {
			return nil, fmt.Errorf("%s refers to unknown target %q", localName, name)
		}
	}
	resolved := make(map[string]target, len(c.Targets))
	for _, name := range sortedKeys(c.Targets) {
		t := c.Targets[name]
		if name == allTargets {
			return nil, fmt.Errorf("target name %q is reserved", name)
		}
		if !targetName.MatchString(name) {
			return nil, fmt.Errorf("invalid target name %q (use lowercase letters, digits, underscores or hyphens)", name)
		}
		settings := make(map[string]string)
		for _, source := range []map[string]string{c.Settings, t.Settings, local.Settings, local.Targets[name].Settings} {
			for _, key := range sortedKeys(source) {
				if !settingName.MatchString(key) {
					return nil, fmt.Errorf("invalid setting name %q", key)
				}
				settings[key] = source[key]
			}
		}
		expanded, err := resolveTarget(t, settings)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", name, err)
		}
		resolved[name] = expanded
	}
	return resolved, nil
}

func resolveTarget(t target, settings map[string]string) (target, error) {
	expand := func(value string) (string, error) {
		var err error
		value = placeholder.ReplaceAllStringFunc(value, func(token string) string {
			key := token[2 : len(token)-1]
			v, ok := settings[key]
			if !ok && err == nil {
				err = fmt.Errorf("unknown setting %q", key)
			}
			return v
		})
		if strings.ContainsRune(value, 0) {
			return "", errors.New("values cannot contain NUL")
		}
		return value, err
	}
	command := func(argv []string) ([]string, error) {
		if len(argv) == 0 {
			return nil, errors.New("command must be a nonempty argv array")
		}
		result := slices.Clone(argv)
		for i, arg := range argv {
			value, err := expand(arg)
			if err != nil {
				return nil, err
			}
			result[i] = value
		}
		if strings.TrimSpace(result[0]) == "" {
			return nil, errors.New("command executable is empty")
		}
		return result, nil
	}
	for _, field := range []struct {
		name string
		step **step
	}{{"dependencies", &t.Dependencies}, {"build", &t.Build}} {
		if *field.step == nil {
			continue
		}
		s := **field.step
		argv, err := command(s.Command)
		if err != nil {
			return t, fmt.Errorf("%s: %w", field.name, err)
		}
		s.Command = argv
		for _, path := range append(slices.Clone(s.Inputs), s.Outputs...) {
			if !filepath.IsLocal(path) || filepath.Clean(path) != path || path == "." || strings.ContainsRune(path, 0) {
				return t, fmt.Errorf("%s: expected a relative path or glob, got %q", field.name, path)
			}
		}
		for _, glob := range s.Inputs {
			if _, err := filepath.Match(glob, ""); err != nil {
				return t, fmt.Errorf("%s: invalid input glob %q: %w", field.name, glob, err)
			}
		}
		*field.step = &s
	}
	if t.Server != nil {
		s := *t.Server
		argv, err := command(s.Command)
		if err != nil {
			return t, fmt.Errorf("server: %w", err)
		}
		s.Command = argv
		s.ReadinessURL, err = expand(s.ReadinessURL)
		if err != nil {
			return t, fmt.Errorf("server readiness URL: %w", err)
		}
		if _, err := readinessAddress(s.ReadinessURL); err != nil {
			return t, err
		}
		if s.AppURL != "" {
			s.AppURL, err = expand(s.AppURL)
			if err != nil {
				return t, fmt.Errorf("server app URL: %w", err)
			}
			u, err := url.Parse(s.AppURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
				return t, errors.New("server app_url must be an absolute HTTP(S) URL without credentials")
			}
		}
		if s.Timeout == "" {
			s.Timeout = "30s"
		}
		if d, err := time.ParseDuration(s.Timeout); err != nil || d <= 0 {
			return t, fmt.Errorf("server timeout %q must be a positive Go duration", s.Timeout)
		}
		t.Server = &s
	}
	return t, nil
}

func readinessAddress(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return "", errors.New("server readiness_url must be a local HTTP(S) URL with an explicit port")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(u.Port())
	if (host != "localhost" && (ip == nil || !ip.IsLoopback())) || err != nil || port < 1 || port > 65535 {
		return "", errors.New("server readiness_url must use localhost or a loopback IP and an explicit port")
	}
	return net.JoinHostPort(host, u.Port()), nil
}

func encodeJSON(value any) ([]byte, error) {
	b, err := json.MarshalIndent(value, "", "  ")
	return append(b, '\n'), err
}

// writeJSON publishes a complete file in one operation. Linking protects an
// existing path even when two initializations race.
func writeJSON(path string, value any, replace bool, mode os.FileMode) error {
	data, err := encodeJSON(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-qa-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if replace {
		return os.Rename(f.Name(), path)
	}
	return os.Link(f.Name(), path)
}

func interactiveConfig(in io.Reader, out io.Writer) (config, error) {
	cfg := config{Version: 1, Targets: make(map[string]target)}
	reader := bufio.NewReader(in)
	prompt := func(label string) (string, error) {
		fmt.Fprint(out, label)
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	array := func(label string) ([]string, error) {
		line, err := prompt(label)
		if err != nil || line == "" {
			return nil, err
		}
		var items []string
		err = decodeJSON(bytes.NewBufferString(line), &items)
		return items, err
	}
	readStep := func(name string) (*step, error) {
		argv, err := array(name + " command as JSON argv (Enter to skip): ")
		if err != nil || len(argv) == 0 {
			return nil, err
		}
		inputs, err := array(name + " tracked input globs as a JSON array (Enter for all commits): ")
		if err != nil {
			return nil, err
		}
		outputs, err := array(name + " expected output paths as a JSON array (Enter to skip): ")
		return &step{Command: argv, Inputs: inputs, Outputs: outputs}, err
	}
	for {
		name, err := prompt("Target name (Enter to finish): ")
		if err != nil {
			return cfg, err
		}
		if name == "" {
			return cfg, nil
		}
		if _, exists := cfg.Targets[name]; exists {
			return cfg, fmt.Errorf("duplicate target %q", name)
		}
		var t target
		t.Dependencies, err = readStep("Dependency")
		if err != nil {
			return cfg, err
		}
		t.Build, err = readStep("Build")
		if err != nil {
			return cfg, err
		}
		argv, err := array("Server command as JSON argv (Enter to skip): ")
		if err != nil {
			return cfg, err
		}
		if len(argv) > 0 {
			value, err := prompt("Server readiness URL: ")
			if err != nil {
				return cfg, err
			}
			t.Server = &serverConfig{Command: argv, ReadinessURL: value}
		}
		settings, err := prompt("Target settings as a JSON object (Enter to skip): ")
		if err != nil {
			return cfg, err
		}
		if settings != "" {
			if err := decodeJSON(strings.NewReader(settings), &t.Settings); err != nil {
				return cfg, err
			}
		}
		cfg.Targets[name] = t
	}
}
