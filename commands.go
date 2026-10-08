package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

func prepare(w *Wslc, p *Project, services []string, build, noBuild bool) error {
	if err := ensureNetworks(w, p, services); err != nil {
		return err
	}
	if err := ensureVolumes(w, p, services); err != nil {
		return err
	}
	return ensureImages(w, p, services, build, noBuild)
}

func existingByService(w *Wslc, p *Project) ([]Ctr, map[string]Ctr, error) {
	ctrs, err := listContainers(w, p, nil, false)
	m := map[string]Ctr{}
	for _, c := range ctrs {
		m[c.Service] = c
	}
	return ctrs, m, err
}

func cmdUp(w *Wslc, p *Project, a *Parsed) (int, error) {
	services, err := p.Resolve(a.Pos, !a.Bool("no-deps"))
	if err != nil {
		return 1, err
	}
	if err := prepare(w, p, services, a.Bool("build"), a.Bool("no-build")); err != nil {
		return 1, err
	}
	ctrs, existing, err := existingByService(w, p)
	if err != nil {
		return 1, err
	}
	var orphans []string
	for _, c := range ctrs {
		if !p.Services.Has(c.Service) {
			orphans = append(orphans, c.Name)
		}
	}
	if len(orphans) > 0 {
		if a.Bool("remove-orphans") {
			for _, n := range orphans {
				w.Try("rm", "-f", n)
				status("Container", n, "Removed")
			}
		} else {
			warn(fmt.Sprintf("Found orphan containers (%s) for this project. If you removed or renamed this "+
				"service in your compose file, you can run this command with the --remove-orphans flag to clean it up.",
				strings.Join(orphans, ", ")))
		}
	}
	var names []string
	for _, s := range services {
		n, err := converge(w, p, s, existing, a.Bool("force-recreate"), a.Bool("no-recreate"))
		if err != nil {
			return 1, err
		}
		names = append(names, n)
	}
	if a.Bool("detach") || w.dryRun {
		return 0, nil
	}
	fmt.Fprintln(os.Stderr, "Attaching to "+strings.Join(names, ", "))
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	opts := []string{"-f"}
	if a.Bool("no-log-history") {
		opts = append(opts, "-n", "0")
	}
	select {
	case <-followLogs(w, names, opts, !a.Bool("no-color"), !a.Bool("no-log-prefix")):
	case <-sig:
		fmt.Fprintln(os.Stderr, "\nGracefully stopping... (press Ctrl+C again to force)")
		signal.Stop(sig)
		for i := len(names) - 1; i >= 0; i-- {
			w.Try("stop", names[i])
			status("Container", names[i], "Stopped")
		}
	}
	return 0, nil
}

func timeoutArgs(a *Parsed) []string {
	if a.Has("timeout") {
		return []string{"-t", a.Str("timeout")}
	}
	return nil
}

func cmdDown(w *Wslc, p *Project, a *Parsed) (int, error) {
	rmi := a.Str("rmi")
	if rmi != "" && rmi != "local" && rmi != "all" {
		return 1, fmt.Errorf("--rmi must be 'local' or 'all'")
	}
	order, err := p.Resolve(p.Services.Keys(), true)
	if err != nil {
		return 1, err
	}
	ctrs, err := listContainers(w, p, nil, true)
	if err != nil {
		return 1, err
	}
	idx := func(s string) int {
		for i, x := range order {
			if x == s {
				return i
			}
		}
		return len(order)
	}
	sort.SliceStable(ctrs, func(i, j int) bool { return idx(ctrs[i].Service) < idx(ctrs[j].Service) })
	for i := len(ctrs) - 1; i >= 0; i-- {
		c := ctrs[i]
		if !p.Services.Has(c.Service) && !a.Bool("remove-orphans") {
			continue
		}
		if c.State == "running" {
			w.Try(append(append([]string{"stop"}, timeoutArgs(a)...), c.Name)...)
			status("Container", c.Name, "Stopped")
		}
		w.Try("rm", "-f", c.Name)
		status("Container", c.Name, "Removed")
	}
	nets, err := w.JSONLines("network", "ls", "--format", "json", "-f", "label="+lProject+"="+p.Name)
	if err != nil {
		return 1, err
	}
	for _, n := range nets {
		name, _ := n["Name"].(string)
		w.Try("network", "rm", name)
		status("Network", name, "Removed")
	}
	if a.Bool("volumes") {
		vols, err := w.JSONLines("volume", "ls", "--format", "json", "-f", "label="+lProject+"="+p.Name)
		if err != nil {
			return 1, err
		}
		for _, v := range vols {
			name, _ := v["Name"].(string)
			w.Try("volume", "rm", name)
			status("Volume", name, "Removed")
		}
	}
	if rmi != "" {
		for _, s := range p.Services.Keys() {
			if rmi == "all" || !p.Svc(s).Has("image") {
				img := p.Image(s)
				if w.Exists("image", img) {
					w.Try("rmi", img)
					status("Image", img, "Removed")
				}
			}
		}
	}
	return 0, nil
}

func printTable(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for _, r := range append([][]string{headers}, rows...) {
		for i, x := range r {
			if n := utf8.RuneCountInString(x); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, r := range append([][]string{headers}, rows...) {
		var sb strings.Builder
		for i, x := range r {
			sb.WriteString(x + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(x)))
			if i < len(r)-1 {
				sb.WriteString("   ")
			}
		}
		fmt.Println(strings.TrimRight(sb.String(), " "))
	}
}

func cmdPs(w *Wslc, p *Project, a *Parsed) (int, error) {
	if a.Bool("services") {
		fmt.Println(strings.Join(p.Active, "\n"))
		return 0, nil
	}
	if _, err := p.Resolve(a.Pos, false); err != nil {
		return 1, err
	}
	ctrs, err := listContainers(w, p, a.Pos, true)
	if err != nil {
		return 1, err
	}
	var shown []Ctr
	for _, c := range ctrs {
		if a.Bool("all") || c.State == "running" {
			shown = append(shown, c)
		}
	}
	if a.Bool("quiet") {
		for _, c := range shown {
			fmt.Println(c.ID)
		}
		return 0, nil
	}
	list, err := w.JSONLines("ps", "-a", "--format", "json", "-f", "label="+lProject+"="+p.Name)
	if err != nil {
		return 1, err
	}
	ps := map[string]map[string]any{}
	for _, d := range list {
		if n, ok := d["Names"].(string); ok {
			ps[n] = d
		}
	}
	type row struct{ Name, Image, Command, Service, Created, Status, Ports string }
	var rows []row
	for _, c := range shown {
		d := ps[c.Name]
		s := func(k string) string { v, _ := d[k].(string); return v }
		st := s("Status")
		if st == "" {
			st = c.State
		}
		rows = append(rows, row{c.Name, s("Image"), s("Command"), c.Service, s("RunningFor"), st, s("Ports")})
	}
	if a.Str("format") == "json" {
		if rows == nil {
			rows = []row{}
		}
		b, _ := json.Marshal(rows)
		fmt.Println(string(b))
		return 0, nil
	}
	var table [][]string
	for _, r := range rows {
		table = append(table, []string{r.Name, r.Image, r.Command, r.Service, r.Created, r.Status, r.Ports})
	}
	printTable([]string{"NAME", "IMAGE", "COMMAND", "SERVICE", "CREATED", "STATUS", "PORTS"}, table)
	return 0, nil
}

func cmdLogs(w *Wslc, p *Project, a *Parsed) (int, error) {
	if _, err := p.Resolve(a.Pos, false); err != nil {
		return 1, err
	}
	ctrs, err := listContainers(w, p, a.Pos, false)
	if err != nil {
		return 1, err
	}
	var names []string
	for _, c := range ctrs {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return 0, nil
	}
	var opts []string
	if a.Bool("follow") {
		opts = append(opts, "-f")
	}
	if t := a.Str("tail"); t != "" && t != "all" {
		opts = append(opts, "-n", t)
	}
	if a.Bool("timestamps") {
		opts = append(opts, "-t")
	}
	for _, k := range []string{"since", "until"} {
		if v := a.Str(k); v != "" {
			opts = append(opts, "--"+k, v)
		}
	}
	color, prefix := !a.Bool("no-color"), !a.Bool("no-log-prefix")
	if !a.Bool("follow") {
		lp := newLogPrinter(names, color, prefix)
		for _, n := range names {
			streamLogs(w, lp, n, opts)
		}
		return 0, nil
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case <-followLogs(w, names, opts, color, prefix):
	case <-sig:
	}
	return 0, nil
}

func cmdBuild(w *Wslc, p *Project, a *Parsed) (int, error) {
	services, err := p.Resolve(a.Pos, false)
	if err != nil {
		return 1, err
	}
	for _, s := range services {
		if p.Svc(s).Has("build") {
			o := buildOpts{NoCache: a.Bool("no-cache"), Pull: a.Bool("pull"), BuildArgs: a.List("build-arg")}
			if err := buildService(w, p, s, o); err != nil {
				return 1, err
			}
		}
	}
	return 0, nil
}

func cmdPull(w *Wslc, p *Project, a *Parsed) (int, error) {
	services, err := p.Resolve(a.Pos, false)
	if err != nil {
		return 1, err
	}
	rc := 0
	for _, s := range services {
		c := p.Svc(s)
		if !c.Has("image") || (c.Has("build") && a.Bool("ignore-buildable")) {
			continue
		}
		args := []string{"pull"}
		if a.Bool("quiet") {
			args = append(args, "-q")
		}
		r, err := w.RunRC(append(args, c.Str("image"))...)
		if err != nil {
			return 1, err
		}
		if r != 0 && !c.Has("build") {
			rc = r
		}
	}
	return rc, nil
}

func existingTargets(w *Wslc, p *Project, services []string) ([]Ctr, error) {
	order, err := p.Resolve(services, false)
	if err != nil {
		return nil, err
	}
	ctrs, err := listContainers(w, p, order, false)
	if err != nil {
		return nil, err
	}
	var out []Ctr
	for _, s := range order {
		for _, c := range ctrs {
			if c.Service == s {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func cmdStart(w *Wslc, p *Project, a *Parsed) (int, error) {
	ctrs, err := existingTargets(w, p, a.Pos)
	if err != nil {
		return 1, err
	}
	for _, c := range ctrs {
		if err := waitDependencies(w, p, c.Service); err != nil {
			return 1, err
		}
		if err := w.RunQ("start", c.Name); err != nil {
			return 1, err
		}
		status("Container", c.Name, "Started")
	}
	return 0, nil
}

func cmdStop(w *Wslc, p *Project, a *Parsed) (int, error) {
	ctrs, err := existingTargets(w, p, a.Pos)
	if err != nil {
		return 1, err
	}
	for i := len(ctrs) - 1; i >= 0; i-- {
		if c := ctrs[i]; c.State == "running" {
			if err := w.RunQ(append(append([]string{"stop"}, timeoutArgs(a)...), c.Name)...); err != nil {
				return 1, err
			}
			status("Container", c.Name, "Stopped")
		}
	}
	return 0, nil
}

func cmdRestart(w *Wslc, p *Project, a *Parsed) (int, error) {
	ctrs, err := existingTargets(w, p, a.Pos)
	if err != nil {
		return 1, err
	}
	for _, c := range ctrs {
		if err := w.RunQ(append(append([]string{"restart"}, timeoutArgs(a)...), c.Name)...); err != nil {
			return 1, err
		}
		status("Container", c.Name, "Restarted")
	}
	return 0, nil
}

func cmdKill(w *Wslc, p *Project, a *Parsed) (int, error) {
	ctrs, err := existingTargets(w, p, a.Pos)
	if err != nil {
		return 1, err
	}
	for _, c := range ctrs {
		if c.State != "running" {
			continue
		}
		args := []string{"kill"}
		if s := a.Str("signal"); s != "" {
			args = append(args, "-s", s)
		}
		if err := w.RunQ(append(args, c.Name)...); err != nil {
			return 1, err
		}
		status("Container", c.Name, "Killed")
	}
	return 0, nil
}

func cmdRm(w *Wslc, p *Project, a *Parsed) (int, error) {
	ctrs, err := existingTargets(w, p, a.Pos)
	if err != nil {
		return 1, err
	}
	var targets []Ctr
	for _, c := range ctrs {
		if c.State == "running" && a.Bool("stop") {
			if err := w.RunQ("stop", c.Name); err != nil {
				return 1, err
			}
			c.State = "exited"
			status("Container", c.Name, "Stopped")
		}
		if c.State != "running" {
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "No stopped containers")
		return 0, nil
	}
	if !a.Bool("force") {
		var names []string
		for _, c := range targets {
			names = append(names, c.Name)
		}
		fmt.Fprintln(os.Stderr, "Going to remove "+strings.Join(names, ", "))
		fmt.Fprint(os.Stderr, "? Are you sure? (y/N) ")
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if ans = strings.ToLower(strings.TrimSpace(ans)); ans != "y" && ans != "yes" {
			return 0, nil
		}
	}
	for _, c := range targets {
		args := []string{"rm"}
		if a.Bool("volumes") {
			args = append(args, "-v")
		}
		if err := w.RunQ(append(args, c.Name)...); err != nil {
			return 1, err
		}
		status("Container", c.Name, "Removed")
	}
	return 0, nil
}

// interactive は対話実行中、Ctrl+C で自プロセスが終了しないようにする (子プロセスに任せる)。
func interactive(fn func() (int, error)) (int, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	return fn()
}

func cmdExec(w *Wslc, p *Project, a *Parsed) (int, error) {
	if len(a.Pos) < 2 {
		return 1, fmt.Errorf("exec requires a service and a command")
	}
	ctrs, err := listContainers(w, p, a.Pos[:1], false)
	if err != nil {
		return 1, err
	}
	var target string
	for _, c := range ctrs {
		if c.State == "running" {
			target = c.Name
			break
		}
	}
	if target == "" {
		return 1, fmt.Errorf("service %q is not running", a.Pos[0])
	}
	args := []string{"exec"}
	if a.Bool("detach") {
		args = append(args, "-d")
	} else {
		args = append(args, "-i")
		if !a.Bool("no-TTY") && isTerminal(os.Stdin) {
			args = append(args, "-t")
		}
	}
	for _, e := range a.List("env") {
		args = append(args, "-e", e)
	}
	if u := a.Str("user"); u != "" {
		args = append(args, "-u", u)
	}
	if wd := a.Str("workdir"); wd != "" {
		args = append(args, "-w", wd)
	}
	args = append(append(args, target), a.Pos[1:]...)
	return interactive(func() (int, error) { return w.RunRC(args...) })
}

func cmdRun(w *Wslc, p *Project, a *Parsed) (int, error) {
	if len(a.Pos) < 1 {
		return 1, fmt.Errorf("run requires a service")
	}
	svc := a.Pos[0]
	if !p.Services.Has(svc) {
		return 1, fmt.Errorf("no such service: %s", svc)
	}
	services := []string{svc}
	if !a.Bool("no-deps") {
		var err error
		if services, err = p.Resolve(services, true); err != nil {
			return 1, err
		}
	}
	if err := prepare(w, p, services, a.Bool("build"), false); err != nil {
		return 1, err
	}
	if len(services) > 1 {
		_, existing, err := existingByService(w, p)
		if err != nil {
			return 1, err
		}
		for _, d := range services[:len(services)-1] {
			if _, err := converge(w, p, d, existing, false, false); err != nil {
				return 1, err
			}
		}
	}
	if err := waitDependencies(w, p, svc); err != nil {
		return 1, err
	}

	cname := a.Str("name")
	if cname == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		cname = fmt.Sprintf("%s-%s-run-%s", p.Name, svc, hex.EncodeToString(b))
	}
	var extra []string
	for _, fk := range [][2]string{{"-e", "env"}, {"-l", "label"}, {"-v", "volume"}, {"-p", "publish"}} {
		for _, v := range a.List(fk[1]) {
			extra = append(extra, fk[0], v)
		}
	}
	if u := a.Str("user"); u != "" {
		extra = append(extra, "-u", u)
	}
	if wd := a.Str("workdir"); wd != "" {
		extra = append(extra, "-w", wd)
	}
	detach := a.Bool("detach")
	if !detach {
		extra = append(extra, "-i")
		if !a.Bool("no-TTY") && isTerminal(os.Stdin) {
			extra = append(extra, "-t")
		}
	}
	o := createOpts{Oneoff: true, Extra: extra, NoPorts: !a.Bool("service-ports")}
	if a.Has("entrypoint") {
		ep := a.Str("entrypoint")
		o.Entrypoint = &ep
	}
	if len(a.Pos) > 1 {
		o.Command = a.Pos[1:]
	}
	args, connects, err := createArgs(p, svc, cname, o)
	if err != nil {
		return 1, err
	}
	if err := w.RunQ(args...); err != nil {
		return 1, err
	}
	for _, conn := range connects {
		if err := w.RunQ(conn...); err != nil {
			return 1, err
		}
	}
	if detach {
		if err := w.RunQ("start", cname); err != nil {
			return 1, err
		}
		fmt.Println(cname)
		return 0, nil
	}
	_, err = interactive(func() (int, error) { return w.RunRC("start", "-a", "-i", cname) })
	rc := 0
	if !w.dryRun {
		if st, e := containerState(w, cname); e == nil {
			code, _ := st["ExitCode"].(float64)
			rc = int(code)
		}
	}
	if a.Bool("rm") {
		w.Try("rm", "-f", cname)
	}
	return rc, err
}

func cmdConfig(w *Wslc, p *Project, a *Parsed) (int, error) {
	switch {
	case a.Bool("services"):
		fmt.Println(strings.Join(p.Services.Keys(), "\n"))
	case a.Bool("volumes"):
		fmt.Println(strings.Join(p.Volumes.Keys(), "\n"))
	case !a.Bool("quiet"):
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(toNode(p.Config)); err != nil {
			return 1, err
		}
		enc.Close()
	}
	return 0, nil
}

func cmdImages(w *Wslc, p *Project, a *Parsed) (int, error) {
	services, err := p.Resolve(a.Pos, false)
	if err != nil {
		return 1, err
	}
	var rows [][]string
	for _, s := range services {
		img := p.Image(s)
		d, err := w.Inspect("image", img)
		if err != nil || len(d) == 0 {
			continue
		}
		repo, tag := img, "latest"
		if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
			repo, tag = img[:i], img[i+1:]
		}
		id, _ := d[0]["Id"].(string)
		id = strings.TrimPrefix(id, "sha256:")
		if len(id) > 12 {
			id = id[:12]
		}
		rows = append(rows, []string{p.ContainerName(s), repo, tag, id})
	}
	printTable([]string{"CONTAINER", "REPOSITORY", "TAG", "IMAGE ID"}, rows)
	return 0, nil
}

func cmdVersion(w *Wslc, p *Project, a *Parsed) (int, error) {
	fmt.Println("wslc-compose version " + version)
	_, _ = w.RunRC("version")
	return 0, nil
}
