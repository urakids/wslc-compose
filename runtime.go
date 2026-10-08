package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Ctr struct {
	ID, Name, Service, Hash, State string
	Oneoff                         bool
}

func warn(msg string) { fmt.Fprintf(os.Stderr, "WARN[0000] %s\n", msg) }

func status(kind, name, state string) { fmt.Fprintf(os.Stderr, " %s %s  %s\n", kind, name, state) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func listContainers(w *Wslc, p *Project, services []string, includeOneoff bool) ([]Ctr, error) {
	text, err := w.Out("ps", "-a", "-q", "--no-trunc", "-f", "label="+lProject+"="+p.Name)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(text)
	if len(ids) == 0 {
		return nil, nil
	}
	data, err := w.Inspect("container", ids...)
	if err != nil {
		return nil, err
	}
	var out []Ctr
	for _, d := range data {
		labels, _ := d["Labels"].(map[string]any)
		if len(labels) == 0 {
			labels, _ = dig(d, "Config", "Labels").(map[string]any)
		}
		str := func(v any) string { s, _ := v.(string); return s }
		if str(labels[lProject]) != p.Name {
			continue
		}
		c := Ctr{
			ID: str(d["Id"]), Name: strings.TrimPrefix(str(d["Name"]), "/"),
			Service: str(labels[lService]), Hash: str(labels[lHash]),
			State: str(dig(d, "State", "Status")), Oneoff: str(labels[lOneoff]) == "True",
		}
		if c.Oneoff && !includeOneoff {
			continue
		}
		if len(services) > 0 && !contains(services, c.Service) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func containerState(w *Wslc, name string) (map[string]any, error) {
	d, err := w.Inspect("container", name)
	if err != nil || len(d) == 0 {
		return nil, fmt.Errorf("no such container: %s", name)
	}
	st, _ := d[0]["State"].(map[string]any)
	return st, nil
}

func ensureNetworks(w *Wslc, p *Project, services []string) error {
	var keys []string
	for _, s := range services {
		nets, err := p.ServiceNetworks(s)
		if err != nil {
			return err
		}
		for _, n := range nets {
			if !contains(keys, n.Key) {
				keys = append(keys, n.Key)
			}
		}
	}
	for _, key := range keys {
		n := p.Networks.Map(key)
		name := p.NetworkName(key)
		if w.Exists("network", name) {
			continue
		}
		if truthy(n.Get("external")) {
			return fmt.Errorf("network %s declared as external, but could not be found", name)
		}
		args := []string{"network", "create", "-l", lProject + "=" + p.Name, "-l", lNetwork + "=" + key}
		if d := n.Str("driver"); d != "" {
			args = append(args, "-d", d)
		}
		opts := n.Map("driver_opts")
		for _, k := range opts.Keys() {
			args = append(args, "-o", k+"="+opts.Str(k))
		}
		labels := toDict(n.Get("labels"))
		for _, k := range labels.Keys() {
			args = append(args, "-l", k+"="+labels.Str(k))
		}
		if truthy(n.Get("internal")) {
			args = append(args, "--internal")
		}
		if cfgs := asList(n.Map("ipam").Get("config")); len(cfgs) > 0 {
			ipam, _ := cfgs[0].(*OMap)
			for _, kv := range [][2]string{{"subnet", "--subnet"}, {"gateway", "--gateway"}, {"ip_range", "--ip-range"}} {
				if v := ipam.Str(kv[0]); v != "" {
					args = append(args, kv[1], v)
				}
			}
		}
		if err := w.RunQ(append(args, name)...); err != nil {
			return err
		}
		status("Network", name, "Created")
	}
	return nil
}

func ensureVolumes(w *Wslc, p *Project, services []string) error {
	var used []string
	for _, s := range services {
		for _, v := range asList(p.Svc(s).Get("volumes")) {
			vtype, src, _, _, err := parseVolume(s, v)
			if err != nil {
				return err
			}
			if vtype == "volume" && p.Volumes.Has(src) && !contains(used, src) {
				used = append(used, src)
			}
		}
	}
	for _, key := range used {
		v := p.Volumes.Map(key)
		name := p.VolumeName(key)
		if w.Exists("volume", name) {
			continue
		}
		if truthy(v.Get("external")) {
			return fmt.Errorf("external volume %q not found", name)
		}
		args := []string{"volume", "create", "-l", lProject + "=" + p.Name, "-l", lVolume + "=" + key}
		if d := v.Str("driver"); d != "" {
			args = append(args, "-d", d)
		}
		opts := v.Map("driver_opts")
		for _, k := range opts.Keys() {
			args = append(args, "-o", k+"="+opts.Str(k))
		}
		labels := toDict(v.Get("labels"))
		for _, k := range labels.Keys() {
			args = append(args, "-l", k+"="+labels.Str(k))
		}
		if err := w.RunQ(append(args, name)...); err != nil {
			return err
		}
		status("Volume", name, "Created")
	}
	return nil
}

type buildOpts struct {
	NoCache, Pull bool
	BuildArgs     []string
}

func buildService(w *Wslc, p *Project, svc string, o buildOpts) error {
	b := p.Svc(svc).Map("build")
	if b == nil {
		b = NewOMap()
		b.Set("context", p.Svc(svc).Str("build"))
	}
	ctxDir := b.Str("context")
	if ctxDir == "" {
		ctxDir = "."
	}
	if !filepath.IsAbs(ctxDir) {
		ctxDir = filepath.Join(p.Workdir, ctxDir)
	}
	args := []string{"build", "-t", p.Image(svc)}
	var stdin *strings.Reader
	if inline := b.Str("dockerfile_inline"); inline != "" {
		args = append(args, "-f", "-")
		stdin = strings.NewReader(inline)
	} else if df := b.Str("dockerfile"); df != "" {
		if !filepath.IsAbs(df) {
			df = filepath.Join(ctxDir, df)
		}
		args = append(args, "-f", df)
	}
	bargs := toDict(b.Get("args"))
	for _, k := range bargs.Keys() {
		if bargs.Get(k) == nil {
			args = append(args, "--build-arg", k)
		} else {
			args = append(args, "--build-arg", k+"="+bargs.Str(k))
		}
	}
	for _, a := range o.BuildArgs {
		args = append(args, "--build-arg", a)
	}
	if t := b.Str("target"); t != "" {
		args = append(args, "--target", t)
	}
	labels := toDict(b.Get("labels"))
	for _, k := range labels.Keys() {
		args = append(args, "-l", k+"="+labels.Str(k))
	}
	for _, t := range asList(b.Get("tags")) {
		args = append(args, "-t", scalarStr(t))
	}
	if o.NoCache || truthy(b.Get("no_cache")) {
		args = append(args, "--no-cache")
	}
	if o.Pull || truthy(b.Get("pull")) {
		args = append(args, "--pull")
	}
	args = append(args, ctxDir)
	fmt.Fprintf(os.Stderr, " Service %s  Building\n", svc)
	var err error
	if stdin != nil {
		err = w.check(args, false, stdin)
	} else {
		err = w.Run(args...)
	}
	if err != nil {
		return fmt.Errorf("failed to build service %q: %w", svc, err)
	}
	status("Service", svc, "Built")
	return nil
}

func ensureImages(w *Wslc, p *Project, services []string, build, noBuild bool) error {
	for _, s := range services {
		c := p.Svc(s)
		image := p.Image(s)
		policy := c.Str("pull_policy")
		if policy == "" {
			policy = "missing"
		}
		exists := w.Exists("image", image)
		hasBuild := c.Has("build")
		switch {
		case hasBuild && !noBuild && (build || policy == "build" || !exists):
			// image と build が両方ある場合は、まず pull を試し、取得できなければビルドする (compose 仕様)
			if !build && policy == "missing" && c.Has("image") {
				if rc, err := w.RunRC("pull", image); err == nil && rc == 0 {
					continue
				}
				warn(fmt.Sprintf("pull of %s failed, building from source", image))
			}
			if err := buildService(w, p, s, buildOpts{}); err != nil {
				return err
			}
		case !hasBuild && (policy == "always" || (policy != "never" && !exists)):
			if err := w.Run("pull", image); err != nil {
				return err
			}
		case !exists && !w.dryRun:
			return fmt.Errorf("image %s for service %q not found", image, s)
		}
	}
	return nil
}

func imageID(w *Wslc, p *Project, svc string) string {
	d, err := w.Inspect("image", p.Image(svc))
	if err != nil || len(d) == 0 {
		return ""
	}
	id, _ := d[0]["Id"].(string)
	return id
}

func waitDependencies(w *Wslc, p *Project, svc string) error {
	if w.dryRun {
		return nil
	}
	deps, err := p.DependsOn(svc)
	if err != nil {
		return err
	}
	for _, d := range deps {
		if d.Cond == "service_started" {
			continue
		}
		cname := p.ContainerName(d.Name)
		status("Container", cname, "Waiting")
		for {
			st, err := containerState(w, cname)
			if err != nil {
				return err
			}
			stStatus, _ := st["Status"].(string)
			code, _ := st["ExitCode"].(float64)
			if d.Cond == "service_healthy" {
				health, ok := dig(st, "Health", "Status").(string)
				if !ok {
					return fmt.Errorf("container %s has no healthcheck configured", cname)
				}
				if health == "healthy" {
					status("Container", cname, "Healthy")
					break
				}
				if health == "unhealthy" {
					return fmt.Errorf("dependency failed to start: container %s is unhealthy", cname)
				}
				if stStatus == "exited" {
					return fmt.Errorf("dependency failed to start: container %s exited", cname)
				}
			} else if d.Cond == "service_completed_successfully" && stStatus == "exited" {
				if code != 0 {
					return fmt.Errorf("service %q didn't complete successfully: exit %d", d.Name, int(code))
				}
				status("Container", cname, "Exited")
				break
			}
			time.Sleep(time.Second)
		}
	}
	return nil
}

// converge はサービスのコンテナを設定どおりの状態にして起動する。
func converge(w *Wslc, p *Project, svc string, existing map[string]Ctr, forceRecreate, noRecreate bool) (string, error) {
	cname := p.ContainerName(svc)
	args, connects, err := createArgs(p, svc, cname, createOpts{})
	if err != nil {
		return "", err
	}
	h := configHash(args, connects, imageID(w, p, svc))
	cur, found := existing[svc]
	if err := waitDependencies(w, p, svc); err != nil {
		return "", err
	}
	if found && (noRecreate || (cur.Hash == h && !forceRecreate)) {
		if cur.State == "running" {
			status("Container", cname, "Running")
		} else {
			if err := w.RunQ("start", cname); err != nil {
				return "", err
			}
			status("Container", cname, "Started")
		}
		return cname, nil
	}
	if found {
		if err := w.RunQ("rm", "-f", cur.Name); err != nil {
			return "", err
		}
		status("Container", cname, "Recreated")
	}
	// "create --name <cname>" の直後にハッシュラベルを挿入
	args = append(append(append([]string{}, args[:3]...), "-l", lHash+"="+h), args[3:]...)
	if err := w.RunQ(args...); err != nil {
		return "", err
	}
	for _, conn := range connects {
		if err := w.RunQ(conn...); err != nil {
			return "", err
		}
	}
	if !found {
		status("Container", cname, "Created")
	}
	if err := w.RunQ("start", cname); err != nil {
		return "", err
	}
	status("Container", cname, "Started")
	return cname, nil
}
