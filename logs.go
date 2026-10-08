package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

var colors = []string{"36", "33", "32", "35", "34", "96", "93", "92", "95", "94"}

type LogPrinter struct {
	width  int
	color  bool
	prefix bool
	colors map[string]string
	mu     sync.Mutex
}

func newLogPrinter(names []string, color, prefix bool) *LogPrinter {
	lp := &LogPrinter{color: color && isTerminal(os.Stdout), prefix: prefix, colors: map[string]string{}}
	for i, n := range names {
		if len(n) > lp.width {
			lp.width = len(n)
		}
		lp.colors[n] = colors[i%len(colors)]
	}
	return lp
}

func (lp *LogPrinter) line(name string, text []byte) {
	var buf bytes.Buffer
	if lp.prefix {
		pre := name + strings.Repeat(" ", lp.width-len(name)) + " | "
		if lp.color {
			pre = fmt.Sprintf("\x1b[%sm%s\x1b[0m", lp.colors[name], pre)
		}
		buf.WriteString(pre)
	}
	buf.Write(text)
	buf.WriteByte('\n')
	lp.mu.Lock()
	os.Stdout.Write(buf.Bytes())
	lp.mu.Unlock()
}

func streamLogs(w *Wslc, lp *LogPrinter, name string, opts []string) {
	c := w.cmd(append(append([]string{"logs"}, opts...), name))
	r, pw, err := os.Pipe()
	if err != nil {
		return
	}
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		pw.Close()
		r.Close()
		return
	}
	pw.Close()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lp.line(name, bytes.TrimRight(line, "\r\n"))
		}
		if err == io.EOF || err != nil {
			break
		}
	}
	r.Close()
	_ = c.Wait()
}

// followLogs は複数コンテナのログを並行して表示し、全ストリーム終了で close される channel を返す。
func followLogs(w *Wslc, names, opts []string, color, prefix bool) <-chan struct{} {
	lp := newLogPrinter(names, color, prefix)
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			streamLogs(w, lp, n, opts)
		}(n)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}
