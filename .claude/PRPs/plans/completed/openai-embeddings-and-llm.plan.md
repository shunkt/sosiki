# Plan: OpenAI 埋め込み / LLM への移行（TEI 廃止）

## Summary
ローカルで Hugging Face TEI（`tei-embed` / `tei-rerank`）が動かないため、埋め込みを OpenAI
Embeddings API（`text-embedding-3-small`、ネイティブ 1536 次元）に、チャット LLM を DeepSeek から
OpenAI Chat Completions に置き換える。OpenAI には rerank エンドポイントが無いので、cross-encoder
リランカーは廃止し、pgvector のコサイン距離を `Relevance` に流用する。結果として docker-compose から
TEI コンテナ 2 つが消え、外部依存は OpenAI API キー 1 本に集約される。

## User Story
As a ローカル開発者,
I want Docker で TEI モデルを動かさずに埋め込みと生成を行いたい,
So that Apple Silicon 上で amd64 エミュレーションの OOM に悩まされず `make up && make seed && make dev` だけで動かせる.

## Problem → Solution
- **現状**: `tei-embed` / `tei-rerank`（`ghcr.io/huggingface/text-embeddings-inference:cpu-1.8`、
  `platform: linux/amd64`）が ruri-v3-310m と ruri-v3-reranker-310m（計 ~2.5GB）をロードする。
  Apple Silicon 上の amd64 エミュレーションで warmup がメモリを食い潰し `/health` に到達しない。
  埋め込み 768 次元、リランクは TEI `/rerank`、チャットは DeepSeek。
- **理想**: 埋め込みは OpenAI `/v1/embeddings`（1536 次元）、リランクはベクトル距離由来の類似度、
  チャットは OpenAI `/v1/chat/completions`。Docker は postgres + MinIO のみ。

## Metadata
- **Complexity**: Large
- **Source PRD**: N/A
- **PRD Phase**: N/A
- **Estimated Files**: 15（新規 3 / 更新 10 / 削除 2）

---

## 確定した設計判断（実装中に再検討しないこと）

| # | 論点 | 決定 |
|---|---|---|
| 1 | リランカー | **廃止**。`vectorSearch` の `distance` から `Relevance = 1 - distance`（コサイン類似度）を導出。`internal/retrieval/rerank.go` と `rerank_test.go` を削除 |
| 2 | 埋め込み次元 | **1536（`text-embedding-3-small` ネイティブ）**。マイグレーション `0002` で `vector(768)` → `vector(1536)` |
| 3 | チャット LLM | **OpenAI に完全置換**。`DEEPSEEK_*` 環境変数は廃止し `OPENAI_*` に統一。DeepSeek のコードパスは残さない |
| 4 | 閾値の再調整 | スケルティシズム閾値は TEI cross-encoder のスコア分布向けの `0.15 + 0.45*S` だった。コサイン類似度は分布が違うので **`RelevanceFloor + SkepticismSpan*S`（既定 `0.25 + 0.20*S`）に変更し、env で調整可能にする**（下記 Notes 参照） |
| 5 | ruri プレフィックス | **完全削除**。OpenAI 埋め込みはプレフィックス条件付けされていない。`EmbedQueries` / `EmbedDocuments` の 2 メソッドは呼び出し側の seam として残すが、中身は同一 |

---

## UX Design

### Before / After
ブラウザから見える挙動は変わらない（同じ SSE、同じ `sources` イベント、同じ引用 UI）。
変わるのは開発者体験と `/api/health` のレスポンス形。

```
Before (開発者)                          After (開発者)
┌────────────────────────────────┐      ┌────────────────────────────────┐
│ make up                        │      │ make up                        │
│  → postgres  healthy           │      │  → postgres  healthy           │
│  → minio     healthy           │      │  → minio     healthy           │
│  → tei-embed  ✗ OOM / 起動せず  │      │ (TEI コンテナは存在しない)       │
│  → tei-rerank ✗ OOM / 起動せず  │      │                                │
│  初回 ~2.5GB のモデル DL        │      │  モデル DL なし                  │
│ .env: DEEPSEEK_API_KEY         │      │ .env: OPENAI_API_KEY のみ        │
└────────────────────────────────┘      └────────────────────────────────┘
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| `GET /api/health` | `{"status":"ok","embed":"ok","rerank":"ok"}` | `{"status":"ok","db":"ok"}` | TEI プローブが消え、代わりに Postgres を ping |
| `make up` | postgres/minio/tei-embed/tei-rerank の healthy 待ち | postgres/minio のみ | Makefile の `until` ループからサービス名 2 つを除去 |
| 引用の `relevance` 値 | cross-encoder スコア（おおよそ 0..1、分布は S 字） | コサイン類似度（0..1、分布は中央寄り） | フロントは数値をそのまま表示しているだけなので変更不要 |
| レイテンシ | 埋め込み+リランクでローカル数百 ms | 埋め込み 1 往復のみ（リランク呼び出しが消える） | ネットワーク往復に変わるが呼び出し回数は減る |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `backend/internal/retrieval/embed.go` | 1-111 | 置き換える本体。プレフィックス/バッチ/次元検証の既存構造 |
| P0 | `backend/internal/retrieval/search.go` | 37-159, 201-242 | `crossEncoder` seam の除去、`distance` → `Relevance` の配線先 |
| P0 | `backend/internal/config/config.go` | 12-116 | `EmbedURL`/`RerankURL`/`LLMConfig` の定義と `Validate()` |
| P0 | `backend/internal/chat/deepseek.go` | 1-89 | OpenAI クライアントへリネームする対象。`LLMClient` への適合方法 |
| P0 | `backend/internal/db/migrations/0001_init.sql` | 20-56 | `vector(768)` と `chunks_embedding_hnsw` の定義。`0002` で書き換える対象 |
| P1 | `backend/internal/retrieval/search_test.go` | 17-127 | `fakeReranker` を使う 4 テスト。全て書き換えが必要 |
| P1 | `backend/internal/retrieval/embed_test.go` | 1-111 | プレフィックステスト 2 本は削除、次元/バッチテストは流用 |
| P1 | `backend/cmd/seed/main.go` | 60-73, 117-137, 特に `seedPersonas` | `persona_interests` を消した後の再 seed 問題（GOTCHA あり） |
| P1 | `backend/internal/persona/store.go` | 13-18, 46-113, 204-224 | `embedder` seam と interests の INSERT SQL。`ReplaceInterests` を足す場所 |
| P1 | `backend/internal/api/api.go` | 28-99 | `Deps`、`handleHealth` の `check` クロージャ |
| P2 | `backend/internal/api/api_test.go` | 20-54 | `testConfig()` が `EmbedURL`/`RerankURL` を使っている。`TestHealth` は "degraded" を期待 |
| P2 | `backend/internal/chat/engine.go` | 100-185 | `e.cfg.LLM.Model` の参照箇所 2 つ。変更不要だが確認 |
| P2 | `docker-compose.yml` / `Makefile` / `.env.example` / `README.md` | all | TEI / DeepSeek への言及を全削除 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| OpenAI Embeddings | `https://platform.openai.com/docs/api-reference/embeddings/create` | `input` は string か []string。`dimensions` は text-embedding-3 系のみ対応。レスポンスは `data[].index` + `data[].embedding` |
| Matryoshka / dimensions | `https://platform.openai.com/docs/guides/embeddings#reducing-embedding-dimensions` | `dimensions` パラメータ経由なら OpenAI 側で正規化済みが返る（手動切り詰めは再正規化が必要）。今回はネイティブ 1536 なので正規化はそのまま有効 |
| go-openai Embeddings | `$(go env GOMODCACHE)/github.com/sashabaranov/go-openai@v1.42.0/embeddings.go` | `EmbeddingRequest` 自身が `EmbeddingRequestConverter` を満たす。`Dimensions int` フィールドあり（`omitempty`）。`openai.SmallEmbedding3 = "text-embedding-3-small"` |
| pgvector `<=>` | `https://github.com/pgvector/pgvector#distances` | `<=>` はコサイン**距離** = `1 - cosine_similarity`。正規化ベクトルなら 0..2、実用上は 0..1 台 |

**KEY_INSIGHT**: `go-openai` v1.42.0 は既に `go.mod` にある（`github.com/sashabaranov/go-openai v1.42.0`）。埋め込みのために新規依存は不要。
**APPLIES_TO**: Task 2（`embed.go` 書き換え）
**GOTCHA**: `go.mod` の require はすべて `// indirect` でマークされている。`go mod tidy` を走らせると直接依存に格上げされる差分が出るのは正常。

**KEY_INSIGHT**: OpenAI Embeddings は入力を自動切り詰めしない（TEI の `truncate: true` に相当する機能がない）。8192 トークン超は 400。
**APPLIES_TO**: Task 2
**GOTCHA**: `cmd/seed` の `chunkSize = 800`（ルーン）なので余裕がある。空文字列も 400 になるので `embedBatch` 側でガードする。

**KEY_INSIGHT**: gpt-5 系は reasoning 系モデルのため `temperature` の任意指定や `max_tokens` が使えない場合がある。
**APPLIES_TO**: Task 5
**GOTCHA**: 現行の `toOpenAIRequest` は `Model` / `Messages` / `Stream` / `ResponseFormat` しか設定していないので影響なし。将来 `temperature` を足すときは要確認。`response_format: {"type":"json_object"}` は gpt-4o / gpt-5 系ともサポートされている。

---

## Patterns to Mirror

### NAMING_CONVENTION
プロバイダ実装は「プロバイダ名.go」にまとめ、型名もプロバイダ名を冠する。
```go
// SOURCE: backend/internal/chat/deepseek.go:17-25
type DeepSeekClient struct {
	client *openai.Client
}

func NewDeepSeekClient(cfg config.LLMConfig) *DeepSeekClient {
	oaCfg := openai.DefaultConfig(cfg.APIKey)
	oaCfg.BaseURL = cfg.BaseURL
	return &DeepSeekClient{client: openai.NewClientWithConfig(oaCfg)}
}
```
→ `OpenAIClient` / `NewOpenAIClient` / ファイル `openai.go` に置換する。

### ERROR_HANDLING
すべて `fmt.Errorf("<パッケージまたは操作>: <何をしていたか>: %w", err)`。sentinel エラーは
`persona.ErrNotFound` のみ。パニックもカスタムエラー型も使わない。
```go
// SOURCE: backend/internal/retrieval/embed.go:80-99
body, err := json.Marshal(embedRequest{Inputs: inputs, Normalize: true, Truncate: true})
if err != nil {
	return nil, fmt.Errorf("embed: marshal request: %w", err)
}
...
if resp.StatusCode != http.StatusOK {
	return nil, fmt.Errorf("embed: TEI returned %s", resp.Status)
}
```
```go
// SOURCE: backend/internal/chat/deepseek.go:29-35
if err != nil {
	return ChatResponse{}, fmt.Errorf("deepseek: create chat completion: %w", err)
}
if len(resp.Choices) == 0 {
	return ChatResponse{}, fmt.Errorf("deepseek: response had no choices")
}
```

### LOGGING_PATTERN
`log/slog` のみ。構造化キーはスネークケース。`main` で作った `*slog.Logger` を注入するか、
パッケージ内では `slog.Warn`/`slog.Error` を直接使う。Info は起動・マイグレーション・seed のみ。
```go
// SOURCE: backend/internal/chat/engine.go:180
slog.Warn("reply truncated by max output", "conversation_id", conversationID)
```
```go
// SOURCE: backend/internal/db/db.go:91-93
if log != nil {
	log.Info("migration applied", "version", name)
}
```

### REPOSITORY_PATTERN
`*pgxpool.Pool` を直接持つ Store 構造体。生 SQL をバッククォート文字列で書き、`$1` プレースホルダ。
ベクトルは `pgvector.NewVector()` で書き、`pgvector.Vector` にスキャンして `.Slice()` で取り出す。
```go
// SOURCE: backend/internal/persona/store.go:71-77
if _, err := tx.Exec(ctx, `
	INSERT INTO persona_interests (persona_id, topic, weight, embedding)
	VALUES ($1, $2, $3, $4)
`, id, in.Topic, in.Weight, pgvector.NewVector(in.Embedding)); err != nil {
	return Persona{}, fmt.Errorf("persona: insert interest: %w", err)
}
```
```go
// SOURCE: backend/internal/retrieval/search.go:229-235
var vec pgvector.Vector
if err := rows.Scan(..., &vec, &distance); err != nil {
	return nil, fmt.Errorf("search: scan row: %w", err)
}
c.Embedding = vec.Slice()
```

### SERVICE_PATTERN
依存はコンストラクタ引数で受け、パッケージ内で定義した**狭いインターフェース**（seam）に
格納してテスト可能にする。具体型はコンストラクタのシグネチャに出す。
```go
// SOURCE: backend/internal/retrieval/search.go:37-56
// queryEmbedder and crossEncoder are narrow seams over Embedder and Reranker
// so tests can drive the blend/threshold logic without a live TEI server.
type queryEmbedder interface {
	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
}

type Searcher struct {
	pool     *pgxpool.Pool
	embedder queryEmbedder
	reranker crossEncoder
	cfg      config.RetrievalConfig
}

func NewSearcher(pool *pgxpool.Pool, embedder *Embedder, reranker *Reranker, cfg config.RetrievalConfig) *Searcher {
	return &Searcher{pool: pool, embedder: embedder, reranker: reranker, cfg: cfg}
}
```

### CONFIG_PATTERN
```go
// SOURCE: backend/internal/config/config.go:83-90, 118-123
EmbedURL:  env("EMBED_URL", "http://localhost:8081"),
LLM: LLMConfig{
	BaseURL: env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
	APIKey:  env("DEEPSEEK_API_KEY", ""),
	Model:   env("CHAT_MODEL", "deepseek-flash"),
},

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
```
ヘルパーは `env` / `envInt` / `envFloat` / `envBool` / `splitAndTrim` の 5 つ。**パース失敗は
fallback に落とす（エラーにしない）**。起動を止めたい条件だけ `Validate()` に書く。

### TEST_STRUCTURE
標準 `testing` のみ（testify なし）。`t.Errorf("X = %v, want %v", got, want)` 形式。
フェイクは同パッケージ内の小さな構造体。HTTP 依存は `httptest.NewServer` + `t.Cleanup(srv.Close)`。
統合テストは env が無ければ `t.Skip`。テーブル駆動には `t.Run`。
```go
// SOURCE: backend/internal/retrieval/search_test.go:17-32
// fakeReranker returns a fixed relevance score for every text, regardless of
// content, so tests control the input to the threshold/blend math precisely.
type fakeReranker struct {
	score float32
}

func (f fakeReranker) Rerank(_ context.Context, _ string, texts []string) ([]RerankResult, error) {
	out := make([]RerankResult, len(texts))
	for i := range texts {
		out[i] = RerankResult{Index: i, Score: f.score}
	}
	return out, nil
}
```
```go
// SOURCE: backend/internal/retrieval/search_test.go:134-142
dsn := os.Getenv("TEST_DATABASE_URL")
if dsn == "" {
	dsn = os.Getenv("DATABASE_URL")
}
if dsn == "" {
	t.Skip("TEST_DATABASE_URL/DATABASE_URL not set; skipping integration test")
}
```

### MIGRATION_PATTERN
`internal/db/migrations/NNNN_name.sql`。`go:embed migrations/*.sql` でファイル名順に 1 ファイル
1 トランザクションで適用され、`schema_migrations` に記録される。**適用済みのファイルは編集しない**
（`0001_init.sql` は触らず `0002` を足す）。SQL には「なぜ」を書いたコメントを付ける。
```sql
-- SOURCE: backend/internal/db/migrations/0001_init.sql:54-56
-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the index.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `backend/internal/db/migrations/0002_openai_embeddings.sql` | CREATE | `vector(768)` → `vector(1536)`、HNSW 再構築、古いベクトル行の削除 |
| `backend/internal/chat/openai.go` | CREATE | `deepseek.go` の置き換え（`OpenAIClient`） |
| `backend/internal/retrieval/embed.go` | UPDATE | TEI HTTP クライアント → go-openai `CreateEmbeddings`。プレフィックス削除、`Dimensions = 1536` |
| `backend/internal/retrieval/search.go` | UPDATE | `crossEncoder`/`reranker` 除去、`Relevance = 1 - distance`、`rerankAndBlend` → `blend` |
| `backend/internal/retrieval/rerank.go` | DELETE | OpenAI に rerank エンドポイントが無い |
| `backend/internal/retrieval/rerank_test.go` | DELETE | 対象コードが消える |
| `backend/internal/retrieval/embed_test.go` | UPDATE | プレフィックステスト削除、OpenAI レスポンス形のフェイクサーバに変更 |
| `backend/internal/retrieval/search_test.go` | UPDATE | `fakeReranker` 廃止。`blend` の新シグネチャに合わせる。統合テストの TEI env を削除 |
| `backend/internal/config/config.go` | UPDATE | `EmbedURL`/`RerankURL` 削除、`Embed EmbedConfig` 追加、`OPENAI_*` 化、`Validate()` 更新、`RelevanceFloor`/`SkepticismSpan` 追加 |
| `backend/internal/chat/deepseek.go` | DELETE | `openai.go` に置換 |
| `backend/internal/persona/store.go` | UPDATE | `ReplaceInterests` を追加（seed の修復用） |
| `backend/internal/api/api.go` | UPDATE | `handleHealth` の TEI プローブを Postgres ping に、`Deps.Pool` 追加 |
| `backend/internal/api/api_test.go` | UPDATE | `testConfig()` から `EmbedURL`/`RerankURL` を除去、`TestHealth` のコメント更新 |
| `backend/cmd/server/main.go` | UPDATE | `NewReranker` 削除、`NewOpenAIClient`、`NewEmbedder(cfg.Embed)`、`Deps.Pool` |
| `backend/cmd/seed/main.go` | UPDATE | `NewEmbedder(cfg.Embed)`、`seedPersonas` が既存ペルソナの interests を修復 |
| `docker-compose.yml` | UPDATE | `tei-embed` / `tei-rerank` / `hf-cache` volume を削除 |
| `Makefile` | UPDATE | `up` の healthy 待ちから TEI 2 サービスを除去、コメント更新 |
| `.env.example` | UPDATE | `EMBED_URL`/`RERANK_URL`/`DEEPSEEK_*` → `OPENAI_*`/`EMBED_MODEL`/閾値 2 つ |
| `README.md` | UPDATE | アーキ図、環境変数表、前提条件、health レスポンス例、ディレクトリ説明 |

## NOT Building

- **プロバイダ切り替え機構**（`LLM_PROVIDER=openai|deepseek`）。DeepSeek は完全に削除する。
- **Cohere / Voyage などの外部 rerank API 連携**。判断 #1 で却下済み。
- **LLM ベースのリランカー**。判断 #1 で却下済み。
- **埋め込み結果のキャッシュ層**。API コストは気になるが今回のスコープ外。
- **リトライ / 指数バックオフ / レートリミット対応**。既存コードにも無い。429 はそのままエラーで上がる。
- **`persona_interests` / `chunks` のベクトル再計算マイグレーション**（旧 768 次元を 1536 に変換する方法は存在しない）。行は削除し `make seed` で作り直す。
- **フロントエンドの変更**。`relevance` / `affinity` は数値としてそのまま流れるだけ。
- **本番向けの secret 管理**。`.env` のままで良い。
- **`chunkSize` / `chunkOverlap` のチューニング**。OpenAI の 8192 トークン上限に対して余裕があるので触らない。

---

## Step-by-Step Tasks

### Task 1: マイグレーション 0002 で埋め込み列を 1536 次元に張り替える
- **ACTION**: `backend/internal/db/migrations/0002_openai_embeddings.sql` を新規作成する。`0001_init.sql` は**編集しない**。
- **IMPLEMENT**:
  ```sql
  -- Switching from ruri-v3-310m (768d, self-hosted TEI) to OpenAI
  -- text-embedding-3-small (1536d). The two live in unrelated vector spaces,
  -- so stored vectors cannot be converted — the rows are dropped and
  -- recreated by `go run ./cmd/seed`.
  --
  -- chunks is emptied first because ADD COLUMN ... NOT NULL without a default
  -- fails on a non-empty table. Deleting chunks cascades to message_citations
  -- (ON DELETE CASCADE), so existing conversations keep their message text
  -- but lose their citation rows. That is acceptable for dev data.
  DELETE FROM chunks;
  DELETE FROM persona_interests;

  -- DROP COLUMN also drops chunks_embedding_hnsw, which depends on it.
  ALTER TABLE chunks DROP COLUMN embedding;
  ALTER TABLE chunks ADD COLUMN embedding vector(1536) NOT NULL;

  -- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the
  -- index. search.go now also reads that distance as the relevance score.
  CREATE INDEX chunks_embedding_hnsw
      ON chunks USING hnsw (embedding vector_cosine_ops);

  ALTER TABLE persona_interests DROP COLUMN embedding;
  ALTER TABLE persona_interests ADD COLUMN embedding vector(1536) NOT NULL;
  ```
- **MIRROR**: MIGRATION_PATTERN（ファイル名連番 + 「なぜ」コメント）
- **IMPORTS**: なし（SQL）
- **GOTCHA**: `personas` 行は**残す**。`persona_interests` だけ消えるので、`seedPersonas` が
  「名前が既に存在 → skip」で早期 return すると **interests がゼロのペルソナが残り、affinity が
  常に 0 になって persona-weighted ランキングが無言で plain RAG に退化する**。Task 8 でこれを直す。
- **GOTCHA**: `conversations` は `personas(id)` を `ON DELETE` 句なしで参照している（=RESTRICT）。
  `DELETE FROM personas` は会話が 1 件でもあると失敗するので、personas は消さない。
- **VALIDATE**:
  ```bash
  cd backend && go run ./cmd/server   # 起動時にマイグレーションが走る
  # ログに `migration applied version=0002_openai_embeddings.sql` が出ること
  psql "$DATABASE_URL" -c "\d chunks" | grep embedding   # vector(1536) であること
  psql "$DATABASE_URL" -c "\d persona_interests" | grep embedding
  ```

### Task 2: config を OPENAI_* に切り替え、埋め込み設定と閾値パラメータを追加する
- **ACTION**: `backend/internal/config/config.go` を更新する。
- **IMPLEMENT**:
  1. `Config` から `EmbedURL string` と `RerankURL string` を**削除**し、代わりに `Embed EmbedConfig` を追加。
     コメントも差し替える:
     ```go
     // Embed points the OpenAI embeddings client at a model whose output width
     // must match vector(1536) in SQL — see retrieval.Dimensions.
     Embed EmbedConfig

     // LLM is the OpenAI chat client. Embed and LLM carry the same BaseURL and
     // APIKey by default; they stay separate structs so each client constructor
     // takes only what it needs.
     LLM LLMConfig
     ```
  2. `EmbedConfig` を新規追加（`LLMConfig` と同じ形）:
     ```go
     // EmbedConfig targets the OpenAI embeddings API. Model must be a
     // text-embedding-3 family model: the request always sends the dimensions
     // parameter, which older models (ada-002) reject.
     type EmbedConfig struct {
     	BaseURL string
     	APIKey  string
     	Model   string
     }
     ```
  3. `LLMConfig` のコメントを DeepSeek 前提から書き換える:
     ```go
     // LLMConfig targets OpenAI's chat completions API. BaseURL stays
     // configurable so an OpenAI-compatible gateway can be swapped in without
     // a rebuild.
     ```
  4. `RetrievalConfig` に 2 フィールド追加:
     ```go
     // RelevanceFloor and SkepticismSpan define the minimum cosine similarity a
     // chunk needs before it can be cited: floor + span*Skepticism. They are
     // tunable because the right values depend on the embedding model's score
     // distribution over the corpus, which only measurement settles — the
     // defaults are calibrated for text-embedding-3-small over Japanese prose.
     RelevanceFloor float64
     SkepticismSpan float64
     ```
  5. `Load()`:
     ```go
     Embed: EmbedConfig{
     	BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
     	APIKey:  env("OPENAI_API_KEY", ""),
     	Model:   env("EMBED_MODEL", "text-embedding-3-small"),
     },
     LLM: LLMConfig{
     	BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
     	APIKey:  env("OPENAI_API_KEY", ""),
     	Model:   env("CHAT_MODEL", "gpt-5-mini"),
     },
     Retrieval: RetrievalConfig{
     	CandidateK:       envInt("CANDIDATE_K", 40),
     	FinalN:           envInt("FINAL_N", 8),
     	PersonaInfluence: envFloat("PERSONA_INFLUENCE", 0.25),
     	ContextPadBytes:  envInt("CONTEXT_PAD_BYTES", 1200),
     	RelevanceFloor:   envFloat("RELEVANCE_FLOOR", 0.25),
     	SkepticismSpan:   envFloat("RELEVANCE_SKEPTICISM_SPAN", 0.20),
     },
     ```
     `EmbedURL` / `RerankURL` の行は削除。
  6. `Validate()`:
     ```go
     if c.LLM.APIKey == "" {
     	return fmt.Errorf("OPENAI_API_KEY is required")
     }
     ```
     さらに閾値の範囲チェックを既存の様式で追加:
     ```go
     if c.Retrieval.RelevanceFloor < 0 || c.Retrieval.RelevanceFloor > 1 {
     	return fmt.Errorf("RELEVANCE_FLOOR must be within 0..1, got %v",
     		c.Retrieval.RelevanceFloor)
     }
     ```
- **MIRROR**: CONFIG_PATTERN（`env()` ヘルパー、パース失敗は fallback、起動を止める条件だけ `Validate()`）
- **IMPORTS**: 変更なし（`fmt`, `os`, `strconv`, `strings`）
- **GOTCHA**: `OPENAI_BASE_URL` の既定値に **`/v1` を含める**こと。go-openai の `DefaultConfig` は
  `https://api.openai.com/v1` を設定するので、`BaseURL` を上書きするならこちらも `/v1` 付きでないと
  404 になる（DeepSeek は `/v1` なしで動いていたので、ここは挙動が変わる）。
- **VALIDATE**: `cd backend && go build ./internal/config` → コンパイルが通る（他パッケージはまだ壊れている状態で正常）

### Task 3: Embedder を OpenAI Embeddings API に置き換える
- **ACTION**: `backend/internal/retrieval/embed.go` を全面的に書き換える。
- **IMPLEMENT**:
  ```go
  // Package retrieval turns a user utterance plus a persona into ranked knowledge.
  package retrieval

  import (
  	"context"
  	"fmt"
  	"strings"

  	openai "github.com/sashabaranov/go-openai"

  	"github.com/shun/kaigi/backend/internal/config"
  )

  // Dimensions is text-embedding-3-small's native output size and must match
  // vector(1536) in SQL. The request sends it explicitly rather than relying on
  // the model default, so a model swap that changes the width fails loudly at
  // the response check below instead of at the INSERT.
  const Dimensions = 1536

  // maxBatch keeps each request well inside OpenAI's per-request limits (2048
  // inputs, 300k tokens). Seed chunks are ~800 runes, so 128 of them is roughly
  // 150k tokens — comfortable, and small enough that one failure retries little.
  const maxBatch = 128

  type Embedder struct {
  	client *openai.Client
  	model  string
  }

  func NewEmbedder(cfg config.EmbedConfig) *Embedder {
  	oaCfg := openai.DefaultConfig(cfg.APIKey)
  	oaCfg.BaseURL = cfg.BaseURL
  	return &Embedder{client: openai.NewClientWithConfig(oaCfg), model: cfg.Model}
  }

  // EmbedQueries and EmbedDocuments are identical calls: unlike ruri-v3, which
  // was prefix-conditioned, OpenAI embeddings place queries and passages in one
  // space with no side-specific prefix. Both methods stay because they are the
  // seams persona.Store and Searcher depend on, and because keeping the call
  // sites honest about which side they are embedding costs nothing.
  func (e *Embedder) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
  	return e.embed(ctx, texts)
  }

  func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
  	return e.embed(ctx, texts)
  }

  func (e *Embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
  	if len(texts) == 0 {
  		return nil, nil
  	}

  	out := make([][]float32, 0, len(texts))
  	for start := 0; start < len(texts); start += maxBatch {
  		end := min(start+maxBatch, len(texts))
  		batch, err := e.embedBatch(ctx, texts[start:end])
  		if err != nil {
  			return nil, err
  		}
  		out = append(out, batch...)
  	}
  	return out, nil
  }

  func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
  	// OpenAI rejects an empty string with a 400 rather than returning a zero
  	// vector, and a blank chunk carries no signal anyway, so substitute a
  	// single space to keep the response indexes aligned with the input slice.
  	inputs := make([]string, len(texts))
  	for i, t := range texts {
  		if strings.TrimSpace(t) == "" {
  			inputs[i] = " "
  			continue
  		}
  		inputs[i] = t
  	}

  	resp, err := e.client.CreateEmbeddings(ctx, openai.EmbeddingRequest{
  		Input:      inputs,
  		Model:      openai.EmbeddingModel(e.model),
  		Dimensions: Dimensions,
  	})
  	if err != nil {
  		return nil, fmt.Errorf("embed: create embeddings: %w", err)
  	}
  	if len(resp.Data) != len(inputs) {
  		return nil, fmt.Errorf("embed: got %d embeddings for %d inputs", len(resp.Data), len(inputs))
  	}

  	// Place by Index rather than trusting response order: index is what the
  	// API documents as authoritative, and a silent misalignment here would
  	// attach every chunk's vector to the wrong text.
  	vectors := make([][]float32, len(inputs))
  	for _, d := range resp.Data {
  		if d.Index < 0 || d.Index >= len(inputs) {
  			return nil, fmt.Errorf("embed: response index %d out of range for %d inputs", d.Index, len(inputs))
  		}
  		if len(d.Embedding) != Dimensions {
  			return nil, fmt.Errorf("embed: vector %d has %d dimensions, want %d",
  				d.Index, len(d.Embedding), Dimensions)
  		}
  		vectors[d.Index] = d.Embedding
  	}
  	for i, v := range vectors {
  		if v == nil {
  			return nil, fmt.Errorf("embed: response missing index %d", i)
  		}
  	}
  	return vectors, nil
  }
  ```
- **MIRROR**: SERVICE_PATTERN（コンストラクタで config を受ける）、ERROR_HANDLING（`embed: <操作>: %w`）
- **IMPORTS**: `context`, `fmt`, `strings`, `openai "github.com/sashabaranov/go-openai"`, `.../internal/config`。
  **削除**されるもの: `bytes`, `encoding/json`, `net/http`, `time`。
- **GOTCHA**: `prefixQuery` / `prefixDocument` 定数と `embedRequest` 型は削除する。`embed_test.go` が
  `embedRequest` を参照しているので Task 6 まで `go test ./internal/retrieval` は通らない。
- **GOTCHA**: `Normalize` / `Truncate` に相当する概念は無い。OpenAI は正規化済みベクトルを返すので
  コサインはそのまま使える。切り詰めは無いので 8192 トークン超は 400 になる。
- **GOTCHA**: go-openai の `EmbeddingRequest.Dimensions` は `omitempty` なので 0 だと送信されない。
  `Dimensions` 定数（1536）を明示的に渡すこと。
- **VALIDATE**: `cd backend && go build ./internal/retrieval` は Task 4 まで通らない。`go vet` で
  未使用 import が無いことだけ確認する。

### Task 4: リランカーを削除し、コサイン類似度を Relevance にする
- **ACTION**: `backend/internal/retrieval/rerank.go` を削除し、`search.go` を更新する。
- **IMPLEMENT**:
  1. `rm backend/internal/retrieval/rerank.go backend/internal/retrieval/rerank_test.go`
  2. `search.go` の seam コメントとインターフェースを差し替える:
     ```go
     // queryEmbedder is the narrow seam over Embedder so tests can drive the
     // blend/threshold logic without a live embeddings API.
     type queryEmbedder interface {
     	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
     }
     ```
     `crossEncoder` インターフェースは**削除**。
  3. `Searcher` から `reranker` フィールドを削除し、コンストラクタも縮める:
     ```go
     type Searcher struct {
     	pool     *pgxpool.Pool
     	embedder queryEmbedder
     	cfg      config.RetrievalConfig
     }

     func NewSearcher(pool *pgxpool.Pool, embedder *Embedder, cfg config.RetrievalConfig) *Searcher {
     	return &Searcher{pool: pool, embedder: embedder, cfg: cfg}
     }
     ```
  4. `Candidate.Relevance` のコメントを更新:
     ```go
     Relevance float32 // cosine similarity to the nearest query, 0..1 in practice
     ```
  5. `Search` 内のループで、最良距離を記録すると同時に `Relevance` を埋める。
     **`<=>` はコサイン距離なので類似度は `1 - distance`**:
     ```go
     for _, row := range rows {
     	if prev, ok := bestDistance[row.candidate.ChunkID]; !ok || row.distance < prev {
     		bestDistance[row.candidate.ChunkID] = row.distance
     		c := row.candidate
     		// pgvector's <=> is cosine distance (1 - similarity). With a
     		// cross-encoder gone, this similarity is the only relevance signal
     		// left, so it is what the skepticism threshold now filters on.
     		c.Relevance = float32(1 - row.distance)
     		byChunk[row.candidate.ChunkID] = c
     	}
     }
     ```
  6. `Search` の末尾を差し替え（`queries[0]` は不要になる）:
     ```go
     return blend(p, s.cfg, candidates), nil
     ```
  7. `rerankAndBlend` を `blend` にリネームし、`ctx` / `reranker` / `primaryQuery` 引数と
     `Rerank` 呼び出しブロックを削除、閾値を config 由来にする。エラーを返す理由が無くなるので
     戻り値は `[]Candidate` 一本にする:
     ```go
     // blend applies the persona weighting to already-retrieved candidates.
     // Split out from Search so the blend and threshold math — the core of what
     // makes this persona-aware — can be unit tested without a live Postgres.
     func blend(p persona.Persona, cfg config.RetrievalConfig, candidates []Candidate) []Candidate {
     	if len(candidates) == 0 {
     		return nil
     	}

     	// A more skeptical persona demands stronger relevance before citing
     	// anything — the threshold rises with Skepticism rather than the other
     	// way around, so a credulous persona (0) still filters obvious noise.
     	threshold := float32(cfg.RelevanceFloor + cfg.SkepticismSpan*float64(p.Personality.Skepticism))
     	filtered := candidates[:0]
     	for _, c := range candidates {
     		if c.Relevance >= threshold {
     			filtered = append(filtered, c)
     		}
     	}
     	candidates = filtered
     	if len(candidates) == 0 {
     		return nil
     	}

     	for i, c := range candidates {
     		c.Affinity = affinity(c.Embedding, p.Personality.Interests)
     		// Affinity is -1..1 and Relevance is roughly 0..1; blending them
     		// unscaled would make PersonaInfluence's meaning depend on the
     		// interest weights in play, defeating it as a single dial.
     		affinity01 := (c.Affinity + 1) / 2
     		lambda := float32(cfg.PersonaInfluence)
     		c.Final = (1-lambda)*c.Relevance + lambda*affinity01
     		candidates[i] = c
     	}

     	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Final > candidates[j].Final })

     	if n := cfg.FinalN; n > 0 && len(candidates) > n {
     		candidates = candidates[:n]
     	}
     	return candidates
     }
     ```
- **MIRROR**: `search.go` の既存コメントスタイル（「なぜ」を説明する段落コメント）をそのまま維持する
- **IMPORTS**: `search.go` から削除するものは無い（`context` は `Search` と `vectorSearch` がまだ使う）
- **GOTCHA**: `filtered := candidates[:0]` は入力スライスを破壊的に再利用する既存イディオム。
  `Search` からは新規スライスを渡しているので安全。テストからも使い捨てスライスを渡すこと。
- **GOTCHA**: `Search` の `if len(byChunk) == 0 { return nil, nil }` はそのまま残す。
- **GOTCHA**: `blend` がエラーを返さなくなるので、`Search` の戻り値は `return blend(...), nil`。
- **VALIDATE**: `cd backend && go build ./internal/retrieval` → 通る。`go vet ./internal/retrieval` → クリーン。

### Task 5: チャットクライアントを OpenAI に置き換える
- **ACTION**: `backend/internal/chat/deepseek.go` を削除し、`backend/internal/chat/openai.go` を作成する。
- **IMPLEMENT**: `deepseek.go` の内容をそのまま移植し、以下だけ変える:
  - `DeepSeekClient` → `OpenAIClient`、`NewDeepSeekClient` → `NewOpenAIClient`、`deepSeekStream` → `openAIStream`
  - エラー文字列の接頭辞 `deepseek:` → `openai:`（4 箇所: create chat completion / no choices / create stream / stream recv）
  - 型コメントを書き換える:
    ```go
    // OpenAIClient adapts go-openai to LLMClient. Every provider-specific
    // quirk is meant to live in this file, not in the engine.
    type OpenAIClient struct {
    	client *openai.Client
    }
    ```
  - `toOpenAIRequest` と `ChatCompletionResponseFormatTypeJSONObject` の扱いは**変更しない**
- **MIRROR**: NAMING_CONVENTION、ERROR_HANDLING（上記の `deepseek.go` スニペット）
- **IMPORTS**: 変更なし（`context`, `errors`, `fmt`, `io`, `openai`, `.../internal/config`）
- **GOTCHA**: `internal/chat/llm.go` のコメントに「DeepSeek」が 2 箇所ある（L47-49 の `LLMClient` 説明）。
  そこも「OpenAI」に直す。`llm.go` のコードは変更不要。
- **GOTCHA**: `prompt.go` L14-17（`BuildSystemPrompt` のキャッシュに関するコメント）と L62-65、
  `engine.go` L117-120 にも「DeepSeek」への言及がある。OpenAI も prefix caching を自動で行うので
  **前提は成立したまま**。文中の "DeepSeek" を "OpenAI" に置換するだけでロジックは触らない。
- **GOTCHA**: `api.go` L19-22 の `chatEngine` コメントにも "live DeepSeek/Postgres/MinIO stack" がある。
- **VALIDATE**:
  ```bash
  cd backend && go build ./internal/chat
  grep -rin deepseek --include='*.go' . ; # 何も出ないこと
  ```

### Task 6: 埋め込みテストを OpenAI のレスポンス形に合わせる
- **ACTION**: `backend/internal/retrieval/embed_test.go` を書き換える。
- **IMPLEMENT**:
  - `fakeEmbedServer` を OpenAI の `/embeddings` レスポンス形を返すものに変える。`config.EmbedConfig`
    の `BaseURL` に `httptest` サーバの URL を渡す（go-openai は `BaseURL + "/embeddings"` を叩く）。
    ```go
    type capturedEmbedRequest struct {
    	Input      []string `json:"input"`
    	Model      string   `json:"model"`
    	Dimensions int      `json:"dimensions"`
    }

    // fakeEmbedServer stands in for OpenAI's /embeddings endpoint. It returns
    // vectors out of input order, with explicit indexes, because that is the
    // one property of the real API the client must not assume away.
    func fakeEmbedServer(t *testing.T, dims int) (*httptest.Server, *capturedEmbedRequest) {
    	t.Helper()
    	captured := &capturedEmbedRequest{}
    	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
    			t.Fatalf("decode request: %v", err)
    		}
    		data := make([]openai.Embedding, len(captured.Input))
    		for i := range data {
    			// Reverse order, so a client that ignores Index and appends in
    			// arrival order fails this test.
    			idx := len(captured.Input) - 1 - i
    			data[i] = openai.Embedding{
    				Index:     idx,
    				Embedding: marker(dims, idx),
    			}
    		}
    		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Data: data})
    	}))
    	t.Cleanup(srv.Close)
    	return srv, captured
    }

    // marker builds a vector whose first element identifies which input it
    // belongs to, so tests can assert the client reassembled them in order.
    func marker(dims, id int) []float32 {
    	v := make([]float32, dims)
    	if dims > 0 {
    		v[0] = float32(id)
    	}
    	return v
    }

    func newTestEmbedder(baseURL string) *Embedder {
    	return NewEmbedder(config.EmbedConfig{
    		BaseURL: baseURL,
    		APIKey:  "test-key",
    		Model:   "text-embedding-3-small",
    	})
    }
    ```
  - **削除**するテスト: `TestEmbedderAppliesQueryPrefix`, `TestEmbedderAppliesDocumentPrefix`
    （プレフィックスという概念が無くなった）
  - **残す / 書き換える**テスト:
    - `TestEmbedderSendsNoPrefix`（新規）: `EmbedQueries` と `EmbedDocuments` の両方で
      `captured.Input` が入力文字列そのままであること（`検索クエリ:` を含まない）。
    - `TestEmbedderRequestsConfiguredDimensions`（新規）: `captured.Dimensions == Dimensions` かつ
      `captured.Model == "text-embedding-3-small"`。
    - `TestEmbedderRejectsWrongDimensions`（流用）: `fakeEmbedServer(t, 512)` でエラー、
      メッセージに `"512"` を含む。
    - `TestEmbedderReordersByResponseIndex`（新規）: 3 件入力し、`got[i][0] == float32(i)` を確認。
    - `TestEmbedderBatchesLargeInputs`（流用）: `maxBatch+1` 件で `calls == 2`。
      **`maxBatch` が 32 → 128 になったので、テストは定数を参照し続けること**（ハードコードしない）。
    - `TestEmbedderSubstitutesEmptyInput`（新規）: `[]string{"", "x"}` を渡し、
      `captured.Input[0] == " "` であること。
    - `TestEmbedderEmptyInputSlice`（新規）: `EmbedQueries(ctx, nil)` が `(nil, nil)`、
      HTTP 呼び出しが 0 回。
  - `equalStrings` ヘルパーは残す（新テストでも使う）。
- **MIRROR**: TEST_STRUCTURE（`httptest.NewServer` + `t.Cleanup`、`t.Errorf("X = %v, want %v")`）
- **IMPORTS**: `context`, `encoding/json`, `net/http`, `net/http/httptest`, `strings`, `testing`,
  `openai "github.com/sashabaranov/go-openai"`, `.../internal/config`
- **GOTCHA**: `openai.EmbeddingResponse` は埋め込みフィールド `httpHeader`（非公開）を持つが、
  `json.Marshal` は非公開フィールドを無視するのでテストからの直接構築で問題ない。
- **GOTCHA**: go-openai は `BaseURL` の末尾に `/embeddings` を足す。`httptest` サーバの URL は
  末尾スラッシュ無しなのでそのまま渡せばよい（`/v1` は不要）。
- **VALIDATE**: `cd backend && go test ./internal/retrieval -run TestEmbedder -v` → 全て PASS

### Task 7: search テストを reranker 抜きに書き換える
- **ACTION**: `backend/internal/retrieval/search_test.go` を更新する。
- **IMPLEMENT**:
  - `fakeReranker` 型を**削除**。代わりに `Relevance` を直接セットするヘルパーにする:
    ```go
    // candidateWithRelevance builds a candidate the way Search does now: the
    // relevance score is the cosine similarity already attached by the vector
    // query, so blend takes it as given rather than computing it.
    func candidateWithRelevance(relevance float32, vec []float32) Candidate {
    	return Candidate{ChunkID: uuid.New(), Content: "x", Embedding: vec, Relevance: relevance}
    }
    ```
    既存の `candidateWithEmbedding` は使われなくなるので削除。
  - `blendConfig` ヘルパーを足して閾値の既定値をテストに持ち込む（`config.Load()` を使わない）:
    ```go
    func blendConfig(finalN int, lambda float64) config.RetrievalConfig {
    	return config.RetrievalConfig{
    		FinalN:           finalN,
    		PersonaInfluence: lambda,
    		RelevanceFloor:   0.25,
    		SkepticismSpan:   0.20,
    	}
    }
    ```
  - `TestSkepticismRaisesThreshold`: 閾値が `0.25 + 0.20*S` になったので**スコアを差し替える**。
    `Relevance = 0.30` は credulous（閾値 0.25）を通り skeptical（閾値 0.45）で切られる:
    ```go
    candidates := []Candidate{candidateWithRelevance(0.30, nil)}
    credulous := persona.Persona{Personality: persona.Personality{Skepticism: 0}}
    got := blend(credulous, blendConfig(100, 0), []Candidate{candidateWithRelevance(0.30, nil)})
    // len(got) == 1
    skeptical := persona.Persona{Personality: persona.Personality{Skepticism: 1}}
    got = blend(skeptical, blendConfig(100, 0), []Candidate{candidateWithRelevance(0.30, nil)})
    // len(got) == 0
    ```
    コメントも「Score 0.30 clears a credulous persona's floor (0.25) but not a skeptical one's
    (0.25 + 0.20*1.0 = 0.45).」に更新する。
  - `TestPersonaInfluenceBlend`: `fakeReranker{score: 0.7}` を `candidateWithRelevance(0.7, []float32{1,0})`
    に置換。期待値（0.7 / 1.0 / 0.85）は**変わらない**。`blend` はエラーを返さないので
    `got, err := ...` → `got := ...` に直す。
  - `TestAffinityWithNoInterests`: 同様に `Relevance = 0.7` を直接セット。期待 `Final == 0.6` は不変。
  - `TestRerankAndBlendEmptyInput` → `TestBlendEmptyInput` にリネーム、`blend(persona.Persona{}, blendConfig(10, 0), nil)` が `nil`。
  - `TestSearchAgainstPostgres`: TEI の env 依存を削除する。
    ```go
    // 削除: embedURL := envOrSkip(t, "TEST_EMBED_URL", "http://localhost:8081")
    // 削除: rerankURL := envOrSkip(t, "TEST_RERANK_URL", "http://localhost:8082")
    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
    	t.Skip("OPENAI_API_KEY not set; skipping integration test")
    }
    ...
    embedder := NewEmbedder(config.EmbedConfig{
    	BaseURL: "https://api.openai.com/v1",
    	APIKey:  apiKey,
    	Model:   "text-embedding-3-small",
    })
    cfg := config.RetrievalConfig{
    	CandidateK: 10, FinalN: 5, PersonaInfluence: 0.6,
    	RelevanceFloor: 0.25, SkepticismSpan: 0.20,
    }
    searcher := NewSearcher(pool, embedder, cfg)
    ```
    doc コメントも「runs against the docker-compose stack (Postgres + live TEI embed/rerank)」から
    「runs against Postgres seeded by `go run ./cmd/seed`, using the live OpenAI embeddings API」に更新。
  - `envOrSkip` ヘルパーが他で使われていなければ削除する（`grep -n envOrSkip` で確認）。
- **MIRROR**: TEST_STRUCTURE（テーブル駆動 + `t.Run`、統合テストは env 無しで `t.Skip`）
- **IMPORTS**: `os` は統合テストでまだ使う。`strings` も `titleOrder` で使う。変更は `config` の
  追加参照のみ（既にインポート済み）。
- **GOTCHA**: `blend` はスライスを破壊的に再利用する（`candidates[:0]`）。サブテストごとに
  新しいスライスリテラルを渡すこと。ループ外で作った 1 本を共有すると後続サブテストが汚染される。
- **GOTCHA**: `TestSearchAgainstPostgres` は実際に OpenAI を課金付きで叩く。CI で回すなら
  `OPENAI_API_KEY` を設定しないことで skip される設計を維持する。
- **VALIDATE**: `cd backend && go test ./internal/retrieval -v` → ユニットテスト全 PASS、
  統合テストは `OPENAI_API_KEY` 未設定なら SKIP

### Task 8: seed が既存ペルソナの interests を修復できるようにする
- **ACTION**: `backend/internal/persona/store.go` に `ReplaceInterests` を追加し、
  `backend/cmd/seed/main.go` の `seedPersonas` から呼ぶ。
- **IMPLEMENT**:
  1. `store.go` に追加（`Update` の直後、`interests` の直前）:
     ```go
     // ReplaceInterests re-embeds and rewrites a persona's interest axes. It
     // exists for the embedding-model migration path: migration 0002 empties
     // persona_interests because 768-dimension vectors cannot be converted to
     // 1536, and without this the seeder — which skips personas that already
     // exist by name — would leave them with no interests at all, silently
     // degenerating the persona-weighted rerank into plain RAG.
     func (s *Store) ReplaceInterests(ctx context.Context, id uuid.UUID, in []InterestInput) error {
     	interests, err := s.embedInterests(ctx, in)
     	if err != nil {
     		return err
     	}

     	tx, err := s.pool.Begin(ctx)
     	if err != nil {
     		return fmt.Errorf("persona: begin: %w", err)
     	}
     	defer func() { _ = tx.Rollback(ctx) }()

     	if _, err := tx.Exec(ctx,
     		`DELETE FROM persona_interests WHERE persona_id = $1`, id,
     	); err != nil {
     		return fmt.Errorf("persona: delete interests: %w", err)
     	}
     	for _, it := range interests {
     		if _, err := tx.Exec(ctx, `
     			INSERT INTO persona_interests (persona_id, topic, weight, embedding)
     			VALUES ($1, $2, $3, $4)
     		`, id, it.Topic, it.Weight, pgvector.NewVector(it.Embedding)); err != nil {
     			return fmt.Errorf("persona: insert interest: %w", err)
     		}
     	}
     	if err := tx.Commit(ctx); err != nil {
     		return fmt.Errorf("persona: commit: %w", err)
     	}
     	return nil
     }
     ```
  2. `cmd/seed/main.go` の `seedPersonas` ループを書き換える。`exists bool` の代わりに ID と
     interests 数を取る:
     ```go
     for _, spec := range specs {
     	var id uuid.UUID
     	var interestCount int
     	err := pool.QueryRow(ctx, `
     		SELECT p.id, count(i.id)
     		FROM personas p
     		LEFT JOIN persona_interests i ON i.persona_id = p.id
     		WHERE p.name = $1
     		GROUP BY p.id
     	`, spec.Name).Scan(&id, &interestCount)
     	switch {
     	case errors.Is(err, pgx.ErrNoRows):
     		if _, err := personas.Create(ctx, spec); err != nil {
     			return fmt.Errorf("create persona %s: %w", spec.Name, err)
     		}
     		log.Info("seeded persona", "name", spec.Name)
     	case err != nil:
     		return fmt.Errorf("check persona %s: %w", spec.Name, err)
     	case interestCount == len(spec.Interests):
     		log.Info("persona already seeded, skipping", "name", spec.Name)
     	default:
     		// Interests went missing (migration 0002 empties them when the
     		// embedding width changes). Re-embed rather than skip: a persona
     		// with no interests scores every chunk identically.
     		if err := personas.ReplaceInterests(ctx, id, spec.Interests); err != nil {
     			return fmt.Errorf("replace interests for %s: %w", spec.Name, err)
     		}
     		log.Info("re-embedded persona interests", "name", spec.Name,
     			"had", interestCount, "want", len(spec.Interests))
     	}
     }
     ```
  3. `run()` 内の `embedder := retrieval.NewEmbedder(cfg.EmbedURL)` を
     `embedder := retrieval.NewEmbedder(cfg.Embed)` に変える。
- **MIRROR**: REPOSITORY_PATTERN（tx + `defer Rollback` + `pgvector.NewVector`、`Create` L46-84 と同形）
- **IMPORTS**: `cmd/seed/main.go` に `errors`、`github.com/google/uuid`、`github.com/jackc/pgx/v5` を追加。
  `store.go` は変更なし（`uuid`, `pgvector`, `fmt`, `context` は既にある）。
- **GOTCHA**: `LEFT JOIN` + `GROUP BY` なので、ペルソナが存在して interests が 0 件でも
  1 行（count=0）が返る。ペルソナ自体が無いときだけ `pgx.ErrNoRows`。
- **GOTCHA**: seed は `config.Validate()` を呼んでいない（`run()` は `config.Load()` だけ）。
  `OPENAI_API_KEY` 未設定でも起動し、埋め込み呼び出しで 401 になる。**既存の挙動と同じなので変えない**。
- **VALIDATE**:
  ```bash
  cd backend && go build ./... && go run ./cmd/seed
  # 2 回目の実行で "re-embedded persona interests" か "already seeded" が出て、エラーにならないこと
  psql "$DATABASE_URL" -c "SELECT p.name, count(i.id) FROM personas p LEFT JOIN persona_interests i ON i.persona_id=p.id GROUP BY p.name"
  # 批評家 | 2 / 実務家 | 2 になること
  ```

### Task 9: health チェックを TEI プローブから Postgres ping に差し替える
- **ACTION**: `backend/internal/api/api.go` と `api_test.go` を更新する。
- **IMPLEMENT**:
  1. `Deps` に `Pool *pgxpool.Pool` を追加:
     ```go
     type Deps struct {
     	Log       *slog.Logger
     	Pool      *pgxpool.Pool
     	Personas  *persona.Store
     	Chat      chatEngine
     	ChatStore *chat.Store
     	Objects   *objectstore.Store
     }
     ```
  2. `handleHealth` を書き換える。`check` クロージャと `healthCheckTimeout` の
     コメントから TEI への言及を消す:
     ```go
     // healthCheckTimeout bounds the dependency probe so a stalled database
     // cannot hang the whole health check indefinitely.
     const healthCheckTimeout = 2 * time.Second

     func (h *handlers) handleHealth(w http.ResponseWriter, r *http.Request) {
     	status := map[string]string{"status": "ok"}

     	// A dependency being unreachable makes the response "degraded", not a
     	// 5xx: the process itself is healthy even if a downstream isn't, and a
     	// failing health check must not be what takes the container down.
     	degraded := false

     	// The embedding and chat providers are not probed: both are third-party
     	// HTTP APIs with no free liveness endpoint, and billing a request per
     	// health check to learn something the next real request reports anyway
     	// is not a trade worth making.
     	switch {
     	case h.deps.Pool == nil:
     		status["db"] = "unconfigured"
     		degraded = true
     	default:
     		ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
     		defer cancel()
     		if err := h.deps.Pool.Ping(ctx); err != nil {
     			status["db"] = "unreachable"
     			degraded = true
     		} else {
     			status["db"] = "ok"
     		}
     	}

     	if degraded {
     		status["status"] = "degraded"
     	}
     	writeJSON(w, http.StatusOK, status)
     }
     ```
  3. `chatEngine` の doc コメント（L19-22）の "live DeepSeek/Postgres/MinIO stack" を
     "live OpenAI/Postgres/MinIO stack" に。
  4. `api_test.go` の `testConfig()` から `EmbedURL` / `RerankURL` を削除:
     ```go
     func testConfig() config.Config {
     	return config.Config{
     		Addr:           ":0",
     		AllowedOrigins: []string{"http://localhost:5173"},
     	}
     }
     ```
     `TestHealth` のコメントを更新:
     ```go
     // newTestHandler wires no Pool, so handleHealth reports db as
     // "unconfigured" and the overall status as "degraded". A live probe is
     // exercised manually against the docker-compose stack.
     ```
     期待値 `"degraded"` は**そのまま**通る。
  5. `cmd/server/main.go` の `api.Deps` に `Pool: pool,` を追加。
- **MIRROR**: 既存 `handleHealth` の degraded セマンティクスと `writeJSON` の使い方をそのまま維持
- **IMPORTS**: `api.go` に `"github.com/jackc/pgx/v5/pgxpool"` を追加。`context` / `time` は既にある。
- **GOTCHA**: `Pool == nil` を "unconfigured" + degraded として扱うことで、`api_test.go` の
  既存の `TestHealth` / `TestCORSOnlyEchoesAllowedOrigin` / `TestPreflightReturnsNoContent`
  （いずれも `/api/health` を叩く）が Pool を渡さずに通り続ける。
- **VALIDATE**: `cd backend && go test ./internal/api -v` → 全 PASS

### Task 10: server main を新しいコンストラクタに配線する
- **ACTION**: `backend/cmd/server/main.go` を更新する。
- **IMPLEMENT**:
  ```go
  embedder := retrieval.NewEmbedder(cfg.Embed)
  searcher := retrieval.NewSearcher(pool, embedder, cfg.Retrieval)

  personas := persona.NewStore(pool, embedder)
  chatStore := chat.NewStore(pool)
  llm := chat.NewOpenAIClient(cfg.LLM)
  engine := chat.NewEngine(llm, searcher, objects, chatStore, cfg)

  handler := api.NewHandler(cfg, api.Deps{
  	Log:       log,
  	Pool:      pool,
  	Personas:  personas,
  	Chat:      engine,
  	ChatStore: chatStore,
  	Objects:   objects,
  })
  ```
  `reranker := retrieval.NewReranker(cfg.RerankURL)` の行を削除。
- **MIRROR**: 既存の配線順（pool → objects → embedder → searcher → stores → llm → engine → handler）
- **IMPORTS**: 変更なし
- **GOTCHA**: `run()` 冒頭の `cfg.Validate()` は残す。エラーコメント（「a conversation API with no
  LLM key configured」）もそのまま有効。
- **VALIDATE**: `cd backend && go build ./... && go vet ./...` → クリーン

### Task 11: docker-compose / Makefile から TEI を取り除く
- **ACTION**: `docker-compose.yml` と `Makefile` を更新する。
- **IMPLEMENT**:
  1. `docker-compose.yml`: `tei-embed` サービス、`tei-rerank` サービス、`volumes:` の
     `hf-cache:` を削除。`services:` 上部のコメント（「Development dependencies only...」）はそのまま。
     TEI サービス群の上にあった「TEI serves exactly one model per process...」のブロックコメントも削除。
  2. `Makefile` の `up` ターゲット:
     ```make
     up: ## Start postgres/MinIO via docker compose and wait for them to be healthy
     	docker compose up -d
     	@echo "waiting for dependencies to become healthy..."
     	@until [ "$$(docker compose ps --format '{{.Health}}' postgres minio 2>/dev/null | grep -cv healthy)" = "0" ]; do sleep 5; done
     	@echo "all dependencies healthy"
     ```
     「first run downloads ~2.5GB of models」という文言を削除。
- **MIRROR**: 既存の compose / Makefile のコメントスタイル
- **IMPORTS**: N/A
- **GOTCHA**: `docker compose down -v` でないと古い `hf-cache` ボリュームは残る。compose ファイルから
  定義を消しただけでは削除されないので、README の掃除手順に `docker volume rm kaigi_hf-cache` を書く。
- **VALIDATE**:
  ```bash
  cd /Users/shun/github/kaigi
  docker compose config >/dev/null   # 構文チェック
  docker compose config --services   # postgres / minio / minio-init のみ
  make down && make up               # モデル DL 無しで数秒で healthy になること
  ```

### Task 12: .env.example と README を更新する
- **ACTION**: `.env.example` と `README.md` を更新する。
- **IMPLEMENT**:
  1. `.env.example`: TEI ブロックと DeepSeek ブロックを置換する。
     ```
     # OpenAI (chat completions + embeddings share one key and base URL).
     # Get a key at https://platform.openai.com/api-keys
     # The server refuses to start without this set — see config.Validate().
     OPENAI_API_KEY=
     # Must include the /v1 path segment.
     OPENAI_BASE_URL=https://api.openai.com/v1
     CHAT_MODEL=gpt-5-mini
     # Must be a text-embedding-3 family model: the request always sends the
     # dimensions parameter, and its width must match vector(1536) in SQL.
     EMBED_MODEL=text-embedding-3-small
     ```
     Retrieval tuning ブロックに追記:
     ```
     # A chunk is citable when its cosine similarity to the query clears
     # RELEVANCE_FLOOR + RELEVANCE_SKEPTICISM_SPAN * persona.skepticism.
     # These are empirical — see the plan's calibration step if recall looks
     # too tight or too loose for your corpus.
     RELEVANCE_FLOOR=0.25
     RELEVANCE_SKEPTICISM_SPAN=0.20
     ```
     `EMBED_URL` / `RERANK_URL` / `DEEPSEEK_API_KEY` / `DEEPSEEK_BASE_URL` の行を削除。
  2. `README.md` の更新箇所（行番号は現状のもの）:
     - L14, L19, L28, L30: `DeepSeek` → `OpenAI`
     - L16: `4. リランキング ────▶ TEI /rerank` を
       `4. 性格重み付け ──── (コサイン類似度 + affinity)` のような外部呼び出しなしの表現に変更
     - L37: `postgres+pgvector / MinIO / TEI(embed) / TEI(rerank)` → `postgres+pgvector / MinIO`
     - L48: `TEI クライアント、pgvector 検索、性格重み付きリランキング` →
       `OpenAI 埋め込みクライアント、pgvector 検索、性格重み付きランキング`
     - L50: `DeepSeek ストリーミング` → `OpenAI ストリーミング`
     - L57: `Docker（postgres+pgvector / MinIO / TEI を動かす）` → `Docker（postgres+pgvector / MinIO を動かす）`
     - L58: `DeepSeek の API キー（...）` → `OpenAI の API キー（<https://platform.openai.com/api-keys>）`
     - L64: `DEEPSEEK_API_KEY を設定する` → `OPENAI_API_KEY を設定する`
     - L68: 「初回は ruri-v3 の埋め込み/リランカーモデル(計 ~2.5GB)を…」のコメントを削除
     - L82: health レスポンス例を `{"status":"ok|degraded","db":"ok"}` に
     - L130-134: 環境変数表から `EMBED_URL` / `RERANK_URL` / `DEEPSEEK_API_KEY` / `DEEPSEEK_BASE_URL`
       の行を削除し、`OPENAI_API_KEY`（必須、既定なし）/ `OPENAI_BASE_URL`（`https://api.openai.com/v1`）/
       `CHAT_MODEL`（`gpt-5-mini`）/ `EMBED_MODEL`（`text-embedding-3-small`）/
       `RELEVANCE_FLOOR`（`0.25`）/ `RELEVANCE_SKEPTICISM_SPAN`（`0.20`）を追加
     - TEI から移行する既存環境向けに一節を足す:
       ```markdown
       ### 既存環境からの移行

       埋め込みモデルが変わったのでベクトルの次元も空間も変わる。マイグレーション `0002` が
       `chunks` と `persona_interests` を空にするので、再 seed が必要。

       ```bash
       make down
       docker volume rm kaigi_hf-cache   # TEI のモデルキャッシュ(~2.5GB)を解放
       make up
       cd backend && go run ./cmd/server   # マイグレーション 0002 を適用
       make seed                            # 1536 次元で作り直す
       ```

       既存の会話の本文は残るが、引用（`message_citations`）は `chunks` の削除に連動して消える。
       ```
- **MIRROR**: README の既存の表とコードブロックのスタイル
- **IMPORTS**: N/A
- **GOTCHA**: `grep -rn -i 'tei\|deepseek\|ruri\|768' README.md .env.example Makefile docker-compose.yml`
  で残存がゼロになるまで確認する（`0001_init.sql` の 768 は履歴として残るので対象外）。
- **VALIDATE**: 上記 grep が（`0001_init.sql` 以外で）ヒットしないこと

### Task 13: 依存整理と全体ビルド・テスト
- **ACTION**: `go mod tidy` を走らせて全体を検証する。
- **IMPLEMENT**:
  ```bash
  cd backend
  go mod tidy
  go build ./...
  go vet ./...
  go test ./...
  ```
- **MIRROR**: N/A
- **IMPORTS**: N/A
- **GOTCHA**: `go.mod` の require は全行 `// indirect` になっている。`go mod tidy` で
  `go-openai` / `pgx` / `minio-go` / `pgvector-go` / `uuid` / `golang.org/x/sync` などが
  直接依存ブロックに移る差分が出る。これは正常なので受け入れる。
- **VALIDATE**: 4 コマンドすべてがエラーなしで完了する

### Task 14: 閾値のキャリブレーション（実データで確認）
- **ACTION**: seed 済みの実データに対して実際の類似度分布を測り、`RELEVANCE_FLOOR` の既定値が
  妥当か確認する。
- **IMPLEMENT**:
  ```bash
  # seed 後に、代表クエリの上位候補の類似度を直接見る
  cd backend && cat > /tmp/sim.sql <<'SQL'
  -- placeholder: 実際には Search 経由で確認する方が早い
  SELECT 1;
  SQL
  # 手順: search_test.go の統合テストを -v で回し、Relevance をログする一時的な
  # t.Logf を足して分布を見る、または cmd/server を立てて /api/conversations に
  # 投げ sources イベントの relevance を読む。
  OPENAI_API_KEY=... go test ./internal/retrieval -run TestSearchAgainstPostgres -v
  ```
  観測した `Relevance` の分布に対して:
  - 上位候補が概ね `0.35` 以上に収まっていれば既定 `0.25 + 0.20*S` はそのまま。
  - 上位候補が `0.25` を下回って結果が空になるなら `RELEVANCE_FLOOR` を下げる
    （`.env` で調整し、妥当な値が分かったら `config.go` の既定値と `.env.example` を更新する）。
  - 逆に無関係なチャンクまで通るなら `RELEVANCE_FLOOR` を上げる。
- **MIRROR**: N/A（計測タスク）
- **IMPORTS**: N/A
- **GOTCHA**: `TestSearchAgainstPostgres` は「2 つのペルソナで順位が違う」ことを検証する。
  閾値が高すぎて両方 0 件になると `expected results from both personas` で落ちる。
  このテストが通ることが閾値の下限側の実質的なガードになっている。
- **GOTCHA**: この計測のために足した `t.Logf` は**コミット前に消す**（あるいは恒久的に有用なら
  `titleOrder` のように整えて残す）。
- **VALIDATE**: `OPENAI_API_KEY` を設定して `go test ./internal/retrieval -run TestSearchAgainstPostgres -v`
  が PASS し、`t.Logf` の出力で両ペルソナの順位が異なっていることを目視確認

---

## Testing Strategy

### Unit Tests

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| `TestEmbedderSendsNoPrefix` | `EmbedQueries(["合意形成"])` / `EmbedDocuments(["本文"])` | `captured.Input == ["合意形成"]` / `["本文"]`（プレフィックスなし） | No |
| `TestEmbedderRequestsConfiguredDimensions` | `EmbedQueries(["x"])` | `captured.Dimensions == 1536`, `captured.Model == "text-embedding-3-small"` | No |
| `TestEmbedderRejectsWrongDimensions` | サーバが 512 次元を返す | エラー、メッセージに `"512"` を含む | Yes |
| `TestEmbedderReordersByResponseIndex` | 3 件入力、サーバが逆順 + `Index` 付きで返す | `got[i][0] == float32(i)` | Yes |
| `TestEmbedderBatchesLargeInputs` | `maxBatch+1` 件 | HTTP 呼び出し 2 回、戻り値 `maxBatch+1` 件 | Yes |
| `TestEmbedderSubstitutesEmptyInput` | `["", "x"]` | `captured.Input[0] == " "`、エラーなし | Yes |
| `TestEmbedderEmptyInputSlice` | `nil` | `(nil, nil)`、HTTP 呼び出し 0 回 | Yes |
| `TestSkepticismRaisesThreshold` | `Relevance=0.30`、Skepticism 0 と 1 | 0 → 1 件通る / 1 → 0 件（閾値 0.25 / 0.45） | Yes |
| `TestPersonaInfluenceBlend` | `Relevance=0.7`、affinity=1、λ ∈ {0, 0.5, 1} | `Final` = 0.7 / 0.85 / 1.0 | No |
| `TestAffinityWithNoInterests` | interests なし、`Relevance=0.7`、λ=0.5 | `Affinity == 0`、`Final == 0.6` | Yes |
| `TestBlendEmptyInput` | `nil` candidates | `nil` | Yes |
| `TestHealth` | Pool なしで `GET /api/health` | 200、`{"status":"degraded","db":"unconfigured"}` | Yes |
| `TestSearchAgainstPostgres` | seed 済み Postgres + 実 OpenAI、同一クエリ・2 ペルソナ | 両方 1 件以上、かつ順位が異なる | 統合 |
| `TestMigrate*`（既存 `db_test.go`） | 実 Postgres | `0001` と `0002` が適用され、`chunks.embedding` が `vector(1536)` | 統合 |

### Edge Cases Checklist
- [x] 空入力 — `embed(nil)` → `(nil, nil)`、`blend(nil)` → `nil`
- [x] 空文字列チャンク — `" "` に置換して 400 を回避
- [x] 最大サイズ入力 — `maxBatch` 超えの分割を `TestEmbedderBatchesLargeInputs` が担保
- [x] 次元不一致 — `TestEmbedderRejectsWrongDimensions`。モデル差し替え事故が INSERT ではなくクライアントで落ちる
- [x] レスポンス順序の入れ替わり — `Index` での再配置を `TestEmbedderReordersByResponseIndex` が担保
- [x] レスポンス件数不一致 / インデックス欠落 — `embedBatch` が明示エラー
- [ ] 並行アクセス — 変更なし（`*openai.Client` は `*http.Client` 同様 goroutine-safe）
- [x] ネットワーク障害 — `CreateEmbeddings` のエラーが `embed: create embeddings: %w` で上がる。
      リトライは NOT Building
- [x] 認証エラー（401） — `OPENAI_API_KEY` 未設定なら `config.Validate()` が起動時に止める。
      seed は `Validate()` を呼ばないので 401 がそのまま上がる（既存挙動を維持）
- [ ] レートリミット（429） — 対応しない（NOT Building）
- [x] 全候補が閾値で切られる — `Search` が `(nil, nil)` を返し、`engine` は空の `sources` を送る（既存の正常系）
- [x] ペルソナの interests がゼロ — Task 8 の修復パスと `TestAffinityWithNoInterests` がカバー

---

## Validation Commands

### Static Analysis
```bash
cd /Users/shun/github/kaigi/backend
go build ./...
go vet ./...
```
EXPECT: 出力なし（エラーゼロ）

### 残存参照チェック
```bash
cd /Users/shun/github/kaigi
grep -rn -i 'deepseek' --include='*.go' --include='*.yml' --include='*.md' --include='Makefile' . | grep -v '\.claude/'
grep -rn -i 'tei\b\|text-embeddings-inference\|ruri' backend/internal backend/cmd docker-compose.yml Makefile .env.example README.md | grep -v '0001_init.sql'
grep -rn 'EMBED_URL\|RERANK_URL\|EmbedURL\|RerankURL\|NewReranker\|RerankResult\|crossEncoder' . --include='*.go' --include='*.md' --include='*.example' | grep -v '\.claude/'
```
EXPECT: すべて 0 ヒット（`0001_init.sql` と `.claude/` 配下のプラン文書は除く）

### Unit Tests
```bash
cd /Users/shun/github/kaigi/backend
go test ./internal/retrieval -v
go test ./internal/api -v
go test ./internal/chat -v
```
EXPECT: 全 PASS。DB / OPENAI_API_KEY を要する統合テストは SKIP

### Full Test Suite
```bash
cd /Users/shun/github/kaigi/backend && go test ./...
```
EXPECT: 回帰なし

### Database Validation
```bash
cd /Users/shun/github/kaigi
make down && docker volume rm kaigi_hf-cache 2>/dev/null; make up
cd backend && go run ./cmd/server   # 起動して 0002 を適用、Ctrl-C
psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" \
  -c "SELECT version FROM schema_migrations ORDER BY version"
psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" \
  -c "SELECT atttypmod FROM pg_attribute WHERE attrelid='chunks'::regclass AND attname='embedding'"
psql "postgres://postgres:kaigi@localhost:5432/kaigi?sslmode=disable" \
  -c "\di chunks_embedding_hnsw"
```
EXPECT: `0001_init.sql` と `0002_openai_embeddings.sql` の 2 行。`atttypmod` が `1536`。
HNSW インデックスが存在する。

### Seed + 統合テスト
```bash
cd /Users/shun/github/kaigi && make seed
psql "$DATABASE_URL" -c "SELECT count(*) FROM chunks"                 # > 0
psql "$DATABASE_URL" -c "SELECT p.name, count(i.id) FROM personas p LEFT JOIN persona_interests i ON i.persona_id=p.id GROUP BY p.name"
# 批評家|2、実務家|2
cd backend && go test ./internal/retrieval -run TestSearchAgainstPostgres -v
```
EXPECT: seed が成功、両ペルソナが interests 2 件、統合テストが PASS して順位が異なる

### Browser Validation
```bash
cd /Users/shun/github/kaigi && make dev
# 別ターミナル
curl -s localhost:8080/api/health   # {"status":"ok","db":"ok"}
open http://localhost:5173
```
EXPECT: ペルソナを選んで質問 → トークンがストリームされ、引用が表示される

### Manual Validation
- [ ] `make up` が 10 秒程度で healthy になる（モデル DL なし、TEI コンテナなし）
- [ ] `docker compose ps` に `tei-embed` / `tei-rerank` が存在しない
- [ ] `.env` の `OPENAI_API_KEY` を空にして `go run ./cmd/server` → `OPENAI_API_KEY is required` で即終了
- [ ] `make seed` が 1 回目で作成、2 回目で「already seeded」になり、いずれもエラーなし
- [ ] `curl -s localhost:8080/api/health | jq` が `{"status":"ok","db":"ok"}`
- [ ] `docker compose stop postgres` 後の health が `{"status":"degraded","db":"unreachable"}`
- [ ] ブラウザで「批評家」と「実務家」に同じ質問をして、引用の並びが違う
- [ ] SSE でトークンが逐次届く（まとめて 1 回ではない）
- [ ] 引用の `relevance` が 0..1 の範囲に見え、`0.25` 未満のものが出てこない
- [ ] `RELEVANCE_FLOOR=0.9` にすると引用がゼロになり、回答が「資料にない」旨を述べる

---

## Acceptance Criteria
- [ ] 全 14 タスク完了
- [ ] `go build ./...` / `go vet ./...` / `go test ./...` がすべてクリーン
- [ ] 残存参照チェックの 3 つの grep が 0 ヒット
- [ ] `docker compose config --services` が `postgres` / `minio` / `minio-init` のみ
- [ ] `chunks.embedding` と `persona_interests.embedding` が `vector(1536)`
- [ ] `make up && make seed && make dev` だけで、Docker でモデルを一切落とさずに動く
- [ ] ブラウザで 2 つのペルソナの引用順が異なることを確認済み

## Completion Checklist
- [ ] コードが Patterns to Mirror のパターンに従っている
- [ ] エラーハンドリングが `fmt.Errorf("<ctx>: %w", err)` 様式
- [ ] ログが `log/slog` + スネークケースキー
- [ ] テストが標準 `testing` のみ、`got/want` 形式
- [ ] ハードコード値なし（モデル名・閾値・次元はすべて config か名前付き定数）
- [ ] README / `.env.example` / docker-compose / Makefile を更新済み
- [ ] スコープ外の追加をしていない（NOT Building を守った）
- [ ] `0001_init.sql` を編集していない
- [ ] キャリブレーション用の一時的な `t.Logf` を消した
- [ ] 自己完結 — 実装中にコードベース検索が不要だった

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| cross-encoder を失って検索品質が落ちる | High | Medium | ベクトル距離のみになるので順位付けは粗くなる。`CANDIDATE_K=40` → `FINAL_N=8` の絞り込みが実質 top-8 のベクトル検索になる。判断 #1 で受容済み。品質が問題になったら Cohere rerank を別タスクで追加 |
| 閾値 `0.25 + 0.20*S` が実コーパスに合わず引用が空 or ノイズだらけ | High | Medium | env で調整可能にし、Task 14 で実測してから既定値を確定する。`TestSearchAgainstPostgres` が「両ペルソナで 1 件以上」を要求するので空振りは検出される |
| `text-embedding-3-small` の日本語性能が ruri-v3（日本語特化）に劣る | Medium | Medium | 判断 #2 でネイティブ 1536 次元を選んで情報量は最大化済み。不足なら `EMBED_MODEL=text-embedding-3-large` + `Dimensions` 定数と新マイグレーションで差し替えられる |
| `CHAT_MODEL=gpt-5-mini` がアカウントで使えない | Medium | Low | env 一発で差し替え可能。`gpt-4o-mini` が広く使える代替。README にその旨を書く |
| マイグレーション 0002 が `persona_interests` を空にしたまま seed がスキップして affinity が常に 0 になる | High（対策なしなら） | High | Task 8 で `seedPersonas` に修復パスを入れる。`TestSearchAgainstPostgres` の「順位が同一なら失敗」がこの退化を検出する |
| 既存の引用（`message_citations`）が消える | High | Low | dev データのみ。README の移行手順で明示する |
| `OPENAI_BASE_URL` に `/v1` を書き忘れて 404 | Medium | Low | 既定値に `/v1` を含め、`.env.example` と README にコメントを書く |
| OpenAI 課金が読めない | Medium | Low | seed は 1 回 ~数千トークン。`text-embedding-3-small` は極めて安価。統合テストは `OPENAI_API_KEY` 未設定で skip されるので CI で課金されない |
| `go mod tidy` の差分が大きく見える | High | None | 全 require が `// indirect` だったため直接依存への格上げが起きる。正常な差分 |

## Notes

### 閾値を既定 `0.25 + 0.20*S` にした理由（プレビューの `0.55` から変更）
判断 #1 のプレビューには `threshold := 0.55 + 0.20*S` と書いたが、実装前に見直して
**`0.25 + 0.20*S`** を既定にする。理由:

- 旧 `0.15 + 0.45*S` は TEI cross-encoder のスコア分布（S 字で、関連ペアは 0.8+ に張り付く）向けの値。
- コサイン類似度の分布はまったく違う。`text-embedding-3-small` では話題が一致する文でも
  おおむね 0.3〜0.6 に収まり、無関係な文が 0.1〜0.25。`0.55` を基準にすると
  **Skepticism 0.5 のペルソナの閾値が 0.65 になり、ほぼ全候補が切られて常に「資料にない」と答える**。
- 正しい値はコーパスと言語に依存し、測らないと決まらない。だから `RELEVANCE_FLOOR` /
  `RELEVANCE_SKEPTICISM_SPAN` を env にし、Task 14 で実測してから既定を確定する。

### `EmbedQueries` / `EmbedDocuments` を 2 メソッド残す理由
OpenAI 埋め込みはプレフィックス条件付けされていないので中身は同一になる。それでも残すのは:
1. `persona.Store` の `embedder` seam（`EmbedQueries` のみ）と `Searcher` の `queryEmbedder` seam が
   このシグネチャに依存している。片方に寄せると 2 パッケージの seam を書き換えることになる。
2. 呼び出し側が「クエリ側かドキュメント側か」を明示し続けることに、将来プレフィックスや
   非対称モデルに戻す余地としての価値がある。

### `0001_init.sql` のコメントについて
`0001_init.sql` L16-19 には「The embedding is of "検索クエリ: {topic}"」という ruri 前提の
コメントが残る。適用済みマイグレーションは編集しない方針なので**そのまま残す**。代わりに
`0002` の冒頭コメントで「プレフィックスは使われなくなった」ことを述べる。
`internal/persona/persona.go` L9 の同趣旨のコメントは**コードコメントなので更新する**。

### ヘルスチェックで OpenAI をプローブしない理由
OpenAI に無料の liveness エンドポイントは無い（`GET /v1/models` は認証付きで、キーの有効性は
測れるがヘルスチェックごとに叩くのは筋が悪い）。Postgres の ping に置き換えるほうが、
「プロセスは生きているが下流が死んでいる」を検出するという元の意図に忠実。

### 本タスクで触らないもの
`internal/chat/engine.go` / `prompt.go` / `store.go`、`internal/objectstore/`、`frontend/` の
ロジックは変更しない。`engine.go` と `prompt.go` は**コメント中の "DeepSeek" を "OpenAI" に
直すだけ**で、プロンプト組み立て・SSE・引用保存の挙動は一切変えない。
