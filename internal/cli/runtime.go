package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type problem struct {
	message, hint string
	code          int
}

func (e *problem) Error() string       { return e.message }
func fail(message, hint string) error  { return &problem{message, hint, 1} }
func usage(message, hint string) error { return &problem{message, hint, 2} }

type runner func(cwd string, args ...string) (string, error)
type app struct {
	out, err io.Writer
	run      runner
	lookup   func(string) (string, error)
	cwd      string
}

func execute(cwd string, args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "WORKMUX_BACKEND=tmux")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	}
	var out, diagnostic bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return out.String(), &processError{err, diagnostic.String()}
	}
	return out.String(), nil
}

type processError struct {
	err        error
	diagnostic string
}

func (e *processError) Error() string { return e.err.Error() }
func (a *app) command(cwd string, args ...string) (string, error) {
	out, err := a.run(cwd, args...)
	if err != nil {
		var process *processError
		if errors.As(err, &process) && process.diagnostic != "" {
			fmt.Fprintln(a.err, strings.TrimSpace(process.diagnostic))
		}
		return "", fail("Workspace operation failed in "+cwd, "Inspect stderr, fix the failure, and retry the same wmm command; existing work is preserved.")
	}
	return strings.TrimSpace(out), nil
}
func (a *app) git(repo string, args ...string) (string, error) {
	return a.command(repo, append([]string{"git"}, args...)...)
}
func (a *app) tryGit(repo string, args ...string) (string, error) {
	s, e := a.run(repo, append([]string{"git"}, args...)...)
	return strings.TrimSpace(s), e
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func keys[V any](m map[string]V) []string {
	v := make([]string, 0, len(m))
	for k := range m {
		v = append(v, k)
	}
	sort.Strings(v)
	return v
}
func quote(value any) string                { b, _ := json.Marshal(value); return string(b) }
func (a *app) scalar(key string, value any) { fmt.Fprintf(a.out, "%s: %s\n", key, quote(value)) }
func (a *app) table(key string, fields []string, rows [][]any) {
	if len(rows) == 0 {
		fmt.Fprintf(a.out, "%s[0]:\n", key)
		return
	}
	fmt.Fprintf(a.out, "%s[%d]{%s}:\n", key, len(rows), strings.Join(fields, ","))
	for _, row := range rows {
		values := make([]string, len(row))
		for i, v := range row {
			values[i] = quote(v)
		}
		fmt.Fprintln(a.out, "  "+strings.Join(values, ","))
	}
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = shellQuote(s)
	}
	return strings.Join(q, " ")
}

func (a *app) findCommand(name string) (string, error) {
	if a.lookup != nil {
		return a.lookup(name)
	}
	return exec.LookPath(name)
}
