package main

import (
	"fmt"
	"regexp"
	"strings"
)

// shlexSplit は POSIX シェル風に文字列を分割する (Python の shlex.split 相当)。
func shlexSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("no closing quotation: %s", s)
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			inWord = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`\n", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, fmt.Errorf("no closing quotation: %s", s)
			}
		case c == '\\':
			inWord = true
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

var safeShellRe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if safeShellRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func shlexJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// splitCmd は command / entrypoint を引数列にする。未指定なら nil, false。
func splitCmd(v any) ([]string, bool, error) {
	switch x := v.(type) {
	case nil:
		return nil, false, nil
	case string:
		r, err := shlexSplit(x)
		return r, true, err
	case []any:
		r := make([]string, len(x))
		for i, e := range x {
			r[i] = scalarStr(e)
		}
		return r, true, nil
	}
	return []string{scalarStr(v)}, true, nil
}
