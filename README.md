# kaigi

ペルソナ（知識 + 性格）が A2A（Agent2Agent）プロトコルで会話する会議アプリ。複数のペルソナが同じ議題について発言し、互いの発言に反応する。Vite + React (TypeScript) フロントエンドと Go バックエンド。

知識は pgvector によるベクトル検索と MinIO 上の原本ファイルの二段構えで取得し、性格はクエリ書き換え・リランキング・システムプロンプトの3段階で回答に作用する。応答は SSE でストリーミングし、会議は Postgres に永続化する。

## アーキテクチャ

ペルソナは1体1プロセスの独立した **A2A エージェント**。司会（moderator）が会議録を共有しながら参加者を逐次呼び出すことで、同じ知識ベースを見ながら互いの主張に反応する会議になる。

```
Browser ──REST/SSE──▶ frontend pod (web:Caddy + moderator:Go)
                          │
                          ├─ GET /registry/agents ──▶ discovery pod
                          │                            （エージェントカード・
                          │                             カタログ。自己登録+TTL）
                          │
                          ├─ A2A SendStreamingMessage ─▶ persona-critic pod
                          ├─ A2A SendStreamingMessage ─▶ persona-pragmatist pod
                          │     （参加者ごとに逐次。会議録を共有するので
                          │      後続の参加者は先行発言に反応できる）
                          │
postgres（1インスタンス）  │                            minio
 ├─ kaigi_meeting   ◀─────┘（司会が読み書き）            （原本ファイル。
 ├─ kaigi_registry  ◀── discovery                        persona pod が
 ├─ kaigi_persona   ◀── persona pod 全体で共有            Range GET で
 └─ kaigi_knowledge ◀── persona pod 全体で読む             コンテキスト拡張）
```

### ペルソナが情報処理に作用する3点

| 段階 | 性格のどの要素が効くか | 実装 |
|---|---|---|
| **① クエリ書き換え** | `Stance` + `Interests` | ペルソナが実際にどう検索するかを OpenAI に JSON 出力で考えさせ、最大3本のクエリに展開する |
| **② 性格重み付け** | `Interests`（重み付き） + `Skepticism` | `Final = (1-λ)·Relevance + λ·Affinity`。`Relevance` は pgvector のコサイン類似度、`Affinity` は関心トピック埋め込みとチャンクの cosine 加重和。`Skepticism` が高いほど、関連度の足切り閾値が上がる（採用する資料そのものが変わる） |
| **③ 生成** | `Stance` + `Verbosity` + `Skepticism` | システムプロンプトを組み立てる。会議の参加者名（自分以外）も含めるが、参加者集合が同じ限りバイト同一を保つので OpenAI の自動コンテキストキャッシュが効く |

`PERSONA_INFLUENCE`（λ）は環境変数。`0` で純粋な RAG に退化するので、性格の効きを A/B できる。

### ペルソナ同士の「相互のやりとり」の仕組み

ペルソナ pod 同士は直接通信しない（スター型）。代わりに、司会が各ラウンドで参加者を1人ずつ順番に呼び、そのたびに**その時点までの会議録全体**（同じラウンド内の先行発言も含む）を渡す。これにより:

```
Round 1: critic ──▶ 発言A（会議録: [議題]）
         pragmatist ──▶ 発言B（会議録: [議題, 発言A]）  ← 発言Aに反応できる
Round 2: critic ──▶ 発言C（会議録: [議題, 発言A, 発言B]） ← 発言Bに反論できる
```

参加者ループは**逐次でなければならない**。並列化すると同じラウンドの他者の発言が見えなくなり、議論ではなく独立したN本の回答になる（`internal/meeting/moderator_test.go` の `TestModeratorSequential` が回帰を防いでいる）。

## レイアウト

```
docker-compose.yml      postgres+pgvector(4DB) / minio / discovery / persona×2 / moderator / frontend / seed
k8s/                     Kubernetes マニフェスト（kind 前提）
  base/                  Kustomize base（namespace/config/secret/postgres/minio/discovery/personas/frontend/ingress）
  overlays/local/        kind 向け overlay（imagePullPolicy:Never、MinIO NodePort、ローカル秘密情報）
  kind-cluster.yaml       kind クラスタ定義
frontend/                Vite + React + TypeScript
  Dockerfile             node でビルド → Caddy で配信するマルチステージ
  Caddyfile              SPA フォールバック + /api を {$BACKEND_ADDR} へリバースプロキシ
  src/api/client.ts      エージェント一覧/会議 API の型付きクライアント、SSE ストリーム
  src/App.tsx            ルーティングのみ（/ → MeetingPage、/personas → PersonasPage）
  src/MeetingPage.tsx    会議 UI（参加者選択・ラウンド区切り・話者ごとの引用）
  src/PersonasPage.tsx   ペルソナ一覧画面（在席状況・人格詳細を30秒間隔で再取得）
  src/PersonaCard.tsx    ペルソナ1体分のカード（スタンス・懐疑度・冗長度・関心トピック）
backend/
  Dockerfile             moderator/discovery/persona/seed の4バイナリを distroless に載せる
  cmd/moderator/         REST+SSE（ブラウザ向け）、A2A クライアント（司会）
  cmd/discovery/         エージェントカード・カタログの HTTP API
  cmd/persona/           A2A サーバ。1プロセス=1ペルソナ（PERSONA_SLUG で指定）
  cmd/seed/               dev 用フィクスチャ投入（MinIO + kaigi_persona/kaigi_knowledge にサンプル2体+文書3件）
  internal/api/           moderator のルーティング、ハンドラ、SSE、CORS
  internal/agentapi/      discovery のルーティング、ハンドラ
  internal/config/        環境変数ベースの設定、DB DSN 組み立て
  internal/db/            pgxpool + 埋め込みマイグレータ（4セット: meeting/registry/persona/knowledge）
  internal/meeting/       会議ドメイン、永続化、司会のラウンド進行ロジック、A2Aダイアラ
  internal/registry/      エージェント登録のドメイン・永続化・（ペルソナ側の）自己登録クライアント
  internal/persona/       ペルソナのドメイン型と永続化
  internal/retrieval/     OpenAI 埋め込みクライアント、pgvector 検索、性格重み付きランキング
  internal/objectstore/   MinIO クライアント（Range GET によるコンテキスト拡張）
  internal/chat/          RAG パイプライン、システムプロンプト組み立て、OpenAI ストリーミング
  internal/a2aconv/       A2A ワイヤ型 ⇔ ドメイン型の変換、ペルソナのエージェントカード生成
  internal/personaexec/   chat.Engine を A2A の AgentExecutor に橋渡し
```

## 要件

- Node 20+（開発は 26）と npm
- Go 1.22+（開発は 1.26）— ルーティングは `net/http` のメソッド付きパターンでルータ依存なし
- Docker（postgres+pgvector / MinIO / 各サービスコンテナを動かす）
- OpenAI の API キー（<https://platform.openai.com/api-keys>）
- k8s で動かす場合: [kind](https://kind.sigs.k8s.io/) と `kubectl`

## はじめかた

起動方法は3通り。いずれも同じ環境変数（`.env`）を使う。**ホスト port 8080 を取り合うので `make dev` と `make docker-up` は併用できない。**

### A. 全部 Docker Compose（Go / Node をホストに入れなくてよい）

```sh
cp .env.example .env
# .env を開き OPENAI_API_KEY を設定する

make docker-up    # postgres/minio/discovery/persona×2/moderator/frontend をビルドして起動
make docker-seed  # サンプル文書3件とペルソナ2体（批評家/実務家）を投入（初回のみ）
```

<http://localhost:5173> を開く。frontend の Caddy が静的ファイルを配信し、`/api/*` を moderator コンテナへリバースプロキシする。ブラウザからは同一オリジンにしか見えないので CORS は経由しない。

```sh
make docker-logs  # discovery/persona/moderator/frontend のログを追う
make docker-down  # 停止
```

### B. ホストで開発（ホットリロード）

```sh
cp .env.example .env
# .env を開き OPENAI_API_KEY を設定する

make setup   # npm install + go mod download
make up      # postgres/MinIO だけを起動し、healthy になるまで待つ
make seed    # サンプル文書3件とペルソナ2体を投入
make dev     # discovery(:8081)/persona-critic(:8082)/persona-pragmatist(:8083)/
             # moderator(:8080)/Vite(:5173) を一括起動（up は自動実行される）
```

`make up` が postgres/MinIO しか起動しないのは意図的で、backend コンテナまで立ち上げると `make dev` のホスト側プロセスとポートが衝突するため。

各プロセスは `make dev-discovery` / `make dev-persona-critic` / `make dev-persona-pragmatist` / `make dev-moderator` / `make dev-frontend` で個別にも起動できる。

> **Note**: B の経路では `.env` は自動で読み込まれない（Go 側に dotenv ローダを入れていない）。direnv を使うか `set -a; source .env; set +a` などで export すること。A の経路では compose の `env_file` が読み込む。

### C. Kubernetes（kind）

```sh
cp .env.example .env
# .env を開き OPENAI_API_KEY を設定する
cp k8s/overlays/local/secret-patch.yaml.example k8s/overlays/local/secret-patch.yaml
# secret-patch.yaml を開き OPENAI_API_KEY を設定する（gitignore 済み。実キーをコミットしないこと）

make kind-up     # kind クラスタを作成し、ingress-nginx を導入
make kind-load   # backend/frontend イメージをビルドして kind ノードへロード
make k8s-up      # overlays/local を apply。postgres/minio が ready になるまで待つ
make k8s-seed    # サンプル文書3件とペルソナ2体を投入（初回のみ。Job は再実行時に自動で作り直す）
```

<http://kaigi.localtest.me> を開く（`localtest.me` は 127.0.0.1 を指すワイルドカード DNS なので `/etc/hosts` の追加は不要）。

```sh
make k8s-logs   # discovery/persona/frontend のログを追う
make k8s-down   # kaigi namespace を削除（クラスタ自体は残す）
make kind-down  # kind クラスタごと削除
```

**ペルソナを追加する場合**: `k8s/base/personas/persona-critic.yaml` をコピーし、`name`/`selector`/`PERSONA_SLUG`/`A2A_PUBLIC_URL`/Service 名の4箇所を変更、`k8s/base/kustomization.yaml` の `resources` に追加、`backend/cmd/seed/main.go` の `seedPersonas` に対応する `CreateInput`（同じ `Slug`）を追加する。docker-compose 側も同様（`persona-critic` サービスをコピー）。

## API（moderator）

| Method | Path                                | 説明 |
| ------ | ----------------------------------- | --- |
| GET    | `/api/health`                       | `{"status":"ok\|degraded","db":"ok"}` |
| GET    | `/api/personas`                     | discovery 由来のエージェント一覧（`present` 付き、`profile` に人格詳細） |
| POST   | `/api/meetings`                     | 会議作成。`{"topic": string, "personaSlugs": string[]}`。不在のペルソナを含むと 400 |
| GET    | `/api/meetings/{id}`                | 会議・参加者・全発言（引用含む）の取得 |
| POST   | `/api/meetings/{id}/turns`          | 発言を送信。応答は SSE |

フロントエンドは `/`（会議画面）と `/personas`（登録ペルソナ一覧。在席状況・スタンス・懐疑度・冗長度・関心トピックの重みを表示。`profile` が `null` のペルソナ pod は「詳細未登録」と表示する）の2画面。

`POST .../turns` のレスポンスは `text/event-stream`。フレームは `event: <type>\ndata: <json>\n\n`。`type` は `speaker_start` → `sources` → `token`*（話者ごとに繰り返し）→ `speaker_end`（または `speaker_error`）→ `round_end`（ラウンドごと）→ `done`。1発言の失敗は会議全体を止めず `speaker_error` として扱う。

discovery pod（`GET /registry/agents`、`POST /registry/agents`）とペルソナ pod（A2A JSON-RPC、`/.well-known/agent-card.json`）にも直接アクセスできる。ペルソナへの手動デバッグ例:

```sh
curl localhost:8082/.well-known/agent-card.json | jq .
curl localhost:8082 -X POST -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"SendMessage",
  "params":{"message":{"role":"ROLE_USER","parts":[{"text":"合意形成について"}]}}}' | jq .
```

## その他のコマンド

```sh
make test          # go test ./...（-race は手動で: cd backend && go test ./... -race）
make lint          # oxlint + go vet
make build          # frontend/dist + backend/bin 以下に4バイナリ
make down            # docker compose を停止
make logs             # docker compose のログを追う
make clean             # ビルド成果物を削除

make docker-build  # 全サービスのイメージをビルド
make docker-up     # 全部コンテナで起動
make docker-seed   # seeder イメージでフィクスチャ投入
make docker-logs   # backend各サービス/frontend のログを追う
make docker-down   # 全部停止

make kind-up / kind-load / kind-down   # kind クラスタの作成・イメージロード・削除
make k8s-up / k8s-seed / k8s-logs / k8s-down  # k8s デプロイ・投入・ログ・namespace削除
```

### 統合テスト

`retrieval` / `db` / `objectstore` / `persona` / `meeting` / `registry` パッケージには実インフラに対する統合テストがある。対応する `TEST_*_DATABASE_URL` が未設定なら自動的にスキップされる。

```sh
export TEST_KNOWLEDGE_DATABASE_URL="postgres://postgres:kaigi@localhost:5432/kaigi_knowledge?sslmode=disable"
export TEST_MEETING_DATABASE_URL="postgres://postgres:kaigi@localhost:5432/kaigi_meeting?sslmode=disable"
# ... persona / registry も同様
cd backend && go test ./... -v
```

**`TestModeratorRounds`/`TestModeratorSequential`（`internal/meeting`）が本機能の受け入れ条件** — 2体目の参加者が受け取る会議録に1体目の直前の発言が含まれること、参加者ループが逐次であることを検証する。

`retrieval.TestSearchAgainstPostgres` は性格重み付けの受け入れ条件 — 同一クエリに対して「批評家」（懐疑心が高い）と「実務家」（懐疑心が低い）で異なる資料が返ることを検証する。`make seed` 実行後でないと通らない。

## 設定

`.env.example` を `.env` にコピーする。値は環境変数から読まれ、ファイル自体は参照用。全変数とその用途は `.env.example` 自身のコメントを参照。k8s の場合は `k8s/base/config.yaml`（ConfigMap）と `k8s/overlays/local/secret-patch.yaml`（Secret、要作成）に相当する。

`make docker-up` で起動した場合、コンテナ内から見た接続先が変わるため `POSTGRES_BASE_URL`・`MINIO_ENDPOINT`・`REGISTRY_URL`・`A2A_PUBLIC_URL` は `docker-compose.yml` 側で上書きされる（`.env` の値より優先される）。k8s では `k8s/base/config.yaml` と各 Deployment の `env` がこれに相当する。

## スコープ外

- 知識の取り込み API（アップロード → 抽出 → チャンク → 埋め込み投入の HTTP エンドポイント）。`make seed` の dev スクリプトのみ
- 認証・認可・マルチテナント
- ペルソナ・会議の削除
- PDF/docx からのテキスト抽出（seed は `.md` のみ扱う）
- ハイブリッド検索、会議の自動要約・圧縮
- ペルソナ pod 同士の直接通信（A2A のメッシュ型トポロジ）。現在はスター型（司会経由）
- A2A の push notification、署名付きエージェントカード
- ペルソナ pod の HPA・オートスケール、Postgres/MinIO の HA
- 本番向け TLS・cert-manager
