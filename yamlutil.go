package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// OMap はキー順序を保持するマップ。YAML のマッピングを表す。
type OMap struct {
	keys []string
	m    map[string]any
}

func NewOMap() *OMap { return &OMap{m: map[string]any{}} }

func (o *OMap) Get(k string) any {
	if o == nil {
		return nil
	}
	return o.m[k]
}

func (o *OMap) Has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.m[k]
	return ok
}

func (o *OMap) Set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *OMap) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

func (o *OMap) Len() int { return len(o.Keys()) }

// Map は子マッピングを返す。存在しない・null の場合は nil (nil の *OMap も安全に扱える)。
func (o *OMap) Map(k string) *OMap {
	m, _ := o.Get(k).(*OMap)
	return m
}

func (o *OMap) Str(k string) string { return scalarStr(o.Get(k)) }

func fromNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return fromNode(n.Content[0])
	case yaml.AliasNode:
		return fromNode(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := fromNode(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := NewOMap()
		var merges []*OMap
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			v, err := fromNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			if k.Tag == "!!merge" {
				switch mv := v.(type) {
				case *OMap:
					merges = append(merges, mv)
				case []any:
					for _, x := range mv {
						if m, ok := x.(*OMap); ok {
							merges = append(merges, m)
						}
					}
				}
				continue
			}
			out.Set(k.Value, v)
		}
		for _, m := range merges {
			for _, k := range m.keys {
				if !out.Has(k) {
					out.Set(k, m.m[k])
				}
			}
		}
		return out, nil
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
}

func toNode(v any) *yaml.Node {
	switch x := v.(type) {
	case *OMap:
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range x.keys {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, toNode(x.m[k]))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range x {
			n.Content = append(n.Content, toNode(e))
		}
		return n
	default:
		n := &yaml.Node{}
		_ = n.Encode(x)
		return n
	}
}

func scalarStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func asList(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	default:
		return []any{x}
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "True" || x == "yes"
	}
	return false
}

// toDict は environment / labels の list 形式を OMap (値は string か nil) に正規化する。
func toDict(v any) *OMap {
	out := NewOMap()
	switch x := v.(type) {
	case *OMap:
		for _, k := range x.keys {
			if x.m[k] == nil {
				out.Set(k, nil)
			} else {
				out.Set(k, scalarStr(x.m[k]))
			}
		}
	case []any:
		for _, item := range x {
			k, val, ok := strings.Cut(scalarStr(item), "=")
			if ok {
				out.Set(k, val)
			} else {
				out.Set(k, nil)
			}
		}
	}
	return out
}

func merge(base, over any) any {
	b, ok1 := base.(*OMap)
	o, ok2 := over.(*OMap)
	if !ok1 || !ok2 {
		return over
	}
	out := NewOMap()
	for _, k := range b.keys {
		out.Set(k, b.m[k])
	}
	for _, k := range o.keys {
		if b.Has(k) {
			out.Set(k, merge(b.m[k], o.m[k]))
		} else {
			out.Set(k, o.m[k])
		}
	}
	return out
}

// ---------------------------------------------------------------- 変数展開

type Env map[string]string

var (
	varRe  = regexp.MustCompile(`\$(?:(\$)|\{([^}]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)
	exprRe = regexp.MustCompile(`(?s)^([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])(.*))?$`)
)

func interpolate(v any, env Env) (any, error) {
	switch x := v.(type) {
	case *OMap:
		out := NewOMap()
		for _, k := range x.keys {
			nv, err := interpolate(x.m[k], env)
			if err != nil {
				return nil, err
			}
			out.Set(k, nv)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			nv, err := interpolate(e, env)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	case string:
		return interpolateStr(x, env)
	}
	return v, nil
}

func interpolateStr(s string, env Env) (string, error) {
	var firstErr error
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := varRe.FindStringSubmatch(m)
		if sm[1] != "" {
			return "$"
		}
		name, op, arg := sm[3], "", ""
		if name == "" {
			em := exprRe.FindStringSubmatch(sm[2])
			if em == nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("invalid interpolation format: ${%s}", sm[2])
				}
				return ""
			}
			name, op, arg = em[1], em[2], em[3]
		}
		val, set := env[name]
		sub := func(a string) string {
			r, err := interpolateStr(a, env)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			return r
		}
		switch op {
		case "":
			if !set {
				warn(fmt.Sprintf("The %q variable is not set. Defaulting to a blank string.", name))
			}
			return val
		case ":-":
			if val != "" {
				return val
			}
			return sub(arg)
		case "-":
			if set {
				return val
			}
			return sub(arg)
		case ":+":
			if val != "" {
				return sub(arg)
			}
			return ""
		case "+":
			if set {
				return sub(arg)
			}
			return ""
		}
		if (op == ":?" && val == "") || (op == "?" && !set) {
			if firstErr == nil {
				firstErr = fmt.Errorf("required variable %s is missing a value: %s", name, arg)
			}
		}
		return val
	})
	return out, firstErr
}
