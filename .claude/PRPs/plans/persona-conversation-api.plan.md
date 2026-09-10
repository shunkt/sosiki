# Plan: ペルソナ会話 API（RAG + 性格駆動リランキング）

## Summary

ペルソナ（= 知識 + 性格）と会話する Go バックエンド API を、既存の kaigi スキャフォールド上に構築する。知識は pgvector（チャンクのベクトル検索）と MinIO（原本ファイル）の二段構えで取得し、性格は「クエリ書き換え → リランキング → システムプロンプト」の3段階で情報処理の方向性を決める。応答は SSE ストリーミングで返し、会話は Postgres に永続化する。

## User Story

As a フロントエンド利用者,
I want 固有の知識と性格を持つペルソナと対話し、その回答が何を根拠にしたか辿れる,
So that 同じ知識ベースでも「誰が答えたか」で切り口が変わる会話体験を得られる。

## Problem → Solution

**現状**: `/api/health` と `/api/hello` だけのスキャフォールド。DB もオブジェクトストレージも LLM 接続もない。
**目標**: ペルソナ CRUD + 会話 API。ユーザ発話 → 性格でクエリ書き換え → pgvector 検索 → 性格重み付きリランキング → MinIO から原文コンテキスト拡張 → DeepSeek でストリーミング生成 → 引用付きで永続化。

## Metadata

- **Complexity**: Large（新規 14 ファイル / 更新 6 ファイル / 4 つの外部サービス）
- **Source PRD**: N/A（フリーフォーム指示）
- **PRD Phase**: standalone
- **Estimated Files**: 20（新規 14、更新 6）

---

## 確定した決定事項（ユーザー回答）

| 論点 | 決定 |
|---|---|
| チャット LLM | `deepseek-flash`（OpenAI 互換 API。旧名 `deepseek-v4-flash` はリタイア済み） |
| 埋め込みモデル | `cl-nagoya/ruri-v3-310m`（日本語特化） |
| 性格の作用範囲 | システムプロンプト + クエリ書き換え + **リランキングまで** |
| ingestion | **今回はスコープ外**。検索＋会話のみ。投入は dev 用 seed スクリプト |
| レスポンス | **SSE ストリーミング**、会話履歴は **Postgres に永続化** |

## 明示した前提（ユーザー未指定 → こちらで決定）

1. **Go クライアントは OpenAI 互換ライブラリ**を `base_url: https://api.deepseek.com` に向けて使う（DeepSeek 公式 Go SDK は存在しない）。**Anthropic SDK は使わない**。クエリ書き換えも本文生成も同一モデル `deepseek-flash`。ライブラリは `github.com/sashabaranov/go-openai` を想定（Task 10 で最終確認）。
2. **推論サーバは HuggingFace TEI**（`text-embeddings-inference`）を docker で 2 コンテナ。Go から PyTorch は呼べないため、埋め込み/リランカーは HTTP 越しに叩く。Python コードは 1 行も書かない。
3. **ペルソナ CRUD は最小構成**（作成・取得・一覧・更新）。削除・認証・マルチテナントは対象外。

---

## UX Design

### Before

```
┌──────────────────────────────────────┐
│  kaigi                               │
│  backend: ok                         │
│                                      │
│  [ name           ] [Call /api/hello]│
│  hello, kaigi                        │
└──────────────────────────────────────┘
```

### After

```
┌──────────────────────────────────────────────────────┐
│  kaigi                          ペルソナ: [批評家 ▾]  │
├──────────────────────────────────────────────────────┤
│  user  ▸ 分散システムの合意形成について              │
│                                                      │
│  批評家 ▸ Raft の理解しやすさという主張には▊         │
│           ← トークンが逐次流れる (SSE)               │
│                                                      │
│  根拠 (3):                                            │
│   [1] raft-paper.md    (関連 0.94 / 関心 +0.31)      │
│   [2] paxos-made-simple.md (0.88 / -0.10)            │
│   [3] 議事録_2026-03.md (0.71 / +0.44)               │
├──────────────────────────────────────────────────────┤
│  [ メッセージを入力…                    ] [送信]     │
└──────────────────────────────────────────────────────┘
```

### Interaction Changes

| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 入力欄 | name（単語） | 自由発話 | Enter 送信 / Shift+Enter 改行 |
| 応答 | 一括 JSON | SSE トークンストリーム | 初トークンまで ~1-3s、以降逐次 |
| 根拠表示 | なし | `sources` イベントで引用リスト | 関連スコアと**関心スコアを分けて**表示。性格の効きが見える |
| ペルソナ選択 | なし | ドロップダウン | 選択で新規 conversation を作成 |
| リロード | 状態消失 | 会話 ID で復元 | `GET /api/conversations/{id}` |

---

## Mandatory Reading

実装前に必ず読むこと。

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `backend/internal/api/middleware.go` | 32-55 | `statusRecorder` が SSE を壊す。Task 2 で必ず直す |
| P0 | `backend/cmd/server/main.go` | 28-35 | `WriteTimeout: 30s` が SSE を 30 秒で切断する |
| P0 | `backend/internal/api/api.go` | 1-40 | ルーティング・`writeJSON`・パッケージコメントの型 |
| P0 | `backend/internal/config/config.go` | 1-40 | 環境変数の読み方。全新規設定はここに足す |
| P1 | `backend/internal/api/api_test.go` | 1-80 | テーブル駆動テストと `httptest` の書き方 |
| P1 | `frontend/src/api/client.ts` | 1-15 | フロントの fetch ラッパの型付け規約 |
| P2 | `backend/internal/api/middleware.go` | 10-30 | CORS。SSE でも `Vary: Origin` は維持 |
| P2 | `README.md` | 全体 | 追記が必要（環境変数表・API 表） |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| ruri-v3-310m | huggingface.co/cl-nagoya/ruri-v3-310m | **768次元** / max 8192 token / **cosine** / Apache-2.0 / `ModernBertModel` / mean pooling |
| ruri プレフィックス | 同上 | クエリ=`検索クエリ: ` / 文書=`検索文書: ` / トピック=`トピック: ` / 意味のみ=`""`。**付け忘れると精度が静かに落ちる** |
| ruri-v3-reranker-310m | huggingface.co/cl-nagoya/ruri-v3-reranker-310m | CrossEncoder / **プレフィックス不要** / ModernBERT-Ja / スコアは 0..1 近傍 |
| TEI | github.com/huggingface/text-embeddings-inference | ModernBERT 対応済み・CPU 対応。`POST /embed`（`{inputs,normalize}`）、`POST /rerank`（`{query,texts}`）。**1プロセス1モデル**なので埋め込み用とリランク用で2コンテナ |
| DeepSeek API | api-docs.deepseek.com | **OpenAI 互換**。`base_url: https://api.deepseek.com`、`POST /chat/completions`、`stream: true` でストリーミング |
| モデル ID | 同上 | **`deepseek-flash`**。当初指定された `deepseek-v4-flash` は**リタイア済みの旧名**（V4.1-Flash にルーティングされる）。新規コードでは使わない |
| DeepSeek 諸元 | 同上 | 1M コンテキスト / 最大出力 384K。安価（キャッシュヒット時 $0.014/M 入力、出力 $1.32/M） |
| JSON Output | api-docs.deepseek.com の API Guides → JSON Output | ⚠️ **未検証**。パラメータ名と json_schema 対応可否は Task 9 の実装時に必ず確認する |
| Context Caching | api-docs.deepseek.com の API Guides → Context Caching | ⚠️ **未検証**だが**自動**キャッシュ（`cache_control` 相当の明示指定は不要）。プロンプト先頭を固定すればヒットする前提で設計 |
| pgvector | github.com/pgvector/pgvector-go | `pgvector.NewVector([]float32)` を pgx にそのまま渡せる。`vector_cosine_ops` + HNSW |

---

## Patterns to Mirror

以下は**実際のこのリポジトリのコード**。新規コードはこれと見分けがつかないように書く。

### NAMING_CONVENTION
```go
// SOURCE: backend/internal/api/api.go:12-19
// NewHandler builds the fully wrapped HTTP handler for the service.
func NewHandler(cfg config.Config, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/hello", handleHello)

	return requestLogger(log, cors(cfg.AllowedOrigins, mux))
}
```
→ ハンドラは `handleXxx`、コンストラクタは `NewXxx`。ルートは `net/http` のメソッド付きパターン。ルータライブラリは**入れない**。

### ERROR_HANDLING
```go
// SOURCE: backend/internal/api/api.go:33-40
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so logging is all that is left.
		slog.Error("encode response", "error", err)
	}
}
```
→ エラーは握りつぶさずログに出す。コメントは「なぜ」だけ書く（「何をしているか」は書かない）。

### CONFIG_PATTERN
```go
// SOURCE: backend/internal/config/config.go:17-29
func Load() Config {
	return Config{
		Addr:           env("ADDR", ":8080"),
		AllowedOrigins: splitAndTrim(env("ALLOWED_ORIGINS", "http://localhost:5173")),
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
```
→ 設定は**全て環境変数**。デフォルト値を必ず持たせる。設定構造体にフィールドコメントを付ける。

### MIDDLEWARE_PATTERN
```go
// SOURCE: backend/internal/api/middleware.go:43-54
func requestLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start),
		)
	})
}
```
→ ミドルウェアは `func(next http.Handler) http.Handler`。ログは `log/slog` のキーバリュー形式。

### LIFECYCLE_PATTERN
```go
// SOURCE: backend/cmd/server/main.go:37-58
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// ...
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
```
→ 起動は `run(log) error` に集約し `main` は終了コードだけ見る。graceful shutdown を維持。

### TEST_STRUCTURE
```go
// SOURCE: backend/internal/api/api_test.go:40-64
func TestCORSOnlyEchoesAllowedOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"allowed", "http://localhost:5173", "http://localhost:5173"},
		{"denied", "http://evil.example", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()
			newTestHandler().ServeHTTP(rec, req)

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.want {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.want)
			}
		})
	}
}
```
→ テーブル駆動 + `t.Run`。`httptest` のみ。**アサーションライブラリを入れない**。`t.Errorf` は `got, want` の順。

### FRONTEND_CLIENT_PATTERN
```ts
// SOURCE: frontend/src/api/client.ts:4-12
async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`${path} responded ${res.status} ${res.statusText}`)
  }
  return (await res.json()) as T
}
```
→ 型を先に `export type` で定義し、関数は薄く。`AbortSignal` を必ず受ける。

---

## Architecture

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

### 性格が情報処理に作用する3点（これが本機能の核）

| 段階 | 性格のどの要素が効くか | 実装 |
|---|---|---|
| **① クエリ書き換え** | `Stance` + `Interests` | DeepSeek に「このペルソナなら何を調べるか」を JSON 出力で 1-3 本のクエリに展開させる |
| **② リランキング** | `Interests`（重み付き） + `Skepticism` | `final = (1-λ)·relevance + λ·affinity`。`affinity` は関心トピック埋め込みとチャンクの cosine 加重和。`Skepticism` が高いほど relevance の足切り閾値が上がる |
| **③ 生成** | `Stance` + `Verbosity` + `Skepticism` | システムプロンプトを組み立て。先頭固定なので DeepSeek の自動キャッシュが効く |

**λ（persona_influence）** は環境変数（既定 0.25）。0 にすると純粋な RAG に退化するので、性格の効きを A/B するツマミになる。

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `docker-compose.yml` | CREATE | postgres+pgvector / minio / minio-init / tei-embed / tei-rerank |
| `backend/internal/db/migrations/0001_init.sql` | CREATE | スキーマ + HNSW インデックス |
| `backend/internal/db/db.go` | CREATE | pgxpool + 埋め込みマイグレータ |
| `backend/internal/config/config.go` | UPDATE | DB/MinIO/TEI/DeepSeek の設定を追加 |
| `backend/internal/persona/persona.go` | CREATE | ドメイン型 `Persona` / `Personality` / `Interest` |
| `backend/internal/persona/store.go` | CREATE | ペルソナの永続化（関心の埋め込み込み） |
| `backend/internal/retrieval/embed.go` | CREATE | TEI 埋め込みクライアント（プレフィックス責務を集約） |
| `backend/internal/retrieval/rerank.go` | CREATE | TEI リランククライアント |
| `backend/internal/retrieval/search.go` | CREATE | pgvector 検索 + 性格リランキング |
| `backend/internal/objectstore/store.go` | CREATE | MinIO Range GET によるコンテキスト拡張 |
| `backend/internal/chat/store.go` | CREATE | conversation / message / citation の永続化 |
| `backend/internal/chat/prompt.go` | CREATE | 性格 → システムプロンプト組み立て |
| `backend/internal/chat/engine.go` | CREATE | RAG オーケストレーション + DeepSeek ストリーミング |
| `backend/internal/api/persona.go` | CREATE | ペルソナ CRUD ハンドラ |
| `backend/internal/api/chat.go` | CREATE | SSE ハンドラ |
| `backend/internal/api/api.go` | UPDATE | ルート追加・依存注入 |
| `backend/internal/api/middleware.go` | UPDATE | **`statusRecorder.Unwrap()` 追加（SSE 必須）** |
| `backend/cmd/server/main.go` | UPDATE | 依存の組み立て・**`WriteTimeout: 0`** |
| `backend/cmd/seed/main.go` | CREATE | dev 用フィクスチャ投入（MinIO + pgvector） |
| `frontend/src/api/client.ts` | UPDATE | SSE クライアント + 型 |
| `frontend/src/App.tsx` | UPDATE | チャット UI |
| `Makefile` / `.env.example` / `README.md` | UPDATE | up/seed ターゲット・環境変数・API 表 |

## NOT Building

明示的にスコープ外。実装中にこれらへ手を広げないこと。

- **ingestion API**（アップロード → 抽出 → チャンク → 埋め込み → 投入の HTTP エンドポイント）。dev seed スクリプトのみ。
- 認証・認可・マルチテナント・レート制限
- ペルソナの削除、会話の削除・アーカイブ
- PDF/docx からのテキスト抽出（seed は `.md` / `.txt` のみ扱う）
- ハイブリッド検索（BM25 併用）、HyDE、クエリ分解の多段化
- 会話コンテキストの自動要約・圧縮（compaction）
- 本番デプロイ構成、CI、可観測性（メトリクス/トレース）
- ツール使用（tool use）／エージェントループ

---

## Step-by-Step Tasks

### Task 1: docker-compose で外部依存を立てる

- **ACTION**: リポジトリルートに `docker-compose.yml` を作成。
- **IMPLEMENT**: 5 サービス。
  - `postgres`: image `pgvector/pgvector:pg17`、`POSTGRES_PASSWORD=kaigi`、`POSTGRES_DB=kaigi`、port `5432:5432`、named volume。
  - `minio`: image `minio/minio`、command `server /data --console-address ":9001"`、`MINIO_ROOT_USER=minioadmin` / `MINIO_ROOT_PASSWORD=minioadmin`、ports `9000:9000` `9001:9001`、named volume。
  - `minio-init`: image `minio/mc`、`depends_on: minio`、entrypoint で `mc alias set` → `mc mb --ignore-existing local/kaigi-knowledge`。
  - `tei-embed`: image `ghcr.io/huggingface/text-embeddings-inference:cpu-1.8`、command `--model-id cl-nagoya/ruri-v3-310m --pooling mean --auto-truncate`、port `8081:80`、HF キャッシュを named volume で `/data` にマウント。
  - `tei-rerank`: 同 image、`--model-id cl-nagoya/ruri-v3-reranker-310m --auto-truncate`、port `8082:80`、同じキャッシュ volume。
  - 全サービスに `healthcheck` を付ける（postgres は `pg_isready`、minio は `/minio/health/live`、TEI は `/health`）。
- **MIRROR**: N/A（新規ファイル）
- **GOTCHA**:
  - TEI は **1 プロセス 1 モデル**。埋め込みとリランカーを 1 コンテナに同居させることはできない。
  - 初回起動でモデルを HuggingFace からダウンロードするため 310m×2 ≒ 2.5GB、数分かかる。**volume を共有**してモデルキャッシュを使い回す。
  - `cpu-1.8` タグは実在確認が必要。`docker pull` が失敗したらその時点で現行の CPU タグに差し替える。
  - Apple Silicon では TEI CPU イメージが amd64 エミュレーションになり遅い。`platform: linux/amd64` を明示し、遅さは許容する（dev 用途）。
- **VALIDATE**:
  ```sh
  docker compose up -d && sleep 180
  docker compose ps                      # 全 service が healthy
  curl -s localhost:8081/health          # TEI embed
  curl -s localhost:8082/health          # TEI rerank
  curl -s -X POST localhost:8081/embed -H 'content-type: application/json' \
    -d '{"inputs":["検索文書: これはテストです"],"normalize":true}' | jq '.[0]|length'
  # → 768 が出ること
  ```

---

### Task 2: SSE を通すために既存ミドルウェアとサーバ設定を直す

- **ACTION**: `middleware.go` に `Unwrap()` を追加し、`main.go` の `WriteTimeout` を 0 にする。
- **IMPLEMENT**:
  ```go
  // middleware.go の statusRecorder に追加
  // Unwrap lets http.ResponseController reach the real ResponseWriter, which the
  // SSE handler needs for Flush — http.Flusher is not part of http.ResponseWriter
  // and therefore is not promoted by embedding.
  func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
  ```
  `main.go` は `WriteTimeout: 30 * time.Second` を削除し、代わりに
  ```go
  // WriteTimeout must stay unset: it is an absolute deadline on the whole
  // response, which would cut SSE streams off mid-conversation. Per-frame
  // deadlines are set with http.ResponseController in the SSE handler.
  WriteTimeout: 0,
  ```
  `ReadTimeout` も長時間接続で問題になるため `ReadHeaderTimeout: 10 * time.Second` のみ残し `ReadTimeout: 0` にする。
- **MIRROR**: LIFECYCLE_PATTERN（`main.go:28-35` の構造体リテラルの形はそのまま維持）
- **IMPORTS**: 追加なし
- **GOTCHA**: **これが最大の落とし穴**。`statusRecorder` は `http.ResponseWriter` を埋め込んでいるが、`Flush()` は `http.ResponseWriter` インターフェースのメソッドではないため**昇格しない**。`Unwrap()` が無いと `http.NewResponseController(w).Flush()` が `ErrNotSupported` を返し、SSE が一切流れずレスポンス終了時に全部まとめて届く。テストで先に落とすこと。
- **VALIDATE**:
  ```sh
  cd backend && go test ./internal/api/ -run TestStatusRecorder -v
  ```
  新規テスト `TestStatusRecorderSupportsFlush`: `httptest.NewRecorder()` を `statusRecorder` で包み、`http.NewResponseController(rec).Flush()` が `nil` を返すことを確認。

---

### Task 3: スキーマとマイグレーション

- **ACTION**: `backend/internal/db/migrations/0001_init.sql` を作成し、`backend/internal/db/db.go` に埋め込みマイグレータを書く。
- **IMPLEMENT**: SQL:
  ```sql
  CREATE EXTENSION IF NOT EXISTS vector;
  CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

  CREATE TABLE personas (
      id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      name        text NOT NULL,
      stance      text NOT NULL DEFAULT '',
      verbosity   text NOT NULL DEFAULT 'balanced'
                    CHECK (verbosity IN ('concise','balanced','detailed')),
      skepticism  real NOT NULL DEFAULT 0.5
                    CHECK (skepticism >= 0 AND skepticism <= 1),
      created_at  timestamptz NOT NULL DEFAULT now(),
      updated_at  timestamptz NOT NULL DEFAULT now()
  );

  -- 関心軸。embedding は「検索クエリ: {topic}」を埋め込んだもの（理由は Task 5）
  CREATE TABLE persona_interests (
      id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      persona_id uuid NOT NULL REFERENCES personas(id) ON DELETE CASCADE,
      topic      text NOT NULL,
      weight     real NOT NULL CHECK (weight >= -1 AND weight <= 1),
      embedding  vector(768) NOT NULL
  );
  CREATE INDEX ON persona_interests (persona_id);

  CREATE TABLE documents (
      id           uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      title        text NOT NULL,
      bucket       text NOT NULL,
      object_key   text NOT NULL,
      content_type text NOT NULL DEFAULT 'text/plain',
      size_bytes   bigint NOT NULL,
      created_at   timestamptz NOT NULL DEFAULT now(),
      UNIQUE (bucket, object_key)
  );

  -- byte_start/byte_end は原本オブジェクト内のバイト位置。MinIO の Range GET で
  -- 前後を読み足して「小さく検索して大きく渡す」を実現する。
  CREATE TABLE chunks (
      id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
      chunk_index int  NOT NULL,
      content     text NOT NULL,
      byte_start  bigint NOT NULL,
      byte_end    bigint NOT NULL,
      embedding   vector(768) NOT NULL,
      UNIQUE (document_id, chunk_index)
  );
  CREATE INDEX chunks_embedding_hnsw
      ON chunks USING hnsw (embedding vector_cosine_ops);

  CREATE TABLE conversations (
      id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      persona_id uuid NOT NULL REFERENCES personas(id),
      title      text NOT NULL DEFAULT '',
      created_at timestamptz NOT NULL DEFAULT now(),
      updated_at timestamptz NOT NULL DEFAULT now()
  );

  CREATE TABLE messages (
      id              uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
      conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
      seq             int  NOT NULL,
      role            text NOT NULL CHECK (role IN ('user','assistant')),
      content         text NOT NULL,
      created_at      timestamptz NOT NULL DEFAULT now(),
      UNIQUE (conversation_id, seq)
  );

  CREATE TABLE message_citations (
      message_id      uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
      chunk_id        uuid NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
      rank            int  NOT NULL,
      relevance_score real NOT NULL,
      affinity_score  real NOT NULL,
      PRIMARY KEY (message_id, chunk_id)
  );
  ```
  Go 側:
  ```go
  //go:embed migrations/*.sql
  var migrationFS embed.FS
  ```
  `schema_migrations(version text primary key, applied_at timestamptz)` を作り、未適用のファイルをファイル名順に 1 トランザクションずつ適用する（~50 行）。
- **MIRROR**: CONFIG_PATTERN（`db.New(ctx, cfg)` として `config.Config` を受ける）
- **IMPORTS**: `github.com/jackc/pgx/v5/pgxpool`, `embed`, `io/fs`
- **GOTCHA**:
  - `//go:embed` は**パッケージディレクトリより上を参照できない**。だから `migrations/` は `backend/internal/db/migrations/` に置く（`backend/migrations/` ではない）。
  - `vector(768)` の 768 は ruri-v3-310m 固有。別モデルに替える場合はここと Go 側の次元チェックの両方を直す。
  - HNSW インデックスはデータ 0 件でも作れる。今回のデータ量（数十件）なら投入前に作って問題ない。
- **VALIDATE**:
  ```sh
  docker compose up -d postgres
  cd backend && go test ./internal/db/ -v      # マイグレータの冪等性テスト
  psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" -c '\d chunks'
  # → embedding が vector(768)、chunks_embedding_hnsw が存在すること
  ```

---

### Task 4: config を拡張する

- **ACTION**: `config.Config` にフィールドを追加。
- **IMPLEMENT**:
  ```go
  type Config struct {
      Addr           string
      AllowedOrigins []string

      // DatabaseURL is the Postgres DSN (the database must have pgvector).
      DatabaseURL string

      // ObjectStore holds the S3-compatible (MinIO) connection.
      ObjectStore ObjectStoreConfig

      // EmbedURL / RerankURL are Text Embeddings Inference base URLs. TEI serves
      // one model per process, so embedding and reranking are separate hosts.
      EmbedURL  string
      RerankURL string

      // LLM points an OpenAI-compatible client at DeepSeek.
      LLM LLMConfig

      // Retrieval tunes the persona-weighted rerank. PersonaInfluence is the
      // lambda that blends cross-encoder relevance with persona affinity.
      Retrieval RetrievalConfig
  }

  type ObjectStoreConfig struct {
      Endpoint       string // in-cluster endpoint the backend dials
      PublicEndpoint string // endpoint baked into presigned URLs for the browser
      AccessKey      string
      SecretKey      string
      Bucket         string
      UseSSL         bool
  }

  // LLMConfig targets DeepSeek through an OpenAI-compatible client. DeepSeek
  // publishes no Go SDK of its own, so the OpenAI client with a swapped
  // base URL is the supported path.
  type LLMConfig struct {
      BaseURL string // https://api.deepseek.com
      APIKey  string // DEEPSEEK_API_KEY
      Model   string // deepseek-flash
  }

  type RetrievalConfig struct {
      CandidateK       int     // pgvector から取る件数
      FinalN           int     // プロンプトに載せる件数
      PersonaInfluence float64 // 0..1
      ContextPadBytes  int     // MinIO Range GET でチャンク前後に読み足すバイト数
  }
  ```
  デフォルト: `DATABASE_URL=postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable`、`EMBED_URL=http://localhost:8081`、`RERANK_URL=http://localhost:8082`、`DEEPSEEK_BASE_URL=https://api.deepseek.com`、`CHAT_MODEL=deepseek-flash`、`CANDIDATE_K=40`、`FINAL_N=8`、`PERSONA_INFLUENCE=0.25`、`CONTEXT_PAD_BYTES=1200`、`MINIO_PUBLIC_ENDPOINT=http://localhost:9000`。
  `envInt` / `envFloat` / `envBool` ヘルパを `env` と同じ形で追加する。
- **MIRROR**: CONFIG_PATTERN（`env(key, fallback)` の形をそのまま踏襲）
- **IMPORTS**: `strconv`
- **GOTCHA**: `CHAT_MODEL` のデフォルトは **`deepseek-flash`**。`deepseek-v4-flash` は**リタイア済みの旧名**なので使わない（現状は V4.1-Flash にルーティングされるが、依存すべきでない）。`DEEPSEEK_API_KEY` が未設定なら起動時に明示的に落とす（LLM 無しでは会話 API が意味を成さないため、遅延エラーにしない）。
- **VALIDATE**: `go test ./internal/config/ -v`（環境変数あり/なし両方でデフォルトが効くテーブル駆動テスト）

---

### Task 5: 埋め込みクライアント（プレフィックス責務をここに閉じる）

- **ACTION**: `internal/retrieval/embed.go` を作成。
- **IMPLEMENT**:
  ```go
  // Package retrieval turns a user utterance plus a persona into ranked knowledge.
  package retrieval

  // ruri-v3 is prefix-conditioned: the same text embedded under a different
  // prefix lands somewhere else in the space. Getting these wrong degrades
  // recall silently, so no caller outside this file writes a prefix.
  const (
      prefixQuery    = "検索クエリ: "
      prefixDocument = "検索文書: "
  )

  // Dimensions is ruri-v3-310m's output size and must match vector(768) in SQL.
  const Dimensions = 768

  type Embedder struct {
      baseURL string
      http    *http.Client
  }

  func NewEmbedder(baseURL string) *Embedder

  // EmbedQueries embeds retrieval queries. Persona interest topics also go
  // through here, not through a "トピック: " prefix: affinity is scored against
  // document chunks, and query↔document is the pairing ruri was trained on.
  func (e *Embedder) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)

  // EmbedDocuments embeds passages destined for the chunks table.
  func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
  ```
  内部は共通の `embed(ctx, prefix, texts)`：`POST {baseURL}/embed` に `{"inputs": [...], "normalize": true, "truncate": true}` を投げ、`[][]float32` を受ける。返却時に**長さが `Dimensions` であることを検証**し、違えばエラー。
- **MIRROR**: ERROR_HANDLING（エラーは `fmt.Errorf("embed: %w", err)` で包む）、NAMING_CONVENTION
- **IMPORTS**: `bytes`, `context`, `encoding/json`, `fmt`, `net/http`, `time`
- **GOTCHA**:
  - **プレフィックスの選択が精度を左右する最重要ポイント**。関心トピックに `トピック: ` を使いたくなるが、それは分類・クラスタリング用の空間。チャンク（`検索文書: `）との cosine を取るなら関心も `検索クエリ: ` で埋め込むのが正しいペアリング。
  - TEI の `/embed` は入力バッチの合計トークン数に上限がある。1 リクエスト 32 件程度で分割する。
  - `normalize: true` を必ず付ける（cosine 前提のため）。
- **VALIDATE**: `httptest.NewServer` でリクエストボディを捕捉し、`EmbedQueries(["合意形成"])` が `inputs: ["検索クエリ: 合意形成"]` を送ること、次元不一致でエラーになることをテーブル駆動でテスト。

---

### Task 6: リランククライアント

- **ACTION**: `internal/retrieval/rerank.go` を作成。
- **IMPLEMENT**:
  ```go
  type Reranker struct {
      baseURL string
      http    *http.Client
  }

  type RerankResult struct {
      Index int     // texts 内の元インデックス
      Score float32 // 0..1 に近い関連度
  }

  // Rerank scores each text against the query with a cross-encoder. Unlike the
  // bi-encoder, ruri's reranker takes raw pairs — no prefixes.
  func (r *Reranker) Rerank(ctx context.Context, query string, texts []string) ([]RerankResult, error)
  ```
  `POST {baseURL}/rerank` に `{"query": ..., "texts": [...], "raw_scores": false, "truncate": true}`。レスポンスは `[{"index":int,"score":float}]`。**返却順は score 降順**なので、呼び出し側が元順序に依存しないよう `Index` をそのまま返す。
- **MIRROR**: Task 5 の `Embedder` と同じ構造（同じパッケージ・同じエラー方針）
- **IMPORTS**: Task 5 と同じ
- **GOTCHA**:
  - **リランカーにはプレフィックスを付けない**（bi-encoder とは別モデル）。付けると精度が落ちる。
  - cross-encoder は候補数に比例して遅い。`CandidateK=40` を超えて投げない。
  - TEI の `/rerank` はモデルが sequence-classification でないと 400 を返す。`tei-rerank` が reranker モデルを指していることを確認。
- **VALIDATE**: `httptest` でプレフィックスが**付いていない**ことを確認するテスト（Task 5 の裏返し）。

---

### Task 7: ベクトル検索 + 性格リランキング（本機能の核）

- **ACTION**: `internal/retrieval/search.go` を作成。
- **IMPLEMENT**:
  ```go
  type Candidate struct {
      ChunkID    uuid.UUID
      DocumentID uuid.UUID
      Title      string
      Bucket     string
      ObjectKey  string
      Content    string
      ByteStart  int64
      ByteEnd    int64
      SizeBytes  int64
      Embedding  []float32

      Relevance float32 // cross-encoder
      Affinity  float32 // 性格の関心との一致度 (-1..1)
      Final     float32 // ブレンド後
  }

  type Searcher struct {
      pool     *pgxpool.Pool
      embedder *Embedder
      reranker *Reranker
      cfg      config.RetrievalConfig
  }

  // Search runs the persona-weighted retrieval pipeline for one or more
  // rewritten queries and returns the top FinalN candidates.
  func (s *Searcher) Search(ctx context.Context, p persona.Persona, queries []string) ([]Candidate, error)
  ```
  手順:
  1. `s.embedder.EmbedQueries(ctx, queries)`。
  2. 各クエリで pgvector 検索。`<=>` は cosine distance:
     ```sql
     SELECT c.id, c.document_id, d.title, d.bucket, d.object_key, d.size_bytes,
            c.content, c.byte_start, c.byte_end, c.embedding
     FROM chunks c JOIN documents d ON d.id = c.document_id
     ORDER BY c.embedding <=> $1
     LIMIT $2
     ```
     `chunk_id` で重複排除（複数クエリでヒットしたものは**最良の距離**を採用して 1 件に）。
  3. `s.reranker.Rerank(ctx, queries[0], contents)` で `Relevance` を得る。
  4. **足切り**: `threshold := 0.15 + 0.45*p.Personality.Skepticism`。`Relevance < threshold` を捨てる。
  5. **関心スコア**: 各候補について
     ```
     Affinity = Σ_i ( weight_i × cosine(chunk.Embedding, interest_i.Embedding) )
                / Σ_i |weight_i|
     ```
     （関心が 0 件なら `Affinity = 0`）。分母で正規化して -1..1 に収める。
  6. **ブレンド**: `Final = (1-λ)*Relevance + λ*((Affinity+1)/2)`。`Affinity` を 0..1 にリスケールしてから混ぜる（スケール不一致で λ の意味が壊れるため）。
  7. `Final` 降順にソートして `FinalN` 件返す。
- **MIRROR**: ERROR_HANDLING、TEST_STRUCTURE
- **IMPORTS**: `github.com/jackc/pgx/v5/pgxpool`, `github.com/pgvector/pgvector-go`, `github.com/google/uuid`, `sort`
- **GOTCHA**:
  - `<=>` は **cosine distance（小さいほど近い）**。`<->`（L2）や `<#>`（内積）と取り違えない。インデックスは `vector_cosine_ops` なので `<=>` 以外を使うとインデックスが効かない。
  - pgvector の値の受け渡しは `pgvector.NewVector(vec)` を使う。`[]float32` を直接渡すと型エラー。
  - `Affinity` と `Relevance` はスケールが違う（前者 -1..1、後者 0..1）。**リスケールを忘れると λ を上げた瞬間に順位が壊れる**。
  - 全候補が足切りされる場合がある。空スライスを正常系として返し、呼び出し側で「資料なし」を扱う。
- **VALIDATE**:
  - ユニット: `Embedder`/`Reranker` をインターフェース化してフェイクを注入し、`Skepticism` を 0→1 に振ると足切り件数が単調増加すること、`λ=0` で `Final == Relevance` になること、`λ=1` で関心順になることをテーブル駆動で検証。
  - 統合: `TEST_DATABASE_URL` があるときのみ走る `TestSearchAgainstPostgres`（無ければ `t.Skip`）。

---

### Task 8: MinIO からのコンテキスト拡張

- **ACTION**: `internal/objectstore/store.go` を作成。
- **IMPLEMENT**:
  ```go
  type Store struct {
      client        *minio.Client // in-cluster endpoint. データ取得用
      presignClient *minio.Client // PublicEndpoint で作る。ブラウザ向け URL 用
      bucket        string
  }

  func New(cfg config.ObjectStoreConfig) (*Store, error)

  // ExpandContext reads the original object around a chunk's byte range so the
  // model sees more than the embedded snippet — the chunk is the search unit,
  // this is the reading unit.
  func (s *Store) ExpandContext(ctx context.Context, key string, start, end, size int64, pad int) (string, error)

  // PresignedURL returns a short-lived link the frontend can use to open the source.
  func (s *Store) PresignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
  ```
  `ExpandContext` は `minio.GetObjectOptions` に `SetRange(max(0,start-pad), min(size-1, end+pad))` を設定して読み、**UTF-8 の壊れた先頭/末尾のルーンを削る**:
  ```go
  // A byte range can slice a multi-byte rune in half; Japanese text makes this
  // the common case rather than the edge case.
  b = bytes.ToValidUTF8(b, nil)
  ```
  さらに、先頭が文の途中なら最初の `。`/`\n` まで、末尾も最後の `。`/`\n` までで切り詰めて読みやすくする。
- **MIRROR**: CONFIG_PATTERN（`config.ObjectStoreConfig` を受ける）
- **IMPORTS**: `github.com/minio/minio-go/v7`, `github.com/minio/minio-go/v7/pkg/credentials`, `bytes`, `time`
- **GOTCHA**:
  - **`bytes.ToValidUTF8` を忘れると日本語が文字化けして LLM に渡る**。Range GET はバイト境界であってルーン境界ではない。
  - `end+pad` がオブジェクトサイズを超えると S3 実装によっては 416 を返す。`size-1` でクランプする（だから `size` を引数で受ける）。
  - Presigned URL のホストは `minio:9000`（コンテナ内名）になり**ブラウザから引けない**。`PublicEndpoint` で作った別クライアントで presign する。
- **VALIDATE**: `TEST_MINIO_ENDPOINT` があるときのみ走る統合テスト。マルチバイト文字をまたぐ範囲を要求して `utf8.ValidString(got)` が true になることを確認。

---

### Task 9: 性格 → プロンプト、およびクエリ書き換え

- **ACTION**: `internal/chat/prompt.go` を作成。
- **IMPLEMENT**:
  ```go
  // BuildSystemPrompt renders the persona's personality into the stable prefix of
  // every request in a conversation, so it is also the prompt-cache boundary.
  func BuildSystemPrompt(p persona.Persona) string
  ```
  組み立て内容（この順序を守る＝キャッシュ前置の安定性のため）:
  1. ペルソナ名と `Stance`（そのまま埋め込む）
  2. 関心軸: 正の重みは「重視する観点」、負の重みは「重視しない観点」として列挙
  3. `Skepticism` を言語化: 0.7 以上なら「根拠が薄い主張には明示的に留保を付ける」
  4. `Verbosity` を言語化: concise=「3文以内」/ balanced=「必要十分」/ detailed=「背景と反論も述べる」
  5. 引用規約: 「与えられた資料に基づいて答え、該当箇所を [1] のように参照する。資料にないことは推測せずその旨を述べる」

  ```go
  // RewriteQueries turns the user's utterance into retrieval queries the persona
  // would actually ask, using structured outputs so the result needs no parsing
  // heuristics.
  func RewriteQueries(ctx context.Context, c LLMClient, model string, p persona.Persona, history []Message, utterance string) ([]string, error)
  ```
  DeepSeek の JSON Output を使い、`{"queries": ["...", "..."]}` を最大 3 本返させる。**パラメータ名は実装時に api-docs.deepseek.com の JSON Output ガイドで確認すること**（OpenAI 互換なら `response_format: {"type": "json_object"}`、json_schema 対応の有無は未検証）。

  `json_object` しか使えない場合は schema による強制が効かないため、**必ずプロンプト側でも出力形状を明示し、パース失敗を正常系として扱う**。失敗時は**元の発話をそのまま 1 本のクエリとして返す**（検索を止めない）。
- **MIRROR**: ERROR_HANDLING（フォールバックしたことを `slog.Warn` に残す）
- **IMPORTS**: OpenAI 互換クライアント（`github.com/sashabaranov/go-openai` 想定）、`encoding/json`
- **GOTCHA**:
  - `BuildSystemPrompt` の出力に**タイムスタンプや会話 ID を混ぜない**。混ぜると DeepSeek の自動コンテキストキャッシュが毎回ミスする。
  - DeepSeek のキャッシュは**自動**。`cache_control` に相当する明示指定は無いので、効かせる手段は「先頭を固定する」ことだけ。だからシステムプロンプトの組み立て順が実質的なキャッシュ境界になる。
  - **JSON Output 時は `messages` のどこかに "json" の語を含める必要がある実装が多い**（OpenAI 互換 API の一般的な制約）。システムプロンプトに出力例を書けば自然に満たせる。実装時に 400 が出たらここを疑う。
  - `RewriteQueries` は毎ターン走る。`deepseek-flash` は安価なのでコスト面は問題にならないが、レイテンシは初トークンまでの時間に直接乗る。
- **VALIDATE**: `BuildSystemPrompt` のゴールデンテスト（同じ Persona からは同一文字列 = キャッシュ安定性の担保）。`RewriteQueries` は `LLMClient` インターフェースのフェイクで検証し、**不正 JSON が返ったとき**に元発話へフォールバックすることをテストする。

---

### Task 10: チャットエンジン（RAG オーケストレーション + ストリーミング）

- **ACTION**: `internal/chat/engine.go` を作成。
- **IMPLEMENT**:
  ```go
  // Event is one unit of progress the HTTP layer turns into an SSE frame.
  type Event struct {
      Type    string   `json:"type"` // "token" | "sources" | "done" | "error"
      Text    string   `json:"text,omitempty"`
      Sources []Source `json:"sources,omitempty"`
      Message *Message `json:"message,omitempty"`
      Error   string   `json:"error,omitempty"`
  }

  // LLMClient is the seam over the OpenAI-compatible DeepSeek client, kept
  // narrow so the engine's tests can drive a fake stream.
  type LLMClient interface {
      CreateChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
      CreateChatCompletionStream(ctx context.Context, req ChatRequest) (ChatStream, error)
  }

  type Engine struct {
      llm      LLMClient
      searcher *retrieval.Searcher
      objects  *objectstore.Store
      store    *Store
      cfg      config.Config
  }

  // Reply runs one turn and pushes events to out. It owns the whole turn: it
  // persists both the user message and the assistant reply, so a dropped client
  // does not lose the conversation.
  func (e *Engine) Reply(ctx context.Context, conversationID uuid.UUID, utterance string, out chan<- Event) error
  ```
  手順:
  1. conversation + persona + 直近 N 件の履歴をロード。
  2. user メッセージを保存（**生成前に保存**。途中で切れても発話は残る）。
  3. `RewriteQueries`。
  4. `searcher.Search`。
  5. 上位 `FinalN` について `objects.ExpandContext` を**並行に**取得（`errgroup` で最大 4 並列）。
  6. `Event{Type:"sources"}` を送る（**生成開始前**に根拠を出す＝体感速度が上がる）。
  7. DeepSeek ストリーミング:
     ```go
     // system は先頭固定。DeepSeek の自動キャッシュはこの前置一致で効く。
     msgs := append([]ChatMessage{{Role: "system", Content: systemPrompt}}, history...)
     msgs = append(msgs, ChatMessage{Role: "user", Content: docsBlock + utterance})

     stream, err := e.llm.CreateChatCompletionStream(ctx, ChatRequest{
         Model:    e.cfg.LLM.Model, // deepseek-flash
         Stream:   true,
         Messages: msgs,
     })
     if err != nil {
         return fmt.Errorf("start stream: %w", err)
     }
     defer stream.Close()

     var sb strings.Builder
     for {
         resp, err := stream.Recv()
         if errors.Is(err, io.EOF) {
             break
         }
         if err != nil {
             return fmt.Errorf("stream recv: %w", err)
         }
         // OpenAI 互換 API は usage 専用など choices が空のチャンクを挟むことがある。
         if len(resp.Choices) == 0 {
             continue
         }
         if d := resp.Choices[0].Delta.Content; d != "" {
             sb.WriteString(d)
             out <- Event{Type: "token", Text: d}
         }
         if fr := resp.Choices[0].FinishReason; fr == "length" {
             slog.Warn("reply truncated by max_tokens", "conversation_id", conversationID)
         }
     }
     reply := sb.String()
     ```
  8. `reply` を assistant メッセージ + citations として保存、`Event{Type:"done", Message: ...}` を送る。
- **MIRROR**: LIFECYCLE_PATTERN（goroutine + channel + `context` の扱い）、ERROR_HANDLING
- **IMPORTS**: OpenAI 互換クライアント（`github.com/sashabaranov/go-openai` 想定）、`golang.org/x/sync/errgroup`、`errors`, `io`, `strings`
- **GOTCHA**:
  - **`len(resp.Choices) == 0` のチャンクを必ず握る**。OpenAI 互換ストリームは usage 専用チャンクなどを挟むため、`resp.Choices[0]` を無条件に触るとパニックする。
  - **終端は `io.EOF`**。`errors.Is(err, io.EOF)` で正常終了、それ以外の `err` は異常。`err == nil` だけ見て抜けるループにしない。
  - `FinishReason == "length"` は出力打ち切り。DeepSeek は最大出力 384K と大きいので通常は起きないが、検出したらログに残す。
  - **`defer stream.Close()` を忘れない**。閉じ忘れるとコネクションが滞留する。
  - **DeepSeek 固有のエラー挙動（レート制限時の応答形状、リトライ可否）は未検証**。実装時に 429 / 5xx を実際に踏んで確認すること。
  - クライアント切断は `ctx.Done()` で来る。**それでも assistant メッセージは保存する**（`context.WithoutCancel` で保存用 ctx を分ける）。
  - 資料ブロックは user ロールのメッセージ内に `<資料 id="1" title="...">...</資料>` の形で入れる。system に入れると毎ターン先頭が変わり、自動キャッシュが壊れる。
- **VALIDATE**: `LLMClient` のフェイク実装でストリームを流し、`sources` → `token`* → `done` の順序、`choices` 空チャンクを飛ばすこと、`io.EOF` で正常終了すること、エラー時に `error` イベントが出ることをテスト。

---

### Task 11: SSE ハンドラ

- **ACTION**: `internal/api/chat.go` を作成。
- **IMPLEMENT**:
  ```go
  func (h *handlers) handleSendMessage(w http.ResponseWriter, r *http.Request) {
      id, err := uuid.Parse(r.PathValue("id"))
      // ... リクエストボディのデコードとバリデーション

      w.Header().Set("Content-Type", "text/event-stream")
      w.Header().Set("Cache-Control", "no-cache")
      w.Header().Set("Connection", "keep-alive")
      w.Header().Set("X-Accel-Buffering", "no") // リバースプロキシのバッファリング抑止
      w.WriteHeader(http.StatusOK)

      rc := http.NewResponseController(w)
      rc.Flush()

      events := make(chan chat.Event, 16)
      go func() {
          defer close(events)
          if err := h.deps.Chat.Reply(r.Context(), id, req.Content, events); err != nil {
              events <- chat.Event{Type: "error", Error: err.Error()}
          }
      }()

      ticker := time.NewTicker(15 * time.Second) // keep-alive comment
      defer ticker.Stop()
      for {
          select {
          case ev, ok := <-events:
              if !ok {
                  return
              }
              _ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
              writeSSE(w, ev)
              rc.Flush()
          case <-ticker.C:
              fmt.Fprint(w, ": keep-alive\n\n")
              rc.Flush()
          case <-r.Context().Done():
              return
          }
      }
  }
  ```
  `writeSSE` は `event: {type}\ndata: {json}\n\n` を書く。
- **MIRROR**: `api.go:21-31` のハンドラ形状、`writeJSON` に倣った `writeSSE`
- **IMPORTS**: `net/http`, `time`, `fmt`, `encoding/json`
- **GOTCHA**:
  - **Task 2 の `Unwrap()` が無いとここが動かない**。`rc.Flush()` が `ErrNotSupported` を返す。
  - `WriteHeader` の前に必ず全ヘッダを設定する。
  - `data:` の値に**生の改行を入れない**。必ず JSON エンコードする（トークンに改行が含まれるため、これを怠るとフレームが壊れる）。
  - SSE でも CORS ヘッダは必要。既存 `cors` ミドルウェアの内側に置くこと。
  - `WriteTimeout: 0` にしたので、代わりに `rc.SetWriteDeadline` で**1フレームごとの**デッドラインを設ける。
- **VALIDATE**: `httptest.NewServer` に対して実 HTTP で接続し、`bufio.Scanner` でフレームを読み、`event: token` が**複数回に分かれて**届くことを確認（1回にまとまっていたら Flush が効いていない）。

---

### Task 12: ペルソナ CRUD とルーティング統合

- **ACTION**: `internal/persona/{persona.go,store.go}`、`internal/api/persona.go` を作成し、`api.go` を更新。
- **IMPLEMENT**: ドメイン型は Task 3 のスキーマに 1:1 対応。`store.Create` は関心トピックを `Embedder.EmbedQueries` に通してから `persona_interests` に入れる（**トランザクション内**）。
  `api.go` は依存を持つように変更:
  ```go
  type Deps struct {
      Log       *slog.Logger
      Personas  *persona.Store
      Chat      *chat.Engine
      ChatStore *chat.Store
      Objects   *objectstore.Store
  }

  func NewHandler(cfg config.Config, d Deps) http.Handler {
      h := &handlers{cfg: cfg, deps: d}
      mux := http.NewServeMux()
      mux.HandleFunc("GET /api/health", h.handleHealth)
      mux.HandleFunc("GET /api/personas", h.handleListPersonas)
      mux.HandleFunc("POST /api/personas", h.handleCreatePersona)
      mux.HandleFunc("GET /api/personas/{id}", h.handleGetPersona)
      mux.HandleFunc("PATCH /api/personas/{id}", h.handleUpdatePersona)
      mux.HandleFunc("POST /api/conversations", h.handleCreateConversation)
      mux.HandleFunc("GET /api/conversations/{id}", h.handleGetConversation)
      mux.HandleFunc("POST /api/conversations/{id}/messages", h.handleSendMessage)
      mux.HandleFunc("GET /api/documents/{id}", h.handleGetDocument)
      return requestLogger(d.Log, cors(cfg.AllowedOrigins, mux))
  }
  ```
  既存の `GET /api/hello` は**削除する**（スキャフォールドの疎通確認用で役目を終えたため）。`handleHealth` は DB と TEI の疎通も見るように拡張し、`{"status":"ok","postgres":"ok","embed":"ok","rerank":"ok"}` を返す。
- **MIRROR**: NAMING_CONVENTION、`writeJSON`
- **IMPORTS**: `github.com/google/uuid`
- **GOTCHA**:
  - `r.PathValue("id")` は Go 1.22+ の機能。`{id}` パターンとセットで使う。
  - `handleHealth` を重くしすぎない。各チェックに 2 秒のタイムアウトを付け、失敗しても 200 で `"degraded"` を返す（ヘルスチェックが原因で compose が落ちるのを防ぐ）。
  - `frontend/src/api/client.ts` と `App.tsx` の `getHello` 参照を消す。消し忘れると `npm run build` の `tsc -b` が落ちる。
- **VALIDATE**: `go build ./... && go vet ./...`、既存の `api_test.go` を新ルート用に書き換えて `go test ./...`

---

### Task 13: dev seed スクリプト

- **ACTION**: `backend/cmd/seed/main.go` を作成。
- **IMPLEMENT**: `backend/testdata/knowledge/*.md` を読み、
  1. MinIO にアップロード → `documents` に登録
  2. 800 文字・オーバーラップ 150 文字でチャンク分割し、**バイトオフセットを記録**
  3. `EmbedDocuments` でまとめて埋め込み → `chunks` に投入
  4. サンプルペルソナを 2 体作成（例: 「批評家」= Skepticism 0.85 / 関心 `{形式的検証:+0.8, マーケティング:-0.5}`、「実務家」= Skepticism 0.3 / 関心 `{運用コスト:+0.9, 理論的純粋性:-0.3}`）
- **MIRROR**: LIFECYCLE_PATTERN（`run(ctx) error` + `main` は終了コードのみ）
- **IMPORTS**: Task 5/8 のパッケージ
- **GOTCHA**:
  - **チャンク分割はルーン単位で行い、byte_start/byte_end はバイトで記録する**。日本語で `len(s)` と `utf8.RuneCountInString(s)` を混同するとオフセットがずれ、Task 8 の Range GET が的外れな位置を読む。
  - 冪等にする（`ON CONFLICT (bucket, object_key) DO UPDATE`）。何度流しても壊れないこと。
- **VALIDATE**:
  ```sh
  make seed
  psql "$DATABASE_URL" -c 'SELECT count(*) FROM chunks;'   # > 0
  psql "$DATABASE_URL" -c 'SELECT name, skepticism FROM personas;'
  ```

---

### Task 14: フロントエンド（SSE クライアント + チャット UI）

- **ACTION**: `frontend/src/api/client.ts` と `App.tsx` を更新。
- **IMPLEMENT**: `client.ts` に型と SSE 関数を追加:
  ```ts
  export type Interest = { topic: string; weight: number }
  export type Persona = {
    id: string; name: string; stance: string
    verbosity: 'concise' | 'balanced' | 'detailed'
    skepticism: number; interests: Interest[]
  }
  export type Source = {
    chunkId: string; documentId: string; title: string
    url: string; relevance: number; affinity: number
  }
  export type ChatEvent =
    | { type: 'token'; text: string }
    | { type: 'sources'; sources: Source[] }
    | { type: 'done'; message: { id: string; content: string } }
    | { type: 'error'; error: string }

  // sendMessage streams the reply. fetch + ReadableStream rather than
  // EventSource: EventSource cannot issue a POST.
  export async function* sendMessage(
    conversationId: string, content: string, signal?: AbortSignal,
  ): AsyncGenerator<ChatEvent>
  ```
  `App.tsx` はペルソナ選択・メッセージリスト・入力欄・根拠リストを持つ。トークンは `setMessages` で末尾要素に追記。
- **MIRROR**: FRONTEND_CLIENT_PATTERN（`getJSON` はそのまま残して再利用）
- **IMPORTS**: なし（追加依存なし）
- **GOTCHA**:
  - **`EventSource` は使えない**（POST が投げられない）。`fetch` + `res.body.getReader()` + `TextDecoder` で自前にフレームを切る。
  - チャンク境界がフレーム境界と一致しないので、**バッファに貯めて `\n\n` で分割**する。これを怠るとトークンが欠ける。
  - React 19 の StrictMode は effect を 2 回走らせる。`AbortController` で必ず前のストリームを中断する。
  - `vite.config.ts` のプロキシは SSE をそのまま通す想定。流れない場合は proxy 設定を疑う。
- **VALIDATE**: `npm run build`（`tsc -b` を含む）→ `make dev` → ブラウザで実際にトークンが逐次出ることを目視。

---

### Task 15: Makefile / .env.example / README を更新

- **ACTION**: 3 ファイルを更新。
- **IMPLEMENT**:
  - `Makefile`: `up`（compose up -d + healthy 待ち）、`down`、`seed`、`logs` を追加。`dev` は `up` に依存させる。
  - `.env.example`: Task 4 の全変数 + `DEEPSEEK_API_KEY`。
  - `README.md`: アーキテクチャ図、API 表、環境変数表、「性格が効く3点」の説明、初回起動でモデル DL に数分かかる旨。
- **MIRROR**: 既存 `Makefile` の `## ` コメント形式（`make help` が拾う）
- **GOTCHA**: `.env` はコミットされない（`.gitignore` 済み）。`DEEPSEEK_API_KEY` の取得方法と扱いを README に明記する。
- **VALIDATE**: `make help` に新ターゲットが並ぶこと。

---

## Testing Strategy

### Unit Tests

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| `TestStatusRecorderSupportsFlush` | `statusRecorder` でラップした ResponseWriter | `ResponseController.Flush()` が nil | ✅ 回帰防止 |
| `TestEmbedderAppliesQueryPrefix` | `EmbedQueries(["合意形成"])` | 送信ボディが `検索クエリ: 合意形成` | |
| `TestEmbedderAppliesDocumentPrefix` | `EmbedDocuments(["本文"])` | 送信ボディが `検索文書: 本文` | |
| `TestEmbedderRejectsWrongDimensions` | TEI が 512 次元を返す | エラー | ✅ |
| `TestRerankerSendsNoPrefix` | `Rerank("q", ["a"])` | ボディに `検索` が含まれない | ✅ |
| `TestSkepticismRaisesThreshold` | skepticism 0.0 / 0.5 / 1.0 | 残る候補数が単調減少 | |
| `TestPersonaInfluenceBlend` | λ=0 / 0.5 / 1 | λ=0 で Final==Relevance、λ=1 で関心順 | ✅ 境界 |
| `TestAffinityWithNoInterests` | 関心 0 件のペルソナ | Affinity=0、パニックしない | ✅ ゼロ除算 |
| `TestExpandContextKeepsValidUTF8` | マルチバイト境界をまたぐ範囲 | `utf8.ValidString` が true | ✅ |
| `TestExpandContextClampsToObjectSize` | end+pad > size | エラーにならず末尾まで | ✅ |
| `TestBuildSystemPromptIsStable` | 同一 Persona を 2 回 | 完全一致（キャッシュ安定性） | |
| `TestRewriteQueriesFallsBackOnError` | LLM がエラー / 不正 JSON | 元発話 1 本を返す | ✅ |
| `TestEngineEmitsEventOrder` | フェイクストリーム | sources → token* → done | |
| `TestEngineSkipsEmptyChoices` | `choices: []` のチャンク | パニックせずスキップ | ✅ |
| `TestSSEFramesAreSeparate` | 3 トークン | 3 回に分けて到達 | ✅ Flush 検証 |
| `TestSSEEncodesNewlinesInTokens` | 改行を含むトークン | フレームが壊れない | ✅ |

### Edge Cases Checklist

- [ ] 空の発話 → 400
- [ ] 存在しない conversation_id → 404
- [ ] 検索結果 0 件（全て足切り）→ 「資料が見つからない」と答えて 200
- [ ] 関心 0 件のペルソナ
- [ ] 生成途中のクライアント切断 → assistant メッセージは保存される
- [ ] TEI ダウン → 502 + 明示的なエラーメッセージ
- [ ] DeepSeek レート制限（429）→ リトライ後もダメなら error イベント
- [ ] ストリーム中に `choices` が空のチャンク → スキップしてパニックしない
- [ ] `finish_reason: length` → 応答は返しつつログに警告
- [ ] マルチバイト文字をまたぐ Range GET
- [ ] 同一チャンクが複数クエリでヒット → 1 件に重複排除

---

## Validation Commands

### Static Analysis
```sh
cd backend && go vet ./...
cd frontend && npx tsc -b --noEmit
```
EXPECT: 出力なし（エラーゼロ）

### Unit Tests
```sh
cd backend && go test ./... -race
```
EXPECT: 全パス。`-race` を必ず付ける（engine が goroutine + channel を使うため）

### Lint
```sh
cd frontend && npm run lint
cd backend && gofmt -l .
```
EXPECT: `gofmt -l` の出力が空

### Database Validation
```sh
docker compose up -d postgres && sleep 5
cd backend && go run ./cmd/server &   # 起動時マイグレーション
psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" -c '\d chunks'
psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" \
  -c "SELECT indexname FROM pg_indexes WHERE tablename='chunks';"
```
EXPECT: `embedding | vector(768)`、`chunks_embedding_hnsw` が存在

### Build
```sh
make build
```
EXPECT: `frontend/dist` と `backend/bin/server` が生成される

### End-to-End
```sh
make up && make seed && make dev
# 別ターミナル:
curl -s localhost:8080/api/health | jq
PERSONA=$(curl -s localhost:8080/api/personas | jq -r '.[0].id')
CONV=$(curl -s -X POST localhost:8080/api/conversations \
  -H 'content-type: application/json' -d "{\"persona_id\":\"$PERSONA\"}" | jq -r .id)
curl -N -X POST "localhost:8080/api/conversations/$CONV/messages" \
  -H 'content-type: application/json' -d '{"content":"合意形成アルゴリズムについて教えて"}'
```
EXPECT: `event: sources` が先に来て、その後 `event: token` が**逐次**流れ、最後に `event: done`

### 性格の効きの検証（この機能の受け入れ条件そのもの）
```sh
# 同じ質問を「批評家」と「実務家」に投げ、sources の順序が変わることを確認
```
EXPECT: 2 体で上位資料の順位が入れ替わる。**変わらなければ λ か関心埋め込みが効いていない**ので Task 7 を疑う

### Browser Validation
```sh
make dev   # → http://localhost:5173
```
EXPECT: ペルソナを選んで送信するとトークンが逐次現れ、根拠リストに関連スコアと関心スコアが別々に表示される

### Manual Validation
- [ ] `docker compose up` から `make seed` まで、README の手順だけで動く
- [ ] ペルソナを切り替えると同じ質問への回答の切り口が変わる
- [ ] 根拠リンクをクリックすると MinIO の原本が開く
- [ ] ブラウザをリロードしても会話が復元される
- [ ] 生成中にタブを閉じて開き直すと、ユーザ発話と（完了していれば）応答が残っている

---

## Acceptance Criteria

- [ ] 全タスク完了
- [ ] 全 validation コマンドがパス
- [ ] テストが書かれ、パスしている（`-race` 込み）
- [ ] `go vet` / `tsc -b` / `gofmt -l` がクリーン
- [ ] SSE がフレーム単位で逐次届く（まとめて届かない）
- [ ] **同一質問で性格の異なる 2 ペルソナが異なる資料順位を出す**
- [ ] 引用が MinIO の原本に辿れる

## Completion Checklist

- [ ] コードが Patterns to Mirror の形に従っている
- [ ] エラー処理が `writeJSON` / `slog` の既存方針と揃っている
- [ ] ログが `log/slog` のキーバリュー形式
- [ ] テストがテーブル駆動 + `httptest`、外部アサーションライブラリ無し
- [ ] マジックナンバーが `config` に出ている（λ、K、N、pad）
- [ ] README と `.env.example` が更新済み
- [ ] NOT Building の項目に手を出していない
- [ ] ruri のプレフィックスが `retrieval` パッケージの外に漏れていない

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| TEI が ModernBERT-**Ja** を読めない | 中 | 高（埋め込み全体が止まる） | Task 1 の VALIDATE で最初に検証する。ダメなら `sentence-transformers` + FastAPI の Python サイドカーに差し替え（`Embedder`/`Reranker` は HTTP 越しなので Go 側は無変更で済む） |
| `Unwrap()` 漏れで SSE が動かない | 高 | 高 | Task 2 を最優先で実施し回帰テストを置く |
| 日本語の Range GET が文字化け | 高 | 中 | `bytes.ToValidUTF8` + 専用テスト |
| バイト/ルーン混同でオフセットずれ | 中 | 中 | seed とチャンク分割で単位を明示、テストで往復検証 |
| Apple Silicon で TEI が遅い | 高 | 低 | dev 用途なので許容。耐えられなければ `ruri-v3-30m`(256次元) に落とす（**その場合 SQL の `vector(768)` も変更**） |
| DeepSeek の JSON Output が json_schema 非対応 | 中 | 中 | `json_object` でも動くようプロンプト側に出力形状を明示し、パース失敗を正常系（元発話へフォールバック）として扱う設計にしてある |
| OpenAI 互換クライアントが DeepSeek の差異を吸収しきれない | 中 | 中 | `LLMClient` インターフェースで隔離済み。差異が出たら実装を差し替えるだけで engine は無変更 |
| λ の調整が効かず性格が体感できない | 中 | 高（機能の存在意義） | 根拠リストに relevance と affinity を**分けて**表示し、効きを可視化。`PERSONA_INFLUENCE` を環境変数にして即座に A/B |
| Presigned URL がブラウザから引けない | 中 | 低 | `PublicEndpoint` 用クライアントを分離（Task 8） |
| 自動キャッシュが効かず入力コストが乗る | 中 | 低 | コスト影響のみ。レスポンスのキャッシュヒット指標を計測し、効いていなければ先頭固定が崩れていないか確認 |

## Notes

- ⚠️ **DeepSeek の未検証事項が 2 つある**。実装時に api-docs.deepseek.com で必ず確認すること。**推測でパラメータ名を書かないこと**:
  1. **JSON Output** — パラメータ名（`response_format` か否か）と json_schema 対応の有無。Task 9 のクエリ書き換えが依存する。
  2. **Context Caching** — 自動であることは確認済みだが、ヒット判定の粒度とレスポンス上の指標フィールド名は未確認。
  まず疎通を最小コードで確認してから Task 9/10 を書くのが速い:
  ```sh
  curl -s https://api.deepseek.com/chat/completions \
    -H "Authorization: Bearer $DEEPSEEK_API_KEY" -H 'content-type: application/json' \
    -d '{"model":"deepseek-flash","messages":[{"role":"user","content":"ping"}],"stream":true}' | head
  ```
- **モデル ID は `deepseek-flash`**。`deepseek-v4-flash` はリタイア済みの旧名なので使わない。
- DeepSeek は OpenAI 互換だが**完全互換ではない**。差異はすべて `LLMClient` の実装側に閉じ込め、`chat.Engine` には漏らさないこと。
- ruri-v3 には 30m / 70m / 130m / 310m がある。310m は 768 次元。**モデルを変えたら次元数が変わり、SQL の `vector(768)` と `retrieval.Dimensions` の両方を直す必要がある**。
- 会話履歴が伸びたときの圧縮（compaction）は今回スコープ外。直近 N ターンで打ち切る単純方式にし、TODO コメントを残す。
- `backend/go.mod` のモジュールパスは `github.com/shun/kaigi/backend`（スキャフォールド時の仮置き）。実リポジトリ名が違う場合、このプランの import パスも読み替えること。
