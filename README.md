# kaigi

ペルソナ（知識 + 性格）と会話する API。Vite + React (TypeScript) フロントエンドと Go バックエンド。

知識は pgvector によるベクトル検索と MinIO 上の原本ファイルの二段構えで取得し、性格はクエリ書き換え・リランキング・システムプロンプトの3段階で回答に作用する。応答は SSE でストリーミングし、会話は Postgres に永続化する。

## アーキテクチャ

```
                       ┌─────────────────────────────────────────┐
  POST /api/           │           chat.Engine                    │
  conversations/{id}/  │                                          │
  messages  ────────▶  │ 1. 履歴ロード       ────▶ Postgres       │
       (SSE)           │ 2. クエリ書き換え   ──▶ OpenAI(JSON出力)  │
       ◀────token───   │ 3. ベクトル検索     ────▶ pgvector        │
       ◀────token───   │ 4. 性格重み付け     ──── (コサイン類似度   │
       ◀────sources──  │    + 性格の関心スコア      + affinity)    │
       ◀────done─────  │ 5. コンテキスト拡張 ────▶ MinIO(Range GET) │
                       │ 6. 生成            ──▶ OpenAI(streaming) │
                       │ 7. 保存            ────▶ Postgres        │
                       └─────────────────────────────────────────┘
```

### 性格が情報処理に作用する3点

| 段階 | 性格のどの要素が効くか | 実装 |
|---|---|---|
| **① クエリ書き換え** | `Stance` + `Interests` | ペルソナが実際にどう検索するかを OpenAI に JSON 出力で考えさせ、最大3本のクエリに展開する |
| **② 性格重み付け** | `Interests`（重み付き） + `Skepticism` | `Final = (1-λ)·Relevance + λ·Affinity`。`Relevance` は pgvector のコサイン類似度、`Affinity` は関心トピック埋め込みとチャンクの cosine 加重和。`Skepticism` が高いほど、関連度の足切り閾値が上がる（採用する資料そのものが変わる） |
| **③ 生成** | `Stance` + `Verbosity` + `Skepticism` | システムプロンプトを組み立てる。ペルソナごとに内容が固定なので、OpenAI の自動コンテキストキャッシュが効く |

`PERSONA_INFLUENCE`（λ）は環境変数。`0` で純粋な RAG に退化するので、性格の効きを A/B できる。

## レイアウト

```
docker-compose.yml     postgres+pgvector / MinIO / backend / frontend / seed
frontend/               Vite + React + TypeScript
  Dockerfile            node でビルド → Caddy で配信するマルチステージ
  Caddyfile             SPA フォールバック + /api を backend へリバースプロキシ
  src/api/client.ts     ペルソナ/会話 API の型付きクライアント、SSE ストリーム
  src/App.tsx           チャット UI
backend/
  Dockerfile            server と seed の2バイナリを distroless に載せる
  cmd/server/           main: 依存の組み立て、HTTP サーバ、graceful shutdown
  cmd/seed/              dev 用フィクスチャ投入（MinIO + pgvector + サンプルペルソナ2体）
  internal/api/          ルーティング、ハンドラ、SSE、CORS
  internal/config/       環境変数ベースの設定
  internal/db/           pgxpool + 埋め込みマイグレータ
  internal/persona/      ペルソナのドメイン型と永続化
  internal/retrieval/    OpenAI 埋め込みクライアント、pgvector 検索、性格重み付きランキング
  internal/objectstore/  MinIO クライアント（Range GET によるコンテキスト拡張）
  internal/chat/         RAG オーケストレーション、システムプロンプト組み立て、OpenAI ストリーミング
```

## 要件

- Node 20+（開発は 26）と npm
- Go 1.22+（開発は 1.26）— ルーティングは `net/http` のメソッド付きパターンでルータ依存なし
- Docker（postgres+pgvector / MinIO を動かす）
- OpenAI の API キー（<https://platform.openai.com/api-keys>）

## はじめかた

起動方法は2通りある。どちらも <http://localhost:5173> を開いて使う。**ホスト port 8080 を取り合うので併用はできない。**

### A. 全部 Docker（Go / Node をホストに入れなくてよい）

```sh
cp .env.example .env
# .env を開き OPENAI_API_KEY を設定する

make docker-up    # postgres/MinIO/backend/frontend をビルドして起動
make docker-seed  # サンプル文書3件とペルソナ2体（批評家/実務家）を投入（初回のみ）
```

frontend は Caddy が静的ファイルを配信し、`/api/*` を backend コンテナへリバースプロキシする。ブラウザからは同一オリジンにしか見えないので CORS は経由しない。

```sh
make docker-logs  # backend/frontend のログを追う
make docker-down  # 停止
```

### B. ホストで開発（ホットリロード）

```sh
cp .env.example .env
# .env を開き OPENAI_API_KEY を設定する

make setup   # npm install + go mod download
make up      # postgres/MinIO だけを起動し、healthy になるまで待つ
make seed    # サンプル文書3件とペルソナ2体を投入
make dev     # Go API を :8080、Vite を :5173 で起動（up は自動実行される）
```

`make up` が postgres/MinIO しか起動しないのは意図的で、backend コンテナまで立ち上げると `make dev` のホスト側 Go プロセスと port 8080 が衝突するため。

バックエンド/フロントエンドは `make dev-backend` / `make dev-frontend` で個別にも起動できる。

> **Note**: B の経路では `.env` は自動で読み込まれない（Go 側に dotenv ローダを入れていない）。direnv を使うか `set -a; source .env; set +a` などで export すること。A の経路では compose の `env_file` が読み込む。

## API

| Method | Path                                | 説明 |
| ------ | ----------------------------------- | --- |
| GET    | `/api/health`                       | `{"status":"ok\|degraded","db":"ok"}` |
| GET    | `/api/personas`                     | ペルソナ一覧 |
| POST   | `/api/personas`                     | ペルソナ作成（関心トピックは作成時に埋め込まれる） |
| GET    | `/api/personas/{id}`                | ペルソナ取得 |
| PATCH  | `/api/personas/{id}`                | `stance` / `verbosity` / `skepticism` を更新 |
| POST   | `/api/conversations`                | 会話作成（`personaId` 必須） |
| GET    | `/api/conversations/{id}`           | 会話と履歴の取得 |
| POST   | `/api/conversations/{id}/messages`  | メッセージ送信。応答は SSE（`sources` → `token`* → `done`） |
| GET    | `/api/documents/{id}`               | 原本ファイルへの presigned URL にリダイレクト |

`POST .../messages` のレスポンスは `text/event-stream`。フレームは `event: <type>\ndata: <json>\n\n`。`type` は `sources` / `token` / `done` / `error`。

## その他のコマンド

```sh
make test          # go test ./...（-race は手動で: cd backend && go test ./... -race）
make lint          # oxlint + go vet
make build         # frontend/dist + backend/bin/server
make down          # docker compose を停止
make logs          # docker compose のログを追う
make clean         # ビルド成果物を削除

make docker-build  # backend/frontend のイメージをビルド
make docker-up     # 全部コンテナで起動
make docker-seed   # seeder イメージでフィクスチャ投入
make docker-logs   # backend/frontend のログを追う
make docker-down   # 全部停止
```

### 統合テスト

`retrieval` / `db` / `objectstore` パッケージには実インフラに対する統合テストがある。`TEST_DATABASE_URL`（無ければ `DATABASE_URL`）などの環境変数が未設定なら自動的にスキップされる。

```sh
export DATABASE_URL="postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable"
cd backend && go test ./internal/retrieval/... -run TestSearchAgainstPostgres -v
```

`TestSearchAgainstPostgres` は本機能の受け入れ条件そのもの — 同一クエリに対して「批評家」（懐疑心が高い）と「実務家」（懐疑心が低い）で異なる資料が返ることを検証する。`make seed` 実行後でないと通らない。

## 設定

`.env.example` を `.env` にコピーする。値は環境変数から読まれ、ファイル自体は参照用。

| 変数 | デフォルト | 用途 |
| --- | --- | --- |
| `ADDR` | `:8080` | backend |
| `ALLOWED_ORIGINS` | `http://localhost:5173` | backend（CORS） |
| `DATABASE_URL` | `postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable` | backend, seed |
| `MINIO_ENDPOINT` | `localhost:9000` | backend, seed（バックエンドが接続する先） |
| `MINIO_PUBLIC_ENDPOINT` | `localhost:9000` | backend（presigned URL の生成先。ブラウザから引ける名前にする） |
| `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | `minioadmin` | backend, seed |
| `MINIO_BUCKET` | `kaigi-knowledge` | backend, seed |
| `MINIO_USE_SSL` | `false` | backend |
| `MINIO_REGION` | `us-east-1` | backend, seed（presign 時のリージョン。未指定だとネットワーク越しの照会が走る） |
| `OPENAI_API_KEY` | （必須、既定なし） | backend, seed（chat + embedding 共用）。未設定だと起動時に失敗する |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | backend, seed |
| `CHAT_MODEL` | `gpt-5-mini` | backend |
| `EMBED_MODEL` | `text-embedding-3-small` | backend, seed。`vector(1536)` と幅が一致する text-embedding-3 系であること |
| `CANDIDATE_K` | `40` | backend（pgvector から取る候補数） |
| `FINAL_N` | `8` | backend（性格重み付け後にプロンプトへ載せる件数） |
| `PERSONA_INFLUENCE` | `0.25` | backend（性格の効き具合 λ、0..1） |
| `CONTEXT_PAD_BYTES` | `1200` | backend（MinIO Range GET でチャンク前後に読み足すバイト数） |
| `RELEVANCE_FLOOR` | `0.25` | backend（引用に必要な最低コサイン類似度） |
| `RELEVANCE_SKEPTICISM_SPAN` | `0.20` | backend（`Skepticism` に応じて閾値が上乗せされる幅） |
| `VITE_API_PROXY_TARGET` | `http://localhost:8080` | frontend |

開発時は Vite のプロキシ、Docker 起動時は Caddy のリバースプロキシにより、いずれもブラウザからは単一オリジンにしか見えないため CORS は経由しない。`ALLOWED_ORIGINS` はフロントエンドを別ホストから配信する場合に効く。

`make docker-up` で起動した場合、コンテナ内から見た接続先が変わるため以下は `docker-compose.yml` 側で上書きされる（`.env` の値より優先される）。

| 変数 | コンテナでの値 | 理由 |
| --- | --- | --- |
| `DATABASE_URL` | `...@postgres:5432/...` | compose ネットワーク内のサービス名で解決する |
| `MINIO_ENDPOINT` | `minio:9000` | backend → MinIO はサービス名で接続する |
| `MINIO_PUBLIC_ENDPOINT` | `localhost:9000` | presigned URL を開くのはブラウザなので、公開ポートを指す必要がある |

### 既存環境からの移行

埋め込みモデルが変わったのでベクトルの次元も空間も変わる。マイグレーション `0002` が
`chunks` と `persona_interests` を空にするので、再 seed が必要。

```sh
make down
docker volume rm kaigi_hf-cache   # TEI のモデルキャッシュ(~2.5GB)を解放
make up
cd backend && go run ./cmd/server   # マイグレーション 0002 を適用
make seed                            # 1536 次元で作り直す
```

既存の会話の本文は残るが、引用（`message_citations`）は `chunks` の削除に連動して消える。

## スコープ外

- 知識の取り込み API（アップロード → 抽出 → チャンク → 埋め込み投入の HTTP エンドポイント）。`make seed` の dev スクリプトのみ
- 認証・認可・マルチテナント
- ペルソナ・会話の削除
- PDF/docx からのテキスト抽出（seed は `.md` のみ扱う）
- ハイブリッド検索、会話の自動要約・圧縮
