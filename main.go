// wslc-compose: wslc (WSL Container CLI) をバックエンドにした docker compose 互換 CLI.
package main

import (
	"fmt"
	"os"
	"strings"
)

var version = "0.2.0"

type Flag struct {
	Short, Long string
	Arg         bool
	Help        string
}

type Parsed struct {
	bools map[string]bool
	vals  map[string][]string
	Pos   []string
}

func (p *Parsed) Bool(k string) bool     { return p.bools[k] }
func (p *Parsed) Has(k string) bool      { return len(p.vals[k]) > 0 }
func (p *Parsed) List(k string) []string { return p.vals[k] }

func (p *Parsed) Str(k string) string {
	if v := p.vals[k]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

var helpFlag = Flag{"h", "help", false, "ヘルプを表示する"}

// parseFlags はフラグと位置引数を解析する。stopAfter 個の位置引数を得た後の引数はすべて位置引数として扱う
// (-1 なら制限なし)。
func parseFlags(args []string, flags []Flag, stopAfter int) (*Parsed, error) {
	p := &Parsed{bools: map[string]bool{}, vals: map[string][]string{}}
	flags = append(flags, helpFlag)
	lookup := func(name string, short bool) *Flag {
		for i := range flags {
			if (short && flags[i].Short == name) || (!short && flags[i].Long == name) {
				return &flags[i]
			}
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if stopAfter >= 0 && len(p.Pos) >= stopAfter {
			p.Pos = append(p.Pos, args[i:]...)
			break
		}
		switch {
		case a == "--":
			p.Pos = append(p.Pos, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			f := lookup(name, false)
			if f == nil {
				return nil, fmt.Errorf("unknown flag: --%s", name)
			}
			if !f.Arg {
				p.bools[f.Long] = true
				continue
			}
			if !hasVal {
				if i++; i >= len(args) {
					return nil, fmt.Errorf("flag needs an argument: --%s", name)
				}
				val = args[i]
			}
			p.vals[f.Long] = append(p.vals[f.Long], val)
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				f := lookup(a[j:j+1], true)
				if f == nil {
					return nil, fmt.Errorf("unknown shorthand flag: %q in %s", a[j], a)
				}
				if !f.Arg {
					p.bools[f.Long] = true
					continue
				}
				val := a[j+1:]
				if val == "" {
					if i++; i >= len(args) {
						return nil, fmt.Errorf("flag needs an argument: -%s", f.Short)
					}
					val = args[i]
				}
				p.vals[f.Long] = append(p.vals[f.Long], val)
				break
			}
		default:
			p.Pos = append(p.Pos, a)
		}
	}
	return p, nil
}

type Command struct {
	Name, Usage, Help string
	Flags             []Flag
	StopAfter         int
	NoProject         bool
	Fn                func(*Wslc, *Project, *Parsed) (int, error)
}

func b(short, long, help string) Flag { return Flag{short, long, false, help} }
func s(short, long, help string) Flag { return Flag{short, long, true, help} }

var globalFlags = []Flag{
	s("f", "file", "Compose 設定ファイル (複数指定可)"),
	s("p", "project-name", "プロジェクト名"),
	s("", "project-directory", "作業ディレクトリを指定する"),
	s("", "env-file", "別の環境変数ファイルを指定する"),
	s("", "profile", "有効にするプロファイルを指定する"),
	s("", "session", "使用する wslc セッション"),
	b("", "dry-run", "コマンドを実行せず、実行内容だけを表示する"),
	b("v", "version", "wslc-compose のバージョン情報を表示する"),
}

var commands = []Command{
	{"up", "[SERVICE...]", "コンテナを作成して起動する", []Flag{
		b("d", "detach", "デタッチモード: コンテナをバックグラウンドで実行する"),
		b("", "build", "コンテナ起動前にイメージをビルドする"),
		b("", "no-build", "イメージがなくてもビルドしない"),
		b("", "force-recreate", "設定が変わっていなくてもコンテナを再作成する"),
		b("", "no-recreate", "コンテナが既に存在する場合は再作成しない"),
		b("", "no-deps", "依存サービスを起動しない"),
		b("", "remove-orphans", "Compose ファイルに定義されていないサービスのコンテナを削除する"),
		b("", "no-color", "モノクロで出力する"),
		b("", "no-log-prefix", "ログの先頭にコンテナ名を付けない"),
		b("", "no-log-history", "アタッチ前に出力されたログを表示しない"),
	}, -1, false, cmdUp},
	{"down", "", "コンテナとネットワークを停止して削除する", []Flag{
		b("v", "volumes", "volumes セクションで宣言した名前付きボリュームも削除する"),
		s("", "rmi", `サービスが使うイメージも削除する ("local"|"all")`),
		b("", "remove-orphans", "Compose ファイルに定義されていないサービスのコンテナを削除する"),
		s("t", "timeout", "停止のタイムアウト秒数"),
	}, -1, false, cmdDown},
	{"ps", "[SERVICE...]", "コンテナを一覧表示する", []Flag{
		b("a", "all", "停止中のコンテナも表示する"),
		b("q", "quiet", "ID のみ表示する"),
		b("", "services", "サービス名を表示する"),
		s("", "format", "出力形式 (table|json)"),
	}, -1, false, cmdPs},
	{"logs", "[SERVICE...]", "コンテナの出力を表示する", []Flag{
		b("f", "follow", "ログを追従表示する"),
		s("n", "tail", "末尾から表示する行数"),
		b("t", "timestamps", "タイムスタンプを表示する"),
		s("", "since", "指定日時以降のログを表示する"),
		s("", "until", "指定日時より前のログを表示する"),
		b("", "no-color", "モノクロで出力する"),
		b("", "no-log-prefix", "ログの先頭にコンテナ名を付けない"),
	}, -1, false, cmdLogs},
	{"build", "[SERVICE...]", "サービスのイメージをビルド (再ビルド) する", []Flag{
		b("", "no-cache", "ビルド時にキャッシュを使わない"),
		b("", "pull", "常に新しいバージョンのベースイメージの取得を試みる"),
		s("", "build-arg", "ビルド時の変数を設定する (KEY=VALUE)"),
	}, -1, false, cmdBuild},
	{"pull", "[SERVICE...]", "サービスのイメージを取得する", []Flag{
		b("q", "quiet", "進行状況を表示せずに取得する"),
		b("", "ignore-buildable", "ビルド可能なイメージは無視する"),
	}, -1, false, cmdPull},
	{"start", "[SERVICE...]", "サービスを開始する", nil, -1, false, cmdStart},
	{"stop", "[SERVICE...]", "サービスを停止する", []Flag{
		s("t", "timeout", "停止のタイムアウト秒数"),
	}, -1, false, cmdStop},
	{"restart", "[SERVICE...]", "サービスのコンテナを再起動する", []Flag{
		s("t", "timeout", "停止のタイムアウト秒数"),
	}, -1, false, cmdRestart},
	{"kill", "[SERVICE...]", "サービスのコンテナを強制停止する", []Flag{
		s("s", "signal", "コンテナに送るシグナル"),
	}, -1, false, cmdKill},
	{"rm", "[SERVICE...]", "停止しているサービスのコンテナを削除する", []Flag{
		b("f", "force", "削除の確認をしない"),
		b("s", "stop", "必要に応じて、削除前にコンテナを停止する"),
		b("v", "volumes", "コンテナに接続された匿名ボリュームも削除する"),
	}, -1, false, cmdRm},
	{"exec", "SERVICE COMMAND [ARGS...]", "実行中のコンテナでコマンドを実行する", []Flag{
		b("d", "detach", "デタッチモード: コマンドをバックグラウンドで実行する"),
		b("T", "no-TTY", "疑似 TTY を割り当てない"),
		s("e", "env", "環境変数を設定する"),
		s("u", "user", "このユーザーでコマンドを実行する"),
		s("w", "workdir", "コマンドの作業ディレクトリ"),
	}, 1, false, cmdExec},
	{"run", "SERVICE [COMMAND] [ARGS...]", "サービスで単発のコマンドを実行する", []Flag{
		b("d", "detach", "コンテナをバックグラウンドで実行し、コンテナ名を表示する"),
		b("", "rm", "終了時にコンテナを自動削除する"),
		s("", "name", "コンテナ名を指定する"),
		s("", "entrypoint", "イメージのエントリポイントを上書きする"),
		s("e", "env", "環境変数を設定する"),
		s("l", "label", "ラベルを追加・上書きする"),
		s("v", "volume", "ボリュームをバインドマウントする"),
		s("p", "publish", "コンテナのポートをホストに公開する"),
		s("u", "user", "指定したユーザー名または UID で実行する"),
		s("w", "workdir", "コンテナ内の作業ディレクトリ"),
		b("T", "no-TTY", "疑似 TTY を割り当てない"),
		b("", "no-deps", "依存サービスを起動しない"),
		b("", "service-ports", "サービスに定義されたポートを公開して実行する"),
		b("", "build", "コンテナ起動前にイメージをビルドする"),
	}, 1, false, cmdRun},
	{"config", "", "Compose ファイルを解析・展開して表示する", []Flag{
		b("q", "quiet", "設定の検証のみ行い、何も表示しない"),
		b("", "services", "サービス名を 1 行ずつ表示する"),
		b("", "volumes", "ボリューム名を 1 行ずつ表示する"),
	}, -1, false, cmdConfig},
	{"images", "[SERVICE...]", "作成したコンテナが使うイメージを一覧表示する", nil, -1, false, cmdImages},
	{"version", "", "バージョン情報を表示する", nil, -1, true, cmdVersion},
}

func printFlags(flags []Flag) {
	for _, f := range append(flags, helpFlag) {
		name := "    --" + f.Long
		if f.Short != "" {
			name = "-" + f.Short + ", --" + f.Long
		}
		if f.Arg {
			name += " string"
		}
		fmt.Printf("  %-30s %s\n", name, f.Help)
	}
}

func printUsage() {
	fmt.Println("使用法:  wslc-compose [オプション] コマンド\n\nwslc で複数コンテナのアプリケーションを定義・実行します。\n\nオプション:")
	printFlags(globalFlags)
	fmt.Println("\nコマンド:")
	for _, c := range commands {
		fmt.Printf("  %-10s %s\n", c.Name, c.Help)
	}
	fmt.Println("\n各コマンドの詳細は 'wslc-compose コマンド --help' で表示できます。")
}

func run(argv []string) int {
	enableVT()
	g, err := parseFlags(argv, globalFlags, 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if g.Bool("version") {
		fmt.Println("wslc-compose version " + version)
		return 0
	}
	if g.Bool("help") || len(g.Pos) == 0 {
		printUsage()
		return 0
	}
	var cmd *Command
	for i := range commands {
		if commands[i].Name == g.Pos[0] {
			cmd = &commands[i]
		}
	}
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "error: unknown command %q for \"wslc-compose\"\n", g.Pos[0])
		return 1
	}
	a, err := parseFlags(g.Pos[1:], cmd.Flags, cmd.StopAfter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if a.Bool("help") {
		fmt.Printf("使用法:  wslc-compose %s [オプション] %s\n\n%s\n\nオプション:\n", cmd.Name, cmd.Usage, cmd.Help)
		printFlags(cmd.Flags)
		return 0
	}
	w := NewWslc(g.Str("session"), g.Bool("dry-run"))
	var p *Project
	if !cmd.NoProject {
		p, err = loadProject(&GlobalOpts{
			Files: g.List("file"), ProjectName: g.Str("project-name"), ProjectDir: g.Str("project-directory"),
			EnvFile: g.Str("env-file"), Profiles: g.List("profile"),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	rc, err := cmd.Fn(w, p, a)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if rc == 0 {
			rc = 1
		}
	}
	return rc
}

func main() { os.Exit(run(os.Args[1:])) }
