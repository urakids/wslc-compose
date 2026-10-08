package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	labelPrefix = "com.wslc.compose."
	lProject    = labelPrefix + "project"
	lService    = labelPrefix + "service"
	lHash       = labelPrefix + "config-hash"
	lOneoff     = labelPrefix + "oneoff"
	lWorkdir    = labelPrefix + "project.working_dir"
	lNetwork    = labelPrefix + "network"
	lVolume     = labelPrefix + "volume"
)

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// wslc にマッピングしているサービスキー。これ以外は警告して無視する。
var supportedKeys = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`image build container_name hostname domainname command entrypoint
		environment env_file ports expose volumes networks network_mode depends_on healthcheck labels
		user working_dir stdin_open tty cpus mem_limit shm_size stop_signal stop_grace_period tmpfs
		ulimits dns dns_search dns_opt gpus deploy pull_policy profiles`) {
		supportedKeys[k] = true
	}
}

type GlobalOpts struct {
	Files       []string
	ProjectName string
	ProjectDir  string
	EnvFile     string
	Profiles    []string
	Session     string
	DryRun      bool
}

type Project struct {
	Name     string
	Workdir  string
	Env      Env
	Config   *OMap
	Services *OMap
	Networks *OMap
	Volumes  *OMap
	Active   []string
}

type Dep struct{ Name, Cond string }

type NetRef struct {
	Key  string
	Opts *OMap
}

func parseDotenv(path string) (*OMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := NewOMap()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[7:])
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok {
			env.Set(key, nil)
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == val[len(val)-1] && (val[0] == '"' || val[0] == '\'') {
			q := val[0]
			val = val[1 : len(val)-1]
			if q == '"' {
				val = strings.NewReplacer(`\n`, "\n", `\"`, `"`).Replace(val)
			}
		} else if loc := regexp.MustCompile(`\s+#`).FindStringIndex(val); loc != nil {
			val = val[:loc[0]]
		}
		env.Set(key, val)
	}
	return env, sc.Err()
}

func findComposeFile() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		for _, name := range composeFiles {
			p := filepath.Join(d, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, nil
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no configuration file provided: not found")
		}
		d = parent
	}
}

func loadProject(g *GlobalOpts) (*Project, error) {
	files := g.Files
	if len(files) == 0 {
		for _, f := range filepath.SplitList(os.Getenv("COMPOSE_FILE")) {
			if f != "" {
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		f, err := findComposeFile()
		if err != nil {
			return nil, err
		}
		files = []string{f}
	}
	for i, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			return nil, err
		}
		files[i] = abs
	}
	p := &Project{Env: Env{}}
	wd := g.ProjectDir
	if wd == "" {
		wd = filepath.Dir(files[0])
	}
	var err error
	if p.Workdir, err = filepath.Abs(wd); err != nil {
		return nil, err
	}

	envFile := g.EnvFile
	if envFile == "" {
		envFile = filepath.Join(p.Workdir, ".env")
	}
	if _, err := os.Stat(envFile); err == nil {
		de, err := parseDotenv(envFile)
		if err != nil {
			return nil, err
		}
		for _, k := range de.Keys() {
			if v, ok := de.Get(k).(string); ok {
				p.Env[k] = v
			}
		}
	} else if g.EnvFile != "" {
		return nil, fmt.Errorf("env file %s not found", envFile)
	}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			p.Env[k] = v
		}
	}

	var cfg any = NewOMap()
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var node yaml.Node
		if err := yaml.Unmarshal(raw, &node); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		data, err := fromNode(&node)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if data == nil {
			continue
		}
		if data, err = interpolate(data, p.Env); err != nil {
			return nil, err
		}
		dm, ok := data.(*OMap)
		if !ok {
			return nil, fmt.Errorf("%s: top-level object must be a mapping", f)
		}
		for _, s := range dm.Map("services").Keys() {
			svc := dm.Map("services").Map(s)
			for _, key := range []string{"environment", "labels"} {
				if svc.Has(key) {
					svc.Set(key, toDict(svc.Get(key)))
				}
			}
		}
		cfg = merge(cfg, dm)
	}
	p.Config = cfg.(*OMap)

	name := g.ProjectName
	for _, cand := range []string{p.Env["COMPOSE_PROJECT_NAME"], p.Config.Str("name"), filepath.Base(p.Workdir)} {
		if name == "" {
			name = cand
		}
	}
	p.Name = strings.TrimLeft(regexp.MustCompile(`[^a-z0-9_-]`).ReplaceAllString(strings.ToLower(name), ""), "_-")
	if p.Name == "" {
		return nil, fmt.Errorf("invalid project name %q", name)
	}
	if p.Config.Has("name") {
		p.Config.Set("name", p.Name)
	} else {
		c := NewOMap()
		c.Set("name", p.Name)
		for _, k := range p.Config.Keys() {
			c.Set(k, p.Config.Get(k))
		}
		p.Config = c
	}

	norm := func(key string) *OMap {
		out := NewOMap()
		for _, k := range p.Config.Map(key).Keys() {
			m := p.Config.Map(key).Map(k)
			if m == nil {
				m = NewOMap()
			}
			out.Set(k, m)
		}
		return out
	}
	p.Services, p.Networks, p.Volumes = norm("services"), norm("networks"), norm("volumes")
	if p.Services.Len() == 0 {
		return nil, fmt.Errorf("no service defined")
	}

	profiles := map[string]bool{}
	for _, pr := range g.Profiles {
		profiles[pr] = true
	}
	for _, pr := range strings.Split(p.Env["COMPOSE_PROFILES"], ",") {
		if pr != "" {
			profiles[pr] = true
		}
	}
	for _, s := range p.Services.Keys() {
		svc := p.Svc(s)
		sp := asList(svc.Get("profiles"))
		active := len(sp) == 0
		for _, x := range sp {
			active = active || profiles[scalarStr(x)]
		}
		if active {
			p.Active = append(p.Active, s)
		}
		if !svc.Has("image") && !svc.Has("build") {
			return nil, fmt.Errorf("service %q has neither an image nor a build context specified", s)
		}
		for _, key := range svc.Keys() {
			if !supportedKeys[key] && !strings.HasPrefix(key, "x-") {
				warn(fmt.Sprintf("service %q: %q is not supported by wslc-compose and is ignored", s, key))
			}
		}
	}
	return p, nil
}

func (p *Project) Svc(name string) *OMap { return p.Services.Map(name) }

func (p *Project) Image(svc string) string {
	if img := p.Svc(svc).Str("image"); img != "" {
		return img
	}
	return p.Name + "-" + svc
}

func (p *Project) ContainerName(svc string) string {
	if n := p.Svc(svc).Str("container_name"); n != "" {
		return n
	}
	return p.Name + "-" + svc + "-1"
}

func externalName(defs *OMap, key, project string) string {
	d := defs.Map(key)
	if n := d.Str("name"); n != "" {
		return n
	}
	if truthy(d.Get("external")) {
		return key
	}
	return project + "_" + key
}

func (p *Project) NetworkName(key string) string { return externalName(p.Networks, key, p.Name) }
func (p *Project) VolumeName(key string) string  { return externalName(p.Volumes, key, p.Name) }

func (p *Project) DependsOn(svc string) ([]Dep, error) {
	var out []Dep
	add := func(name, cond string) error {
		if !p.Services.Has(name) {
			return fmt.Errorf("service %q depends on undefined service %q", svc, name)
		}
		if cond == "" {
			cond = "service_started"
		}
		out = append(out, Dep{name, cond})
		return nil
	}
	switch d := p.Svc(svc).Get("depends_on").(type) {
	case []any:
		for _, x := range d {
			if err := add(scalarStr(x), ""); err != nil {
				return nil, err
			}
		}
	case *OMap:
		for _, k := range d.Keys() {
			if err := add(k, d.Map(k).Str("condition")); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// ServiceNetworks はサービスが接続するネットワークを返す。network_mode 使用時は空。
func (p *Project) ServiceNetworks(svc string) ([]NetRef, error) {
	c := p.Svc(svc)
	if c.Str("network_mode") != "" {
		return nil, nil
	}
	var out []NetRef
	switch n := c.Get("networks").(type) {
	case nil:
		out = []NetRef{{"default", nil}}
	case []any:
		for _, x := range n {
			out = append(out, NetRef{scalarStr(x), nil})
		}
	case *OMap:
		for _, k := range n.Keys() {
			out = append(out, NetRef{k, n.Map(k)})
		}
	}
	for _, r := range out {
		if r.Key != "default" && !p.Networks.Has(r.Key) {
			return nil, fmt.Errorf("service %q refers to undefined network %q", svc, r.Key)
		}
	}
	return out, nil
}

// Resolve は指定サービス(空なら有効な全サービス)を依存関係順に並べて返す。
func (p *Project) Resolve(names []string, withDeps bool) ([]string, error) {
	for _, n := range names {
		if !p.Services.Has(n) {
			return nil, fmt.Errorf("no such service: %s", n)
		}
	}
	targets := names
	if len(targets) == 0 {
		targets = p.Active
	}
	var order []string
	seen := map[string]int{}
	var visit func(s string) error
	visit = func(s string) error {
		switch seen[s] {
		case 1:
			return fmt.Errorf("dependency cycle detected at service %q", s)
		case 2:
			return nil
		}
		seen[s] = 1
		if withDeps {
			deps, err := p.DependsOn(s)
			if err != nil {
				return err
			}
			for _, d := range deps {
				if err := visit(d.Name); err != nil {
					return err
				}
			}
		}
		seen[s] = 2
		order = append(order, s)
		return nil
	}
	for _, t := range targets {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return order, nil
}

var durRe = regexp.MustCompile(`([\d.]+)(ms|us|h|m|s)`)

// durationSeconds は "1m30s" / "500ms" / 10 などを秒(切り上げ)に変換する。
func durationSeconds(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	}
	units := map[string]float64{"h": 3600, "m": 60, "s": 1, "ms": 0.001, "us": 1e-6}
	total := 0.0
	for _, m := range durRe.FindAllStringSubmatch(scalarStr(v), -1) {
		n, _ := strconv.ParseFloat(m[1], 64)
		total += n * units[m[2]]
	}
	return int(math.Ceil(total))
}
