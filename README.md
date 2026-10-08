# wslc-compose

`wslc`（WSL Container CLI）をバックエンドにした、`docker compose` 互換の CLI です。
`compose.yaml` を読み込み、`wslc` のコマンド（network / volume / build / pull / create / start …）に変換して実行します。

Go 製の単一実行ファイル（約 4MB、外部ランタイム不要）です。

## 必要なもの

- 実行: Windows + `wslc`（`C:\Program Files\WSL\wslc.exe`、PATH 上にあること）
- ビルド: Go 1.22 以上

## ビルド

`build.bat` を実行すると、`build\wslc-compose.exe` が作成されます。

```bash
build.bat
```

- 埋め込むバージョンは `build.bat` 先頭の `SET VERSION=...` で指定します（`wslc-compose --version` で確認できます）
- Go が PATH にない場合は `C:\Program Files\Go\bin` を使います

バッチを使わずにビルドする場合:

```bash
go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o build\wslc-compose.exe .
```

依存ライブラリは `gopkg.in/yaml.v3` のみです。

## 使い方

`wslc-compose.exe` を PATH の通ったフォルダに置くか、フルパスで実行します。

```bash
wslc-compose up -d
```

```bash
wslc-compose ps
```

```bash
wslc-compose logs -f app
```

```bash
wslc-compose down -v
```

### グローバルオプション

| オプション | 説明 |
|---|---|
| `-f, --file` | compose ファイル（複数指定でマージ）。省略時はカレント→親ディレクトリの順に `compose.yaml` / `compose.yml` / `docker-compose.yaml` / `docker-compose.yml` を探索 |
| `-p, --project-name` | プロジェクト名（省略時: `COMPOSE_PROJECT_NAME` → トップレベル `name` → ディレクトリ名） |
| `--project-directory` | 作業ディレクトリ |
| `--env-file` | 変数展開に使う env ファイル（既定: `<project>/.env`） |
| `--profile` | 有効にするプロファイル（`COMPOSE_PROFILES` も可） |
| `--session` | `wslc --session` に渡すセッション |
| `--dry-run` | 状態を変更する wslc コマンドを表示するだけで実行しない |

### コマンド

| コマンド | 主なオプション |
|---|---|
| `up [SERVICE...]` | `-d`, `--build`, `--no-build`, `--force-recreate`, `--no-recreate`, `--no-deps`, `--remove-orphans`, `--no-color`, `--no-log-prefix` |
| `down` | `-v`, `--rmi local\|all`, `--remove-orphans`, `-t` |
| `ps [SERVICE...]` | `-a`, `-q`, `--services`, `--format table\|json` |
| `logs [SERVICE...]` | `-f`, `-n/--tail`, `-t`, `--since`, `--until`, `--no-color`, `--no-log-prefix` |
| `build [SERVICE...]` | `--no-cache`, `--pull`, `--build-arg` |
| `pull [SERVICE...]` | `-q`, `--ignore-buildable` |
| `start` / `stop` / `restart` / `kill` / `rm` | `stop`/`restart`: `-t`、`kill`: `-s`、`rm`: `-f`, `-s`, `-v` |
| `exec SERVICE CMD...` | `-d`, `-T`, `-e`, `-u`, `-w` |
| `run SERVICE [CMD...]` | `--rm`, `-d`, `--name`, `--entrypoint`, `-e`, `-l`, `-v`, `-p`, `-u`, `-w`, `-T`, `--no-deps`, `--service-ports`, `--build` |
| `config` | `-q`, `--services`, `--volumes` |
| `images [SERVICE...]` | |
| `version` | |

## 対応している compose の機能

- 変数展開: `$VAR`, `${VAR}`, `${VAR:-def}`, `${VAR-def}`, `${VAR:?err}`, `${VAR?err}`, `${VAR:+alt}`, `$$`、`.env` 読み込み
- サービス: `image`, `build`（`context`, `dockerfile`, `dockerfile_inline`, `args`, `target`, `labels`, `tags`, `no_cache`, `pull`）, `container_name`, `hostname`, `domainname`, `command`, `entrypoint`, `environment`, `env_file`, `ports`（短縮/長形式）, `volumes`（名前付き/バインド/tmpfs、短縮/長形式）, `networks`（`aliases`, `ipv4_address`）, `network_mode`（`host`/`none`/`bridge`）, `depends_on`（`service_started` / `service_healthy` / `service_completed_successfully`）, `healthcheck`, `labels`, `user`, `working_dir`, `stdin_open`, `tty`, `cpus`, `mem_limit`, `shm_size`, `stop_signal`, `stop_grace_period`, `tmpfs`, `ulimits`, `dns`, `dns_search`, `dns_opt`, `gpus`, `deploy.resources`（limits の cpus/memory、GPU 予約）, `pull_policy`, `profiles`
- トップレベル: `name`, `networks`（`driver`, `driver_opts`, `internal`, `ipam`, `external`, `name`, `labels`）, `volumes`（`driver`, `driver_opts`, `external`, `name`, `labels`）
- 設定ハッシュ（ラベル `com.wslc.compose.config-hash`）による差分検出: 設定やイメージが変わったサービスだけ再作成
- YAML アンカー / エイリアス / マージキー（`<<: *default`）

## ラベル

作成したコンテナ・ネットワーク・ボリュームには、docker compose と区別するため `com.wslc.compose.*` のラベルを付けます。

| ラベル | 対象 |
|---|---|
| `com.wslc.compose.project` | コンテナ / ネットワーク / ボリューム |
| `com.wslc.compose.service` | コンテナ |
| `com.wslc.compose.oneoff` | コンテナ（`run` で作成したものは `True`） |
| `com.wslc.compose.config-hash` | コンテナ |
| `com.wslc.compose.project.working_dir` | コンテナ |
| `com.wslc.compose.network` / `com.wslc.compose.volume` | ネットワーク / ボリューム |

## 制限事項

- `restart`, `privileged`, `cap_add`, `extra_hosts`, `secrets`, `configs`, `logging` などwslc に対応オプションのないキーは警告を出して無視します
- ボリュームのモードは `ro` / `rw` のみ wslc に渡します（`cached` / `delegated` / `consistent` は docker と同様に無視、`z` / `nocopy` などは警告して無視）
- 1 サービス 1 コンテナ（`scale` / `deploy.replicas` 非対応）
- `network_mode: service:xxx` / `container:xxx`、`extends`、`include` は非対応
- 複数ファイルのマージは「マップは深くマージ、それ以外は上書き」の単純なルール（`environment` / `labels` はキー単位でマージ）
- `up`（フォアグラウンド）は Ctrl+C でコンテナを停止して終了します。全コンテナが終了した場合も終了します
