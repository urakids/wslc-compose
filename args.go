package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	shortVolRe = regexp.MustCompile(`^(?:((?:[A-Za-z]:)?[^:]+):)?(/[^:]*)(?::([^:]+))?$`)
	volNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

func serviceEnv(p *Project, svc string) (*OMap, error) {
	c := p.Svc(svc)
	env := NewOMap()
	for _, ef := range asList(c.Get("env_file")) {
		path, required := scalarStr(ef), true
		if m, ok := ef.(*OMap); ok {
			path = m.Str("path")
			required = !m.Has("required") || truthy(m.Get("required"))
		}
		path = filepath.Join(p.Workdir, path)
		if _, err := os.Stat(path); err == nil {
			de, err := parseDotenv(path)
			if err != nil {
				return nil, err
			}
			for _, k := range de.Keys() {
				env.Set(k, de.Get(k))
			}
		} else if required {
			return nil, fmt.Errorf("env file %s not found", path)
		}
	}
	envs := toDict(c.Get("environment"))
	for _, k := range envs.Keys() {
		env.Set(k, envs.Get(k))
	}
	out := NewOMap()
	for _, k := range env.Keys() {
		if v, ok := env.Get(k).(string); ok {
			out.Set(k, v)
		} else if v, ok := os.LookupEnv(k); ok {
			out.Set(k, v)
		}
	}
	return out, nil
}

func portArgs(c *OMap) []string {
	var args []string
	for _, port := range asList(c.Get("ports")) {
		spec := scalarStr(port)
		if m, ok := port.(*OMap); ok {
			spec = m.Str("target")
			if m.Str("published") != "" {
				spec = m.Str("published") + ":" + spec
				if m.Str("host_ip") != "" {
					spec = m.Str("host_ip") + ":" + spec
				}
			}
			if m.Str("protocol") != "" {
				spec += "/" + m.Str("protocol")
			}
		}
		args = append(args, "-p", spec)
	}
	return args
}

// parseVolume は volumes の 1 要素を (type, source, target, mode) に分解する。
func parseVolume(svc string, v any) (vtype, src, dst, mode string, err error) {
	if m, ok := v.(*OMap); ok {
		vtype, src, dst = m.Str("type"), m.Str("source"), m.Str("target")
		if vtype == "" {
			vtype = "volume"
		}
		if truthy(m.Get("read_only")) {
			mode = "ro"
		}
		return
	}
	sm := shortVolRe.FindStringSubmatch(scalarStr(v))
	if sm == nil {
		return "", "", "", "", fmt.Errorf("service %q: invalid volume spec %q", svc, scalarStr(v))
	}
	src, dst, mode = sm[1], sm[2], sm[3]
	vtype = "bind"
	if src == "" || volNameRe.MatchString(src) {
		vtype = "volume"
	}
	return
}

// volumeMode は wslc が受け付ける ro / rw だけを残す。cached などの一貫性オプションは
// docker と同様に黙って無視し、それ以外 (z, nocopy など) は警告して無視する。
func volumeMode(svc, mode string) string {
	out := ""
	for _, m := range strings.Split(mode, ",") {
		switch m {
		case "", "cached", "delegated", "consistent":
		case "ro", "rw":
			out = m
		default:
			warn(fmt.Sprintf("service %q: volume option %q is not supported by wslc and is ignored", svc, m))
		}
	}
	return out
}

func volumeArgs(p *Project, svc string) ([]string, error) {
	var args []string
	for _, v := range asList(p.Svc(svc).Get("volumes")) {
		vtype, src, dst, mode, err := parseVolume(svc, v)
		if err != nil {
			return nil, err
		}
		switch vtype {
		case "tmpfs":
			args = append(args, "--tmpfs", dst)
			continue
		case "volume":
			if src != "" {
				if !p.Volumes.Has(src) {
					return nil, fmt.Errorf("service %q refers to undefined volume %q", svc, src)
				}
				src = p.VolumeName(src)
			}
		case "bind":
			if strings.HasPrefix(src, "~") {
				home, _ := os.UserHomeDir()
				src = home + src[1:]
			}
			if !filepath.IsAbs(src) && !strings.HasPrefix(src, "/") && !strings.HasPrefix(src, `\`) {
				src = filepath.Join(p.Workdir, src)
			}
			if src, err = filepath.Abs(src); err != nil {
				return nil, err
			}
		}
		spec := dst
		if src != "" {
			spec = src + ":" + dst
		}
		if mode = volumeMode(svc, mode); mode != "" {
			spec += ":" + mode
		}
		args = append(args, "-v", spec)
	}
	return args, nil
}

func healthcheckArgs(c *OMap) []string {
	hc := c.Map("healthcheck")
	if hc == nil {
		return nil
	}
	test := hc.Get("test")
	tl, isList := test.([]any)
	if truthy(hc.Get("disable")) || (isList && len(tl) > 0 && scalarStr(tl[0]) == "NONE") {
		return []string{"--no-healthcheck"}
	}
	var args []string
	if test != nil {
		var cmd string
		if !isList {
			cmd = scalarStr(test)
		} else if len(tl) > 0 {
			strs := make([]string, len(tl))
			for i, x := range tl {
				strs[i] = scalarStr(x)
			}
			switch strs[0] {
			case "CMD-SHELL":
				cmd = strings.Join(strs[1:], " ")
			case "CMD":
				cmd = shlexJoin(strs[1:])
			default:
				cmd = shlexJoin(strs)
			}
		}
		if cmd != "" {
			args = append(args, "--health-cmd", cmd)
		}
	}
	for _, kv := range [][2]string{{"interval", "--health-interval"}, {"timeout", "--health-timeout"},
		{"start_period", "--health-start-period"}, {"retries", "--health-retries"}} {
		if hc.Has(kv[0]) {
			args = append(args, kv[1], hc.Str(kv[0]))
		}
	}
	return args
}

func miscArgs(c *OMap) []string {
	var args []string
	for _, kv := range [][2]string{{"hostname", "-h"}, {"domainname", "--domainname"}, {"user", "-u"},
		{"working_dir", "-w"}, {"cpus", "--cpus"}, {"mem_limit", "-m"}, {"shm_size", "--shm-size"},
		{"stop_signal", "--stop-signal"}, {"gpus", "--gpus"}} {
		if c.Get(kv[0]) != nil {
			args = append(args, kv[1], c.Str(kv[0]))
		}
	}
	if truthy(c.Get("stdin_open")) {
		args = append(args, "-i")
	}
	if truthy(c.Get("tty")) {
		args = append(args, "-t")
	}
	if c.Get("stop_grace_period") != nil {
		args = append(args, "--stop-timeout", fmt.Sprint(durationSeconds(c.Get("stop_grace_period"))))
	}
	for _, t := range asList(c.Get("tmpfs")) {
		args = append(args, "--tmpfs", scalarStr(t))
	}
	ul := c.Map("ulimits")
	for _, name := range ul.Keys() {
		lim := ul.Str(name)
		if m := ul.Map(name); m != nil {
			lim = m.Str("soft") + ":" + m.Str("hard")
		}
		args = append(args, "--ulimit", name+"="+lim)
	}
	for _, kv := range [][2]string{{"dns", "--dns"}, {"dns_search", "--dns-search"}, {"dns_opt", "--dns-option"}} {
		for _, x := range asList(c.Get(kv[0])) {
			args = append(args, kv[1], scalarStr(x))
		}
	}
	res := c.Map("deploy").Map("resources")
	if lim := res.Map("limits"); lim != nil {
		if lim.Str("cpus") != "" && !c.Has("cpus") {
			args = append(args, "--cpus", lim.Str("cpus"))
		}
		if lim.Str("memory") != "" && !c.Has("mem_limit") {
			args = append(args, "-m", lim.Str("memory"))
		}
	}
	for _, dev := range asList(res.Map("reservations").Get("devices")) {
		dm, _ := dev.(*OMap)
		for _, capa := range asList(dm.Get("capabilities")) {
			if scalarStr(capa) == "gpu" && !c.Has("gpus") {
				args = append(args, "--gpus", "all")
			}
		}
	}
	return args
}

type createOpts struct {
	Oneoff     bool
	Entrypoint *string  // nil ならサービス定義を使う
	Command    []string // nil ならサービス定義を使う
	Extra      []string
	NoPorts    bool
}

// createArgs は wslc create の引数と、追加ネットワーク接続用の引数リストを返す。
func createArgs(p *Project, svc, cname string, o createOpts) ([]string, [][]string, error) {
	c := p.Svc(svc)
	args := []string{"create", "--name", cname}
	args = append(args, miscArgs(c)...)
	env, err := serviceEnv(p, svc)
	if err != nil {
		return nil, nil, err
	}
	for _, k := range env.Keys() {
		args = append(args, "-e", k+"="+env.Str(k))
	}
	if !o.NoPorts {
		args = append(args, portArgs(c)...)
	}
	vols, err := volumeArgs(p, svc)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, vols...)
	args = append(args, healthcheckArgs(c)...)

	var connects [][]string
	if mode := c.Str("network_mode"); mode != "" {
		if strings.HasPrefix(mode, "service:") || strings.HasPrefix(mode, "container:") {
			warn(fmt.Sprintf("service %q: network_mode %q is not supported, using default bridge", svc, mode))
		} else {
			args = append(args, "--network", mode)
		}
	}
	nets, err := p.ServiceNetworks(svc)
	if err != nil {
		return nil, nil, err
	}
	for i, n := range nets {
		var opt []string
		for _, a := range append([]any{svc}, asList(n.Opts.Get("aliases"))...) {
			opt = append(opt, "--network-alias", scalarStr(a))
		}
		if ip := n.Opts.Str("ipv4_address"); ip != "" {
			opt = append(opt, "--ip", ip)
		}
		if i == 0 {
			args = append(args, "--network", p.NetworkName(n.Key))
			args = append(args, opt...)
		} else {
			conn := append([]string{"network", "connect"}, opt...)
			connects = append(connects, append(conn, p.NetworkName(n.Key), cname))
		}
	}

	labels := toDict(c.Get("labels"))
	labels.Set(lProject, p.Name)
	labels.Set(lService, svc)
	labels.Set(lOneoff, map[bool]string{true: "True", false: "False"}[o.Oneoff])
	labels.Set(lWorkdir, p.Workdir)
	for _, k := range labels.Keys() {
		args = append(args, "-l", k+"="+labels.Str(k))
	}

	var ep []string
	if o.Entrypoint != nil {
		ep, _, err = splitCmd(*o.Entrypoint)
	} else {
		ep, _, err = splitCmd(c.Get("entrypoint"))
	}
	if err != nil {
		return nil, nil, err
	}
	cmd := o.Command
	if cmd == nil {
		if cmd, _, err = splitCmd(c.Get("command")); err != nil {
			return nil, nil, err
		}
	}
	args = append(args, o.Extra...)
	if len(ep) > 0 {
		args = append(args, "--entrypoint", ep[0])
		cmd = append(append([]string{}, ep[1:]...), cmd...)
	}
	args = append(args, p.Image(svc))
	args = append(args, cmd...)
	return args, connects, nil
}

func configHash(args []string, connects [][]string, imageID string) string {
	data, _ := json.Marshal([]any{args, connects, imageID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
