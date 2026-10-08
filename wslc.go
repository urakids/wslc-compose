package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Wslc struct {
	base   []string
	dryRun bool
}

func NewWslc(session string, dryRun bool) *Wslc {
	bin := os.Getenv("WSLC_BIN")
	if bin == "" {
		bin = "wslc"
	}
	base := []string{bin}
	if session != "" {
		base = append(base, "--session", session)
	}
	return &Wslc{base: base, dryRun: dryRun}
}

func (w *Wslc) cmd(args []string) *exec.Cmd {
	return exec.Command(w.base[0], append(append([]string{}, w.base[1:]...), args...)...)
}

func quoteArg(a string) string {
	if a == "" || strings.ContainsAny(a, " \t\"") {
		return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return a
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// exec は状態を変更するコマンドを実行する。dry-run 時は表示のみ。
func (w *Wslc) exec(args []string, quiet bool, stdin io.Reader) (int, error) {
	if w.dryRun {
		q := make([]string, 0, len(w.base)+len(args))
		for _, a := range append(append([]string{}, w.base...), args...) {
			q = append(q, quoteArg(a))
		}
		fmt.Fprintln(os.Stderr, "DRY-RUN:", strings.Join(q, " "))
		return 0, nil
	}
	c := w.cmd(args)
	c.Stderr = os.Stderr
	if !quiet {
		c.Stdout = os.Stdout
	}
	if stdin != nil {
		c.Stdin = stdin
	}
	err := c.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return 1, err
	}
	return exitCode(err), nil
}

func (w *Wslc) check(args []string, quiet bool, stdin io.Reader) error {
	rc, err := w.exec(args, quiet, stdin)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("wslc %s failed (exit %d)", args[0], rc)
	}
	return nil
}

// Run は出力を端末に流してコマンドを実行する。
func (w *Wslc) Run(args ...string) error { return w.check(args, false, os.Stdin) }

// RunQ は標準出力を捨ててコマンドを実行する。
func (w *Wslc) RunQ(args ...string) error { return w.check(args, true, nil) }

// Try は失敗を無視して (標準出力を捨てて) 実行する。
func (w *Wslc) Try(args ...string) { _, _ = w.exec(args, true, nil) }

// RunRC は対話的に実行し終了コードを返す。
func (w *Wslc) RunRC(args ...string) (int, error) { return w.exec(args, false, os.Stdin) }

// Out は問い合わせ用コマンド。dry-run でも実行する。
func (w *Wslc) Out(args ...string) (string, error) {
	c := w.cmd(args)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("wslc %s failed: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (w *Wslc) JSONLines(args ...string) ([]map[string]any, error) {
	text, err := w.Out(args...)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (w *Wslc) Exists(kind, name string) bool {
	return w.cmd([]string{"inspect", "-t", kind, name}).Run() == nil
}

func (w *Wslc) Inspect(kind string, names ...string) ([]map[string]any, error) {
	text, err := w.Out(append([]string{"inspect", "-t", kind}, names...)...)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	return out, json.Unmarshal([]byte(text), &out)
}
