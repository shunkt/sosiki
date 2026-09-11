# kaigi

ペルソナ（知識 + 性格）と会話する API。Vite + React (TypeScript) フロントエンドと Go バックエンド。

知識は pgvector によるベクトル検索と MinIO 上の原本ファイルの二段構えで取得し、性格はクエリ書き換え・リランキング・システムプロンプトの3段階で回答に作用する。応答は SSE でストリーミングし、会話は Postgres に永続化する。

## アーキテクチャ

```
                       ┌─────────────────────────────────────────┐
  POST /api/           │           chat.Engine                    │
  conversations/{id}/  │                                          │
  messages  ────────▶  │ 1. 履歴ロード       ────▶ Postgres       │
       (SSE)           │ 2. クエリ書き換え   ──▶ DeepSeek(JSON出力) │
       ◀────token───   │ 3. ベクトル検索     ────▶ pgvector        │
       ◀────token───   │ 4. リランキング     ────▶ TEI /rerank     │
       ◀────sources──  │    + 性格の関心スコア                     │
       ◀────done─────  │ 5. コンテキスト拡張 ────▶ MinIO(Range GET) │
                       │ 6. 生成            ──▶ DeepSeek(streaming)│
                       │ 7. 保存            ────▶ Postgres        │
                       └─────────────────────────────────────────┘
```

### 性格が情報処理に作用する3点

| 段階 | 性格のどの要素が効くか | 実装 |
|---|---|---|
| **① クエリ書き換え** | `Stance` + `Interests` | ペルソナが実際にどう検索するかを DeepSeek に JSON 出力で考えさせ、最大3本のクエリに展開する |
| **② リランキング** | `Interests`（重み付き） + `Skepticism` | `Final = (1-λ)·Relevance + λ·Affinity`。`Affinity` は関心トピック埋め込みとチャンクの cosine 加重和。`Skepticism` が高いほど、リランク関連度の足切り閾値が上がる（採用する資料そのものが変わる） |
| **③ 生成** | `Stance` + `Verbosity` + `Skepticism` | システムプロンプトを組み立てる。ペルソナごとに内容が固定なので、DeepSeek の自動コンテキストキャッシュが効く |

`PERSONA_INFLUENCE`（λ）は環境変数。`0` で純粋な RAG に退化するので、性格の効きを A/B できる。

## レイアウト

```
docker-compose.yml     postgres+pgvector / MinIO / TEI(embed) / TEI(rerank)
frontend/               Vite + React + TypeScript
  src/api/client.ts     ペルソナ/会話 API の型付きクライアント、SSE ストリーム
  src/App.tsx           チャット UI
backend/
  cmd/server/           main: 依存の組み立て、HTTP サーバ、graceful shutdown
  cmd/seed/              dev 用フィクスチャ投入（MinIO + pgvector + サンプルペルソナ2体）
  internal/api/          ルーティング、ハンドラ、SSE、CORS
  internal/config/       環境変数ベースの設定
  internal/db/           pgxpool + 埋め込みマイグレータ
  internal/persona/      ペルソナのドメイン型と永続化
  internal/retrieval/    TEI クライアント、pgvector 検索、性格重み付きリランキング
  internal/objectstore/  MinIO クライアント（Range GET によるコンテキスト拡張）
  internal/chat/         RAG オーケストレーション、システムプロンプト組み立て、DeepSeek ストリーミング
```

## 要件

- Node 20+（開発は 26）と npm
- Go 1.22+（開発は 1.26）— ルーティングは `net/http` のメソッド付きパターンでルータ依存なし
- Docker（postgres+pgvector / MinIO / TEI を動かす）
- DeepSeek の API キー（<https://platform.deepseek.com/>）

## はじめかた

```sh
cp .env.example .env
# .env を開き DEEPSEEK_API_KEY を設定する

make setup   # npm install + go mod download
make up      # docker compose up -d、依存が healthy になるまで待つ
             # 初回は ruri-v3 の埋め込み/リランカーモデル(計 ~2.5GB)を
             # ダウンロードするため数分かかる
make seed    # サンプル文書3件とペルソナ2体（批評家/実務家）を投入
make dev     # Go API を :8080、Vite を :5173 で起動（up は自動実行される）
```

<http://localhost:5173> を開く。ペルソナを選び、メッセージを送るとストリーミングで応答が返る。

バックエンド/フロントエンドは `make dev-backend` / `make dev-frontend` で個別にも起動できる。

## API

| Method | Path                                | 説明 |
| ------ | ----------------------------------- | --- |
| GET    | `/api/health`                       | `{"status":"ok\|degraded","embed":"ok","rerank":"ok"}` |
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
make test    # go test ./...（-race は手動で: cd backend && go test ./... -race）
make lint    # oxlint + go vet
make build   # frontend/dist + backend/bin/server
make down    # docker compose を停止
make logs    # docker compose のログを追う
make clean   # ビルド成果物を削除
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
| `EMBED_URL` | `http://localhost:8081` | backend, seed（TEI 埋め込み） |
| `RERANK_URL` | `http://localhost:8082` | backend（TEI リランカー） |
| `DEEPSEEK_API_KEY` | （必須、既定なし） | backend。未設定だと起動時に失敗する |
| `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | backend |
| `CHAT_MODEL` | `deepseek-flash` | backend |
| `CANDIDATE_K` | `40` | backend（pgvector から取る候補数） |
| `FINAL_N` | `8` | backend（リランク後にプロンプトへ載せる件数） |
| `PERSONA_INFLUENCE` | `0.25` | backend（性格の効き具合 λ、0..1） |
| `CONTEXT_PAD_BYTES` | `1200` | backend（MinIO Range GET でチャンク前後に読み足すバイト数） |
| `VITE_API_PROXY_TARGET` | `http://localhost:8080` | frontend |

開発時は Vite のプロキシによりブラウザからは単一オリジンにしか見えないため CORS は経由しない。`ALLOWED_ORIGINS` はフロントエンドを別ホストから配信する場合に効く。

## スコープ外

- 知識の取り込み API（アップロード → 抽出 → チャンク → 埋め込み投入の HTTP エンドポイント）。`make seed` の dev スクリプトのみ
- 認証・認可・マルチテナント
- ペルソナ・会話の削除
- PDF/docx からのテキスト抽出（seed は `.md` のみ扱う）
- ハイブリッド検索、会話の自動要約・圧縮
