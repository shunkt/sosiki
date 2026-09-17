# Plan: Kubernetes 上の A2A ペルソナ会議

> **実装ステータス（2026-09-17）**: **全26タスク完了・コミット済み**（`fc55708`, `26e3426`, `ca61ffe`）。Docker Compose と実際の kind クラスタの両方で実機E2E検証済み（後者は Ingress 経由で実 OpenAI 生成を含む完全な会議ターンを確認）。詳細は `.claude/PRPs/reports/k8s-a2a-persona-mesh-report.md` を参照。

## Summary

単一プロセスの kaigi バックエンドを、Kubernetes 上の4種類の pod に解体する。ペルソナは1体1 pod の独立した **A2A エージェント**になり、フロントエンド pod が司会としてラウンドを回し、各ペルソナ pod へ A2A の `SendStreamingMessage` を発行する。ペルソナは前の発言者の発言を会議録として受け取るので、同じ知識ベースを見ながら互いの主張に反応する。Postgres は1インスタンスのまま、機能ごとに**データベースを切り替える**（`kaigi_meeting` / `kaigi_registry` / `kaigi_persona` / `kaigi_knowledge`）。

## User Story

As a 人間の参加者,
I want 複数のペルソナが1つの議題について互いの発言を踏まえて議論するのを眺めたい,
So that 単一ペルソナへの Q&A では出てこない、立場の衝突と論点の出し合いが読める。

## Problem → Solution

**現状**: `cmd/server` 1プロセスに全部入り。`chat.Engine` は1会話＝1ペルソナに固定され、ペルソナはプロセス内の DB 行にすぎない。ネットワーク境界がないので「ペルソナ同士がやりとりする」を表現できない。docker compose の6サービス構成。

**目標**: ペルソナ = ネットワーク越しに到達可能な A2A エージェント。フロントエンド pod（司会）が discovery pod で参加者を解決し、A2A でラウンドを回す。Kubernetes（kind）上で `kubectl apply -k` 一発。Postgres は1インスタンス・4データベース。

## Metadata

- **Complexity**: XL（新規 Go パッケージ 5 / 新規 cmd 3 / 削除 cmd 1 / k8s マニフェスト 14 / フロントエンド全面改修）
- **Source PRD**: N/A（フリーフォーム指示）
- **PRD Phase**: standalone
- **Estimated Files**: 48（新規 37、更新 10、削除 1）
- **前提コミット**: `3e80b35`（main）

---

## 確定した決定事項（ユーザー回答）

| 論点 | 決定 |
|---|---|
| 会話の駆動 | **フロントエンド pod が司会**。discovery pod で参加者を解決し、各ペルソナ pod へ順に A2A を発行。ペルソナ同士の直接通信は**なし**（スター型） |
| DB 分割の粒度 | **機能ごと**。1 Postgres インスタンス内に `kaigi_meeting` / `kaigi_registry` / `kaigi_persona` / `kaigi_knowledge` の4データベース |
| MinIO | **pod として残す**。`chat.Engine` のコンテキスト拡張（Range GET）と引用 presigned URL を維持 |
| k8s | **kind + Kustomize**。`k8s/base` + `k8s/overlays/local`、Ingress は ingress-nginx |
| ブラウザ↔フロントエンド pod | 既存の **REST + SSE を維持**（A2A はフロントエンド pod 以降） |

### 「ペルソナ同士が相互にやりとりする」の実現方法（重要）

スター型を選んだので、ペルソナ pod が他のペルソナ pod を直接呼ぶことはない。**相互作用は会議録の共有で起きる**:

```
Round 1:
  critic      ← 受け取る会議録: [user: 議題]
  pragmatist  ← 受け取る会議録: [user: 議題][critic: 発言1]      ← critic に反応できる
Round 2:
  critic      ← 受け取る会議録: [user][critic:1][pragmatist:1]   ← pragmatist に反論できる
  pragmatist  ← 受け取る会議録: [user][critic:1][pragmatist:1][critic:2]
```

したがって**ラウンド内のペルソナ呼び出しは逐次でなければならない**。並列化すると同じラウンドの他者の発言が見えず、議論ではなく独立した N 本の回答になる。これは性能上の妥協ではなく仕様である（Task 12 の GOTCHA）。

---

## 明示した前提（ユーザー未指定 → こちらで決定）

1. **`cmd/server` は削除する。** 単一プロセス版を残すと「全部入り」と「A2A 分散」の2つのアーキテクチャを並行保守することになる。代わりに **docker-compose を新トポロジ（moderator / discovery / persona×2）に書き換える**ので、`make up` + `make dev` のホットリロード開発ループは維持される。k8s とコンテナで同じプロセス構成が走る。

2. **フロントエンド pod は2コンテナ**（`web` = Caddy で SPA 配信 + `/api` を `localhost:8080` へリバースプロキシ、`moderator` = Go）。ユーザーが挙げた pod 一覧に「フロントエンドの pod」が1つだったので、SPA と司会を同一 pod に入れて数を合わせる。Caddyfile の変更は `backend:8080` → `localhost:8080` の1行。

3. **discovery pod は A2A のエージェントカード・カタログ**。A2A 仕様にレジストリの定義はないので自作する。ペルソナ pod が起動時に自己登録し、15秒ごとにハートビート、TTL 45秒。discovery 側は登録された base URL から `/.well-known/agent-card.json` を解決してキャッシュする。Kubernetes の Service DNS があれば discovery は理論上不要だが、「どのペルソナが今生きていて、どんな skill を持つか」をカードで答える層はユーザー指定の構成要素なので実装する。

4. **会議録はメッセージに載せて渡す**（A2A の task 履歴には依存しない）。ペルソナ pod は会話状態を持たない: `kaigi_persona`（自分の定義）と `kaigi_knowledge`（チャンク）を**読むだけ**。書き込みは司会だけが `kaigi_meeting` に対して行う。これで A2A の "opaque agent" 原則に沿い、ペルソナ pod は水平スケール可能になる。

5. **`message_citations` の外部キーは失われる**。`messages` は `kaigi_meeting`、`chunks` は `kaigi_knowledge` にあり、Postgres はデータベース間 FK を張れない。引用は `chunk_id uuid`（FK なし）＋ `title` / `object_key` / スコアの非正規化コピーとして保存する。トレードオフは Risks に記載。

6. **`/api/conversations` 系は廃止し `/api/meetings` に置き換える**。1ペルソナの会議は参加者1名の会議として表現できるので、概念を2つ持つ理由がない。

7. **認証・認可は引き続きスコープ外**。A2A の `SecuritySchemes` はカードに空で出す。クラスタ内通信は平文 HTTP（JSON-RPC）。

8. **A2A のトランスポートは JSONRPC** を選ぶ。SDK は GRPC / HTTP+JSON も持つが、JSON-RPC は `curl` でデバッグでき、SSE ストリーミングがそのまま乗る。

---

## UX Design

### Before

```
┌──────────────────────────────────────────────────────┐
│  kaigi                          ペルソナ: [批評家 ▾]  │
├──────────────────────────────────────────────────────┤
│  user  ▸ 分散システムの合意形成について              │
│                                                      │
│  批評家 ▸ Raft の理解しやすさという主張には▊         │
│                                                      │
│  根拠 (3):                                            │
│   [1] raft-paper.md    (関連 0.94 / 関心 +0.31)      │
├──────────────────────────────────────────────────────┤
│  [ メッセージを入力…                    ] [送信]     │
└──────────────────────────────────────────────────────┘
        1ペルソナと1対1。他のペルソナは選び直すしかない
```

### After

```
┌────────────────────────────────────────────────────────────────┐
│  kaigi 会議                                        ● 3/3 在席   │
│  参加者: [✓批評家] [✓実務家] [✓設計者]        ラウンド: [2 ▾]  │
├────────────────────────────────────────────────────────────────┤
│  議題 ▸ 分散システムの合意形成について                          │
│                                                                │
│  ── Round 1 ─────────────────────────────────────────────────  │
│  批評家 ▸ Raft の「理解しやすさ」という主張には留保が必要で…   │
│    根拠: [1] raft-paper.md (0.94/+0.31) [2] paxos… (0.88/-0.10)│
│                                                                │
│  実務家 ▸ 批評家の指摘はもっともだが、運用上は▊               │
│    ↑ 批評家の発言を会議録で受け取っているので直接反応できる    │
│                                                                │
│  設計者 ▸ （応答待ち…）                                        │
│                                                                │
│  ── Round 2 ─────────────────────────────────────────────────  │
│  批評家 ▸ 実務家の「運用上」という反論について…                │
├────────────────────────────────────────────────────────────────┤
│  [ 議題を入力…                                    ] [開始]     │
└────────────────────────────────────────────────────────────────┘
```

### Interaction Changes

| Touchpoint | Before | After | Notes |
|---|---|---|---|
| ペルソナ選択 | 単一選択の `<select>`。切り替えると会話が破棄される | 複数選択のチェックボックス。参加者集合が会議を定義する | `GET /api/personas` は discovery 由来になり、在席していないペルソナは disabled 表示 |
| 送信 | `POST /api/conversations/{id}/messages` | `POST /api/meetings/{id}/turns`（`rounds` 付き） | SSE フレーム形式（`event:` / `data:`）は不変 |
| ストリーム | 1話者。`sources` → `token`* → `done` | 話者が切り替わる。`speaker_start` → `sources` → `token`* → `speaker_end` を参加者×ラウンド回繰り返し → `round_end`* → `done` | 全フレームに `personaId` が入る |
| 引用表示 | 会話末尾に1ブロック | 発言ごとに1ブロック | `Source` 型は不変 |
| 在席表示 | なし | ヘッダに `● n/m 在席`（discovery のハートビートから） | 新規 |
| エラー | 会話全体が失敗 | 1ペルソナの失敗は `speaker_error` でその発言だけ落ち、会議は続行 | 部分失敗の分離 |

---

## アーキテクチャ

```
                          kind cluster (namespace: kaigi)
┌─────────────────────────────────────────────────────────────────────────┐
│                                                                         │
│  Ingress (ingress-nginx)  kaigi.localtest.me  ─┐                        │
│                                                │                        │
│  ┌─────────────────────────────────────────────▼───────────────────┐    │
│  │ pod: frontend                                                   │    │
│  │  ┌──────────────┐          ┌──────────────────────────────────┐ │    │
│  │  │ web (Caddy)  │  :80     │ moderator (Go)            :8080  │ │    │
│  │  │ SPA 配信     │─────────▶│ REST + SSE（ブラウザ向け）       │ │    │
│  │  │ /api → :8080 │ localhost│ A2A クライアント（司会）          │ │    │
│  │  └──────────────┘          └──┬──────────────┬────────────────┘ │    │
│  └────────────────────────────────┼──────────────┼──────────────────┘    │
│                                   │              │                       │
│           GET /registry/agents    │              │ A2A JSON-RPC          │
│                                   ▼              │ SendStreamingMessage  │
│  ┌──────────────────────────────────────┐        │                       │
│  │ pod: discovery                :8081  │        │                       │
│  │  エージェントカード・カタログ         │        │                       │
│  │  自己登録 + ハートビート(TTL 45s)     │        │                       │
│  └──────────────────────────────────────┘        │                       │
│                     ▲ POST /registry/agents      │                       │
│                     │                            ▼                       │
│  ┌──────────────────┴───────────────────────────────────────────────┐    │
│  │ pod: persona-critic / persona-pragmatist / … (N個)        :8082  │    │
│  │  A2A サーバ  /.well-known/agent-card.json + JSON-RPC /           │    │
│  │  AgentExecutor: クエリ書き換え→検索→性格重み付け→拡張→生成        │    │
│  │  会話状態なし（会議録はリクエストで受け取る）                      │    │
│  └────────┬───────────────────────────────────────┬─────────────────┘    │
│           │                                       │                      │
│           ▼                                       ▼                      │
│  ┌────────────────────────────────┐   ┌─────────────────────────────┐   │
│  │ StatefulSet: postgres  :5432   │   │ StatefulSet: minio   :9000  │   │
│  │  pgvector/pgvector:pg17        │   │  原本ファイル + Range GET   │   │
│  │  ├ kaigi_meeting   ← moderator │   └─────────────────────────────┘   │
│  │  ├ kaigi_registry  ← discovery │                                     │
│  │  ├ kaigi_persona   ← persona   │   ┌─────────────────────────────┐   │
│  │  └ kaigi_knowledge ← persona   │   │ Job: minio-init / seed      │   │
│  └────────────────────────────────┘   └─────────────────────────────┘   │
│                                                                         │
│           OpenAI API（chat + embeddings）へは persona pod だけが出る     │
└─────────────────────────────────────────────────────────────────────────┘
```

### データベースの持ち主と中身

| データベース | 書く pod | 読む pod | テーブル | pgvector |
|---|---|---|---|---|
| `kaigi_meeting` | moderator | moderator | `meetings`, `meeting_participants`, `turns`, `turn_citations` | 不要 |
| `kaigi_registry` | discovery | discovery | `agents` | 不要 |
| `kaigi_persona` | seed | persona | `personas`, `persona_interests` | **必要** |
| `kaigi_knowledge` | seed | persona | `documents`, `chunks` | **必要** |

`CREATE EXTENSION vector` は `kaigi_persona` と `kaigi_knowledge` にのみ必要。`uuid-ossp` は4つ全部に必要（全テーブルが `uuid_generate_v4()` を既定値に使う）。

---

## Mandatory Reading

実装前に必ず読むファイル。

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `backend/internal/chat/engine.go` | 1-230 | 解体対象の中心。`Reply` のシグネチャを変える。イベント順序（`sources`→`token`*→`done`）とコンテキスト拡張の並列化をそのまま持ち越す |
| P0 | `backend/internal/config/config.go` | 1-199 | 全 env をここで読む規約。`env`/`envInt`/`envFloat`/`envBool` ヘルパを再利用する。DSN 組み立てを足す |
| P0 | `backend/internal/db/db.go` | 15-111 | `go:embed` の制約（パッケージ配下しか届かない）とマイグレータ。スキーマ別に分ける |
| P0 | `backend/internal/api/chat.go` | 14-110 | SSE の書き方の正典。`http.ResponseController` での per-frame デッドライン、keep-alive、`writeSSE` のフレーム形式 |
| P0 | `backend/internal/api/api.go` | 20-63 | `net/http` メソッド付きパターンでのルーティング、`Deps` による依存注入、`chatEngine` インタフェースを1箇所だけ切る方針 |
| P1 | `backend/internal/retrieval/search.go` | 20-140 | `Candidate` 型と `blend()`。ここは**無変更**で持ち越す。pool を渡す先が変わるだけ |
| P1 | `backend/internal/chat/prompt.go` | 13-142 | `BuildSystemPrompt` はバイト同一でなければキャッシュが効かない。会議録を system に入れてはいけない理由 |
| P1 | `backend/internal/chat/store.go` | 42-140 | `PersonaFor` が消える。`History`/`AppendMessage`/`SaveCitations` は moderator 側へ移る |
| P1 | `backend/internal/persona/store.go` | 12-27, 42-90 | `embedder` インタフェースを local に切る理由（import cycle 回避）。persona pod が使う |
| P1 | `backend/internal/objectstore/store.go` | 29-57 | `Region` を明示する理由。k8s でも `MINIO_PUBLIC_ENDPOINT` は Ingress 経由の名前になる |
| P1 | `backend/internal/api/middleware.go` | 32-47 | `statusRecorder.Unwrap` がないと SSE がバッファされる。moderator にも同じものが必要 |
| P1 | `frontend/src/api/client.ts` | 26-121 | SSE フレームのパーサ。`ChatEvent` を話者付きに拡張する。パーサ自体は変えない |
| P2 | `backend/internal/api/api_test.go` | 20-110 | テストの型（`testConfig()`、`io.Discard` のロガー、fake を1箇所だけ） |
| P2 | `backend/internal/db/db_test.go` | 15-62 | 統合テストの skip 規約（`TEST_DATABASE_URL` → `DATABASE_URL` → `t.Skip`） |
| P2 | `backend/cmd/seed/main.go` | 45-90 | seed の構造。2つの pool を持つように変える |
| P2 | `docker-compose.yml` | 1-121 | `x-backend-env` アンカーの使い方、`depends_on` の `service_completed_successfully` |
| P2 | `frontend/Caddyfile` | 1-24 | `encode` を `/api` ブロックに入れない理由（SSE がバッファされる） |

---

## External Documentation

すべて実物のソース／`go list` で検証済み（2026-09-16 時点）。

| Topic | Source | Key Takeaway |
|---|---|---|
| A2A 仕様バージョン | <https://a2a-protocol.org/latest/specification/> | **v1.0.0**。2026年1月に experimental → production-ready。署名付きエージェントカードが入った |
| 公式 Go SDK | <https://github.com/a2aproject/a2a-go> | モジュール `github.com/a2aproject/a2a-go/v2`。`go list -m -versions` で確認した最新は **v2.5.0**。Go 1.25+ 必須（本リポジトリは 1.26.3 なので可） |
| エージェントカードのパス | `a2asrv/agentcard.go:27` | `a2asrv.WellKnownAgentCardPath = "/.well-known/agent-card.json"`。定数が公開されているのでリテラルを書かない |
| サーバの組み立て | `a2asrv/example_test.go` `ExampleNewHandler_fullServer` | `NewHandler(executor)` → `NewJSONRPCHandler(handler)` を `/` に、`NewStaticAgentCardHandler(card)` を well-known に mount |
| AgentExecutor | `a2asrv` pkg doc | `Execute(ctx, *ExecutorContext) iter.Seq2[a2a.Event, error]` / `Cancel(...)` の2メソッド。Go 1.23 の range-over-func イテレータを返す |
| クライアント | `a2aclient/example_test.go` | `a2aclient.NewFromCard(ctx, card)` → `SendStreamingMessage(ctx, req) iter.Seq2[a2a.Event, error]` |
| カード解決 | `a2aclient/agentcard/resolver.go:86` | `agentcard.DefaultResolver.Resolve(ctx, baseURL)`。**baseURL を渡す**（well-known パスは内部で付く） |
| TaskState | `a2a/core.go:282-301` | `TASK_STATE_SUBMITTED` / `_WORKING` / `_COMPLETED` / `_FAILED` / `_CANCELED` / `_INPUT_REQUIRED` / `_REJECTED` / `_AUTH_REQUIRED`。Go 定数は `a2a.TaskStateWorking` など |
| Part | `a2a/core.go:565,600-644,764` | `a2a.NewTextPart(s)` / `NewDataPart(any)` / `NewFileURLPart(url,mime)`。読み出しは `p.Text()` / `p.Data()`。`p.SetMeta(k,v)` でメタデータを付けられる |
| イベント構築 | `a2a/core.go:489-547` | `NewSubmittedTask` / `NewStatusUpdateEvent(ctx,state,msg)` / `NewArtifactEvent(ctx,parts...)`（新 ID）/ `NewArtifactUpdateEvent(ctx,id,parts...)`（`Append:true` で追記） |
| トランスポート定数 | `a2a/agent.go:190-195` | `TransportProtocolJSONRPC="JSONRPC"` / `TransportProtocolGRPC="GRPC"` / `TransportProtocolHTTPJSON="HTTP+JSON"` |
| AgentInterface | `a2a/agent.go:140` | `a2a.NewAgentInterface(url, binding)` が `ProtocolVersion` に `a2a.Version`（`"1.0"`）を自動で入れる |

```
KEY_INSIGHT: AgentExecutor は iter.Seq2[a2a.Event, error] を返す。SDK 側にイベントキューは露出していない。
APPLIES_TO: Task 8（cmd/persona）
GOTCHA: yield の戻り値 false は「消費側が離脱した」の意味。既存 chat.Engine は chan<- Event に
        送るので、イテレータ内で goroutine + channel を橋渡しする必要がある。yield が false を
        返したら goroutine をリークさせないよう ctx をキャンセルすること。

KEY_INSIGHT: DataPart は JSON-RPC 越しに来ると p.Data() が map[string]any になる（Go の型は保たれない）。
APPLIES_TO: Task 7（internal/a2aconv）
GOTCHA: json.Marshal → json.Unmarshal で型付き構造体に詰め直す。直接の型アサーションは必ず失敗する。

KEY_INSIGHT: pkg.go.dev の TaskState 抽出は誤り（QUEUED/RUNNING と出る）。実ソースは TASK_STATE_* 形式。
APPLIES_TO: Task 8
GOTCHA: 定数名で書けば問題ない（a2a.TaskStateWorking）。文字列リテラルを書かないこと。

KEY_INSIGHT: Kustomize は同じ component を2回 include できない。
APPLIES_TO: Task 20（k8s マニフェスト）
GOTCHA: ペルソナ pod は base に1ファイル/ペルソナで明示的に置く。N体をループ生成したいなら Helm が必要
        だが、ユーザーは Kustomize を選んだ。3体目の追加はファイルコピー＋3箇所書き換えで済むようにする。
```

---

## Patterns to Mirror

コードベースから抽出した実物のパターン。**新しいコードはこれと区別がつかないように書く。**

### NAMING_CONVENTION

```go
// SOURCE: backend/internal/persona/persona.go:1-5, backend/internal/retrieval/search.go:43-51
// パッケージ名は単数形の名詞・小文字1語（persona, retrieval, chat, objectstore, config, db）。
// パッケージコメントは "// Package x ..." で始め、何であるかを1文で述べる。
// 永続化型は Store、コンストラクタは New/NewStore、依存は非公開フィールドで受ける。

// Package persona defines what a persona is: knowledge access shaped by a
// personality that biases retrieval and generation.
package persona

type Searcher struct {
	pool     *pgxpool.Pool
	embedder queryEmbedder
	cfg      config.RetrievalConfig
}

func NewSearcher(pool *pgxpool.Pool, embedder *Embedder, cfg config.RetrievalConfig) *Searcher {
	return &Searcher{pool: pool, embedder: embedder, cfg: cfg}
}
```

### ERROR_HANDLING

```go
// SOURCE: backend/internal/chat/engine.go:87-90, backend/internal/chat/store.go:63-76
// 常に fmt.Errorf("<パッケージ名>: <動作>: %w", err) でラップする。パッケージ内の下層は
// "search: embed queries: %w" のように動作名だけ、境界を越えるものは "chat: ..." と名前を付ける。
// センチネルは errors.Is で判定できるよう ErrXxx として公開し、pgx.ErrNoRows から変換する。

	p, err := e.store.PersonaFor(ctx, conversationID)
	if err != nil {
		return fmt.Errorf("reply: load persona: %w", err)
	}

	if err := s.pool.QueryRow(ctx, `...`).Scan(...); err != nil {
		if err == pgx.ErrNoRows {
			return persona.Persona{}, fmt.Errorf("chat: %w", ErrConversationNotFound)
		}
		return persona.Persona{}, fmt.Errorf("chat: load persona for conversation: %w", err)
	}
```

```go
// SOURCE: backend/internal/api/persona.go:101-111
// HTTP 層ではセンチネルを errors.Is で見て 404 に、それ以外はログ＋500。
// クライアントに返すのは常に {"error": "..."} の固定形。内部エラー文字列は漏らさない。

	p, err := h.deps.Personas.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, persona.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "persona not found"})
			return
		}
		h.deps.Log.Error("get persona", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get persona"})
		return
	}
```

### LOGGING_PATTERN

```go
// SOURCE: backend/cmd/server/main.go:23, backend/internal/api/middleware.go:54-59,
//         backend/internal/chat/prompt.go:89
// log/slog のみ。TextHandler を os.Stdout に。キーは snake_case、メッセージは小文字の動詞句。
// 依存注入されたロガーを使うのが基本だが、静的な関数からは slog パッケージ関数で直接出す。
// 「失敗したが継続できる」は Warn + フォールバックの説明、「呼び出しが失敗した」は Error。

log := slog.New(slog.NewTextHandler(os.Stdout, nil))

log.Info("request",
	"method", r.Method,
	"path", r.URL.Path,
	"status", rec.status,
	"duration", time.Since(start),
)

slog.Warn("query rewrite failed, falling back to raw utterance", "error", err)
```

### REPOSITORY_PATTERN

```go
// SOURCE: backend/internal/chat/store.go:50-58, backend/internal/persona/store.go:46-80
// SQL は生のまま raw string literal で、pgxpool を直接叩く（ORM なし）。
// プレースホルダは $1, $2。複数テーブルに跨る書き込みは tx + defer Rollback。
// Commit 前の Rollback は defer で必ず呼ぶ（成功後の Rollback は no-op なので安全）。

func (s *Store) CreateConversation(ctx context.Context, personaID uuid.UUID, title string) (uuid.UUID, error) {
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO conversations (persona_id, title) VALUES ($1, $2) RETURNING id
	`, personaID, title).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("chat: create conversation: %w", err)
	}
	return id, nil
}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Persona{}, fmt.Errorf("persona: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
```

### SERVICE_PATTERN

```go
// SOURCE: backend/internal/chat/engine.go:21-39
// 外部依存は「狭いインタフェース」として利用側パッケージに定義する（定義側ではなく利用側）。
// 具体型を直接使うのは、そのパッケージ自身の統合テストで既にカバーされている場合のみ。
// 何を seam にして何をしないかの判断理由をコメントに書くのが本リポジトリの規約。

// The interfaces below are narrow seams over Store, retrieval.Searcher, and
// objectstore.Store — the same pattern retrieval.Searcher uses for its own
// embeddings client — so Reply's event ordering (sources -> token* -> done)
// can be unit tested with fakes and no live Postgres or MinIO.
type conversationStore interface {
	PersonaFor(ctx context.Context, conversationID uuid.UUID) (persona.Persona, error)
	History(ctx context.Context, conversationID uuid.UUID, limit int) ([]Message, error)
}

type knowledgeSearcher interface {
	Search(ctx context.Context, p persona.Persona, queries []string) ([]retrieval.Candidate, error)
}
```

### SSE_PATTERN

```go
// SOURCE: backend/internal/api/chat.go:45-96
// ヘッダは最初の Write より前に全部セットする。ResponseController で per-frame デッドラインを張り、
// サーバの WriteTimeout は未設定にしておく（絶対期限なのでストリームを途中で切ってしまう）。
// keep-alive は ": comment\n\n"。イベントは select で channel / ticker / ctx.Done() を待つ。

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable reverse-proxy buffering
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		h.deps.Log.Error("SSE flush unsupported; response will buffer until handler returns", "error", err)
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
			if err := writeSSE(w, ev); err != nil {
				h.deps.Log.Error("SSE write failed", "conversation_id", id, "error", err)
				return
			}
			_ = rc.Flush()
		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			_ = rc.Flush()
		case <-r.Context().Done():
			return
		}
	}
```

### MIGRATION_PATTERN

```sql
-- SOURCE: backend/internal/db/migrations/0001_init.sql:16-27, 40-56
-- 番号プレフィックス付きファイル名（0001_, 0002_）。テーブルは複数形、カラムは snake_case。
-- uuid 主キーは DEFAULT uuid_generate_v4()、値域制約は CHECK で表明する。
-- 「なぜこの設計か」をテーブル上のコメントで説明する（本リポジトリの特徴）。

-- byte_start/byte_end locate the chunk inside the original object so a Range
-- GET can read the surrounding text: the chunk is the search unit, the expanded
-- span is the reading unit.
CREATE TABLE chunks (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index int  NOT NULL,
    byte_start  bigint NOT NULL,
    embedding   vector(1536) NOT NULL,
    UNIQUE (document_id, chunk_index)
);

-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the index.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);
```

### TEST_STRUCTURE

```go
// SOURCE: backend/internal/api/api_test.go:20-34, 103-110 / backend/internal/db/db_test.go:15-22
// 内部テスト（package api、_test サフィックスなし）。testify は使わず標準の testing のみ。
// エラーメッセージは "got = %v, want %v" 形式。テーブル駆動は tests := []struct{...} + t.Run。
// ロガーは io.Discard。統合テストは env が無ければ t.Skip する。

func testConfig() config.Config {
	return config.Config{
		Addr:           ":0",
		AllowedOrigins: []string{"http://localhost:5173"},
	}
}

func newTestHandler() http.Handler {
	return NewHandler(testConfig(), Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

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
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.want {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.want)
			}
		})
	}

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL not set; skipping integration test")
	}
```

### FRONTEND_PATTERN

```ts
// SOURCE: frontend/src/api/client.ts:26-51, 110-121
// 型は type エイリアス（interface ではなく）。イベントは判別可能 union。
// API 関数は getJSON/postJSON の薄いラッパを1行の const アロー関数で公開する。
// SSE は fetch + ReadableStream（EventSource は POST できない）。

export type ChatEvent =
  | { type: 'token'; text: string }
  | { type: 'sources'; sources: Source[] }
  | { type: 'done'; message: Message }
  | { type: 'error'; error: string }

export const listPersonas = (signal?: AbortSignal) => getJSON<Persona[]>('/api/personas', signal)
```

---

## Files to Change

### 新規（Go）

| File | Action | Justification |
|---|---|---|
| `backend/internal/config/dsn.go` | CREATE | 1インスタンス内のデータベース切り替え。`POSTGRES_BASE_URL` + DB 名 → DSN |
| `backend/internal/config/dsn_test.go` | CREATE | DSN 組み立て（クエリ文字列の保持、オーバーライド優先順位）の単体テスト |
| `backend/internal/db/migrations/meeting/0001_init.sql` | CREATE | `meetings` / `meeting_participants` / `turns` / `turn_citations` |
| `backend/internal/db/migrations/registry/0001_init.sql` | CREATE | `agents`（エージェントカードのキャッシュ + ハートビート） |
| `backend/internal/db/migrations/persona/0001_init.sql` | CREATE | `personas` / `persona_interests`（既存 0001+0002 を 1536 次元で統合） |
| `backend/internal/db/migrations/knowledge/0001_init.sql` | CREATE | `documents` / `chunks`（同上） |
| `backend/internal/a2aconv/a2aconv.go` | CREATE | A2A ↔ ドメイン型の変換。会議録 DataPart、引用 DataPart、トークン TextPart |
| `backend/internal/a2aconv/a2aconv_test.go` | CREATE | ラウンドトリップテスト（`map[string]any` 経由の再詰めを含む） |
| `backend/internal/a2aconv/card.go` | CREATE | `persona.Persona` → `*a2a.AgentCard` |
| `backend/internal/a2aconv/card_test.go` | CREATE | skill / tag / URL の生成検証 |
| `backend/internal/meeting/meeting.go` | CREATE | 会議のドメイン型（`Meeting` / `Turn` / `Citation` / `Event`） |
| `backend/internal/meeting/store.go` | CREATE | `kaigi_meeting` への永続化 |
| `backend/internal/meeting/store_test.go` | CREATE | 統合テスト（`TEST_MEETING_DATABASE_URL` gate） |
| `backend/internal/meeting/moderator.go` | CREATE | ラウンド進行。参加者を逐次呼び、A2A イベントを会議イベントへ変換 |
| `backend/internal/meeting/moderator_test.go` | CREATE | fake A2A クライアントでラウンド順序とイベント順序を検証（本機能の受け入れ条件） |
| `backend/internal/registry/registry.go` | CREATE | 登録エントリのドメイン型 + TTL 判定 |
| `backend/internal/registry/store.go` | CREATE | `kaigi_registry` への永続化（upsert + ハートビート） |
| `backend/internal/registry/store_test.go` | CREATE | 統合テスト（TTL による在席/不在の切り替え） |
| `backend/internal/registry/client.go` | CREATE | ペルソナ pod 側の自己登録＋ハートビートループ |
| `backend/internal/agentapi/agentapi.go` | CREATE | discovery の HTTP API（`POST /registry/agents`、`GET /registry/agents`、health） |
| `backend/internal/agentapi/agentapi_test.go` | CREATE | ハンドラテスト |
| `backend/internal/personaexec/executor.go` | CREATE | `a2asrv.AgentExecutor` 実装。`chat.Engine` を A2A イベントに橋渡し |
| `backend/internal/personaexec/executor_test.go` | CREATE | fake engine でイベント列（submitted→working→artifact*→completed）を検証 |
| `backend/cmd/moderator/main.go` | CREATE | フロントエンド pod の Go コンテナ。REST+SSE + A2A クライアント |
| `backend/cmd/discovery/main.go` | CREATE | discovery pod |
| `backend/cmd/persona/main.go` | CREATE | persona pod。A2A サーバ |

### 更新（Go）

| File | Action | Justification |
|---|---|---|
| `backend/internal/config/config.go` | UPDATE | `DatabaseURL` を4つに分離、`A2A` / `Registry` / `Meeting` / `Persona` 設定セクション追加 |
| `backend/internal/db/db.go` | UPDATE | `New(ctx, dsn, set, log)`。`go:embed migrations` をツリー全体に、`migrationNames(set)` |
| `backend/internal/db/db_test.go` | UPDATE | セット指定に追従。`knowledge` セットで vector(1536) を検証 |
| `backend/internal/chat/engine.go` | UPDATE | `Reply(ctx, p, transcript, utterance, out)`。会話 DB 依存を削除 |
| `backend/internal/chat/engine_test.go` | UPDATE | 新シグネチャに追従。`conversationStore` fake を削除 |
| `backend/internal/chat/store.go` | UPDATE | `PersonaFor` / `CreateConversation` / `History` / `AppendMessage` / `SaveCitations` を削除（meeting へ移動）。`DocumentObjectKey` は残す |
| `backend/internal/chat/prompt.go` | UPDATE | `BuildSystemPrompt(p, participants)`。他の参加者名を system に載せる（会議ごとに固定＝キャッシュは保てる） |
| `backend/internal/chat/prompt_test.go` | UPDATE | 参加者リスト付きの検証を追加 |
| `backend/internal/api/*.go` | UPDATE | `api` パッケージを moderator 専用に。persona/conversation ハンドラを meeting ハンドラに置換 |
| `backend/cmd/seed/main.go` | UPDATE | 2つの pool（persona / knowledge）を開く |
| `backend/go.mod` / `go.sum` | UPDATE | `github.com/a2aproject/a2a-go/v2 v2.5.0` |

### 削除

| File | Action | Justification |
|---|---|---|
| `backend/cmd/server/main.go` | DELETE | 単一プロセス版。compose を新トポロジにするので不要（前提 1） |
| `backend/internal/db/migrations/0001_init.sql` | DELETE | セット別ディレクトリへ再編 |
| `backend/internal/db/migrations/0002_openai_embeddings.sql` | DELETE | 同上。新規環境なので 1536 次元を初版に織り込む |

### 新規（インフラ / フロントエンド）

| File | Action | Justification |
|---|---|---|
| `k8s/base/kustomization.yaml` | CREATE | base の集約 |
| `k8s/base/namespace.yaml` | CREATE | namespace `kaigi` |
| `k8s/base/config.yaml` | CREATE | ConfigMap（非機密設定） |
| `k8s/base/secret.yaml` | CREATE | Secret のテンプレート（`OPENAI_API_KEY` は overlay で注入） |
| `k8s/base/postgres.yaml` | CREATE | StatefulSet + Service + 4 DB 作成用 initdb ConfigMap |
| `k8s/base/minio.yaml` | CREATE | StatefulSet + Service |
| `k8s/base/minio-init-job.yaml` | CREATE | バケット作成 Job |
| `k8s/base/discovery.yaml` | CREATE | Deployment + Service |
| `k8s/base/frontend.yaml` | CREATE | Deployment（web + moderator の2コンテナ）+ Service |
| `k8s/base/ingress.yaml` | CREATE | ingress-nginx。SSE 用のバッファ無効アノテーション付き |
| `k8s/base/personas/persona-critic.yaml` | CREATE | ペルソナ pod #1 |
| `k8s/base/personas/persona-pragmatist.yaml` | CREATE | ペルソナ pod #2 |
| `k8s/base/seed-job.yaml` | CREATE | フィクスチャ投入 Job |
| `k8s/overlays/local/kustomization.yaml` | CREATE | kind 向け overlay（imagePullPolicy、レプリカ、ホスト名） |
| `k8s/kind-cluster.yaml` | CREATE | kind クラスタ定義（extraPortMappings 80/443） |
| `frontend/src/api/client.ts` | UPDATE | meeting API + マルチ話者 SSE イベント |
| `frontend/src/App.tsx` | UPDATE | 参加者複数選択、ラウンド区切り、話者ごとの引用 |
| `frontend/src/App.css` | UPDATE | 話者レーン・ラウンド区切りのスタイル |
| `frontend/Caddyfile` | UPDATE | `reverse_proxy backend:8080` → `localhost:8080` |
| `docker-compose.yml` | UPDATE | 新トポロジ（moderator / discovery / persona×2） |
| `Makefile` | UPDATE | `k8s-up` / `k8s-down` / `k8s-seed` / `kind-load` を追加、`dev` を4プロセスに |
| `.env.example` | UPDATE | 新 env |
| `README.md` | UPDATE | アーキテクチャ図・起動手順・API 表 |

---

## NOT Building

以下は**明示的にスコープ外**。実装中に手を出さないこと。

- **ペルソナ pod 同士の直接 A2A 呼び出し**。ユーザーがスター型を選択した。相互作用は会議録の共有で実現する
- **A2A の push notification**（webhook）。`AgentCapabilities.PushNotifications = false` で出す
- **A2A の署名付きエージェントカード**（`Signatures`）。v1.0.0 の機能だが鍵管理が必要になる
- **A2A の `tasks/*` メソッドを使った再接続・再購読**。ストリームが切れたらそのラウンドは失敗として扱う
- **gRPC / HTTP+JSON トランスポート**。JSONRPC だけを実装する（前提 8）
- **認証・認可・マルチテナント**。`a2a.AgentCard.SecuritySchemes` は空
- **ペルソナ pod の HPA / オートスケール**。レプリカは固定1
- **Postgres の HA / レプリケーション / バックアップ**。`StatefulSet` 1レプリカ、PVC 1本
- **MinIO の分散モード**。単一ノード
- **知識取り込み API**（アップロード→抽出→チャンク→埋め込み）。引き続き `seed` のみ
- **ペルソナの CRUD API**。`POST/PATCH /api/personas` は廃止し、ペルソナは seed と pod 定義で決まる
- **会議・発言の削除**
- **ハイブリッド検索、会話の自動要約・圧縮**
- **本番向けの TLS / cert-manager**。Ingress は HTTP のみ
- **ペルソナ pod の動的追加 UI**。3体目は YAML を1枚足して `kubectl apply -k`

---

## Step-by-Step Tasks

### Task 1: config に「1インスタンス・複数データベース」の DSN 組み立てを入れる

- **ACTION**: `backend/internal/config/dsn.go` を新規作成し、`config.go` の `DatabaseURL string` を4フィールドに置き換える。
- **IMPLEMENT**:
  - `POSTGRES_BASE_URL`（既定 `postgres://postgres:kaigi@localhost:5432?sslmode=disable`）を1つ読む。
  - `func DatabaseURLFor(baseURL, dbName string) (string, error)`: `url.Parse` → `u.Path = "/" + dbName` → `u.String()`。**クエリ文字列（`sslmode` 等）は保持する**。
  - 論理 DB 名は定数化: `DBMeeting = "kaigi_meeting"` / `DBRegistry = "kaigi_registry"` / `DBPersona = "kaigi_persona"` / `DBKnowledge = "kaigi_knowledge"`。
  - `Config` に `Databases DatabaseConfig` を持たせ、`Meeting` / `Registry` / `Persona` / `Knowledge` の4 DSN を格納。
  - 個別オーバーライドを許す: `MEETING_DATABASE_URL` 等が設定されていればそれを優先（ベース URL からの組み立てより強い）。
  - `Validate()` に「自分が使う DSN が空でないこと」の判定を足す（サービスごとに必要な DSN が違うので、`Validate` は引数で必要セットを受ける形にせず、各 `main` が使う前に空チェックする方針でよい。`Validate` は既存の LLM キーと retrieval 範囲チェックのみ維持）。
- **MIRROR**: `NAMING_CONVENTION`（`env()` ヘルパの再利用）。`config.go:147-188` の `env`/`envInt`/`envFloat`/`envBool` をそのまま使う。
- **IMPORTS**: `net/url`, `fmt`, `os`, `strconv`, `strings`
- **GOTCHA**: `url.Parse("postgres://u:p@h:5432?sslmode=disable")` は `Path` が空。`u.Path = "/" + dbName` で正しく `/kaigi_meeting?sslmode=disable` になる。文字列連結で組み立てると `?sslmode=disable` の前に DB 名を挿す処理を自前で書くことになり必ず壊れる。
- **VALIDATE**: `cd backend && go test ./internal/config/... -run TestDatabaseURLFor -v`

### Task 2: db パッケージをマイグレーション・セット対応にする

- **ACTION**: `backend/internal/db/db.go` を更新。
- **IMPLEMENT**:
  - `//go:embed migrations` に変更（サブディレクトリを含むツリー全体）。
  - `func New(ctx context.Context, databaseURL, set string, log *slog.Logger) (*pgxpool.Pool, error)` — `set` は `"meeting"` / `"registry"` / `"persona"` / `"knowledge"`。
  - `func Migrate(ctx context.Context, pool *pgxpool.Pool, set string, log *slog.Logger) error`。
  - `migrationNames(set string)` は `fs.ReadDir(migrationFS, "migrations/"+set)` を読む。未知の `set` は `fmt.Errorf("db: unknown migration set %q", set)`。
  - `schema_migrations` テーブルはデータベースごとに独立して存在するので、`version` にセット名を含める必要はない。ただしログには出す: `log.Info("migration applied", "set", set, "version", name)`。
- **MIRROR**: `MIGRATION_PATTERN`。`db.go:41-96` の「1マイグレーション=1トランザクション」「適用済みは skip」「失敗時は Rollback」をそのまま維持する。
- **IMPORTS**: 変更なし（`embed`, `io/fs`, `sort` を継続利用）
- **GOTCHA**: `go:embed migrations/*.sql` のままだとサブディレクトリの SQL が埋め込まれず、`migrationNames` は空を返して「マイグレーション0件で正常終了」になる。テーブルが無いまま起動して最初のクエリで落ちるので、**埋め込みパターンの変更を忘れないこと**。`embed.FS` にファイルが入っているかは `fs.ReadDir` の結果件数を `Migrate` の最初にログするか、テストで確認する。
- **VALIDATE**: `cd backend && go build ./internal/db/` が通り、`go test ./internal/db/... -v`（DSN 未設定なら skip）

### Task 3: 4セットのマイグレーションを書く

- **ACTION**: `backend/internal/db/migrations/{meeting,registry,persona,knowledge}/0001_init.sql` を作成し、旧 `0001_init.sql` / `0002_openai_embeddings.sql` を削除。
- **IMPLEMENT**:

`knowledge/0001_init.sql`（旧 `documents` + `chunks`、1536 次元で初版化）:
```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

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

-- byte_start/byte_end locate the chunk inside the original object so a Range
-- GET can read the surrounding text: the chunk is the search unit, the expanded
-- span is the reading unit.
CREATE TABLE chunks (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    document_id uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index int  NOT NULL,
    content     text NOT NULL,
    byte_start  bigint NOT NULL,
    byte_end    bigint NOT NULL,
    embedding   vector(1536) NOT NULL,
    UNIQUE (document_id, chunk_index)
);

-- vector_cosine_ops, so queries must use <=> (cosine distance) to hit the index.
CREATE INDEX chunks_embedding_hnsw
    ON chunks USING hnsw (embedding vector_cosine_ops);
```

`persona/0001_init.sql`（旧 `personas` + `persona_interests`、`slug` 追加）:
```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- slug is new: a persona pod identifies itself by a stable, human-readable name
-- fixed in its Deployment manifest, not by a uuid that changes on every reseed.
CREATE TABLE personas (
    id          uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug        text NOT NULL UNIQUE,
    name        text NOT NULL,
    stance      text NOT NULL DEFAULT '',
    verbosity   text NOT NULL DEFAULT 'balanced'
                  CHECK (verbosity IN ('concise', 'balanced', 'detailed')),
    skepticism  real NOT NULL DEFAULT 0.5
                  CHECK (skepticism >= 0 AND skepticism <= 1),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE persona_interests (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    persona_id uuid NOT NULL REFERENCES personas(id) ON DELETE CASCADE,
    topic      text NOT NULL,
    weight     real NOT NULL CHECK (weight >= -1 AND weight <= 1),
    embedding  vector(1536) NOT NULL
);
CREATE INDEX persona_interests_persona_id ON persona_interests (persona_id);
```

`registry/0001_init.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- The agent card is cached as raw jsonb rather than exploded into columns: it
-- is an A2A wire type owned by the spec, and re-parsing it on read keeps this
-- table from needing a migration every time the card gains a field.
CREATE TABLE agents (
    slug          text PRIMARY KEY,
    base_url      text NOT NULL,
    card          jsonb NOT NULL,
    persona_id    uuid NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agents_last_seen_at ON agents (last_seen_at DESC);
```

`meeting/0001_init.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE meetings (
    id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    topic      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- speaking_order is explicit rather than implied by insertion: the moderator
-- must call participants in a deterministic order for a meeting to be
-- reproducible, and "the order the client sent them" is that order.
CREATE TABLE meeting_participants (
    meeting_id     uuid NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    persona_slug   text NOT NULL,
    persona_name   text NOT NULL,
    speaking_order int  NOT NULL,
    PRIMARY KEY (meeting_id, persona_slug),
    UNIQUE (meeting_id, speaking_order)
);

-- speaker_slug is null for the human's turns. round is 0 for the human's
-- opening topic, 1..n for the persona rounds that answer it.
CREATE TABLE turns (
    id           uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    meeting_id   uuid NOT NULL REFERENCES meetings(id) ON DELETE CASCADE,
    seq          int  NOT NULL,
    round        int  NOT NULL,
    role         text NOT NULL CHECK (role IN ('user', 'persona')),
    speaker_slug text,
    speaker_name text NOT NULL,
    content      text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (meeting_id, seq),
    CHECK ((role = 'user') = (speaker_slug IS NULL))
);

-- chunk_id carries NO foreign key: chunks live in kaigi_knowledge and Postgres
-- cannot reference across databases. title/object_key/scores are denormalised
-- copies so a citation stays readable even if the chunk is later reseeded —
-- which is exactly what the FK would have prevented, and now cannot.
CREATE TABLE turn_citations (
    turn_id         uuid NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    chunk_id        uuid NOT NULL,
    document_id     uuid NOT NULL,
    rank            int  NOT NULL,
    title           text NOT NULL,
    object_key      text NOT NULL,
    relevance_score real NOT NULL,
    affinity_score  real NOT NULL,
    PRIMARY KEY (turn_id, chunk_id)
);
```

- **MIRROR**: `MIGRATION_PATTERN`。設計理由をテーブル上のコメントに書く規約を守る。
- **GOTCHA**: `turns` の `CHECK ((role = 'user') = (speaker_slug IS NULL))` は「人間の発言に話者 slug は無く、ペルソナの発言には必ずある」を DB で表明する。これを入れないと、司会のバグで話者不明の発言が入り込んでも気付けない。
- **VALIDATE**: 4 DB を作った Postgres に対して `go test ./internal/db/...`。各セットが適用され、`chunks.embedding` が `vector(1536)` であること。

### Task 4: Postgres の4データベース初期化

- **ACTION**: `k8s/base/postgres.yaml` の initdb ConfigMap と `docker-compose.yml` の初期化スクリプトを用意する。
- **IMPLEMENT**:
  - `pgvector/pgvector:pg17` は `/docker-entrypoint-initdb.d/*.sql` を初回起動時だけ実行する。以下を置く:
    ```sql
    CREATE DATABASE kaigi_meeting;
    CREATE DATABASE kaigi_registry;
    CREATE DATABASE kaigi_persona;
    CREATE DATABASE kaigi_knowledge;
    ```
  - `POSTGRES_DB` は設定しない（既定の `postgres` データベースが initdb スクリプトの接続先になる）。
  - テーブル・拡張は各サービスの起動時マイグレーションが作る（既存方針の維持）。initdb は `CREATE DATABASE` だけに留める。
- **MIRROR**: `docker-compose.yml:34-47` の postgres サービス定義（healthcheck を含む）。
- **GOTCHA**: initdb スクリプトは**データディレクトリが空のときしか走らない**。既存の PVC / volume が残っていると4 DB が作られず、全サービスが `database "kaigi_meeting" does not exist` で起動失敗する。開発中に DB 構成を変えたら `kubectl delete pvc` / `docker volume rm kaigi_postgres-data` が必要。README とエラーメッセージに書く。
- **GOTCHA**: `CREATE DATABASE` はトランザクション内で実行できない。`docker-entrypoint-initdb.d` の `.sql` は `psql -f` で流れるので問題ないが、Go のマイグレータからは絶対にやらないこと。
- **VALIDATE**: `docker compose up -d postgres` → `docker compose exec postgres psql -U postgres -lqt | cut -d'|' -f1` に4つの `kaigi_*` が並ぶ。

### Task 5: chat.Engine を会話 DB 非依存にする

- **ACTION**: `backend/internal/chat/engine.go` の `Reply` を書き換え、`conversationStore` seam を削除。
- **IMPLEMENT**:
  - 新シグネチャ:
    ```go
    // Turn is one prior statement in the meeting, as seen by a persona. It is
    // passed in rather than loaded: a persona pod holds no conversation state,
    // so the moderator's transcript is the only history that exists.
    type Turn struct {
        SpeakerName string // "user" for the human, the persona's name otherwise
        Role        string // "user" | "persona"
        Content     string
    }

    // Reply runs one turn for persona p against the transcript so far and
    // pushes events to out, closing nothing (the caller owns out).
    func (e *Engine) Reply(ctx context.Context, p persona.Persona, participants []string, transcript []Turn, utterance string, out chan<- Event) error
    ```
  - `NewEngine(llm, searcher, objects, cfg)` — `*Store` 引数を落とす。
  - 本体の手順は**そのまま維持**: `RewriteQueries` → `searcher.Search` → `expandContext` → `out <- Event{Type:"sources"}` → `stream`。
  - 削除するもの: `store.PersonaFor` / `store.History` / `store.AppendMessage` / `store.SaveCitations` の呼び出し、`context.WithoutCancel` の永続化ブロック、`historyTurns`。
  - `Reply` は最後に `out <- Event{Type: "done", Candidates: candidates}` を送る。永続化は司会が行うので、`Event` に `Candidates []retrieval.Candidate` を追加して引用を呼び出し側に返す（`Sources` は presigned URL 付きのクライアント向け、`Candidates` はスコアと chunk_id を持つ永続化向け）。
  - `RewriteQueries` に渡す `history` は `[]Turn` から `[]ChatMessage` 相当に変換する。`prompt.go` の `buildRewritePrompt` も `[]Turn` を取るように直す。
  - LLM に渡すメッセージ組み立て:
    ```go
    messages := make([]ChatMessage, 0, len(transcript)+2)
    messages = append(messages, ChatMessage{Role: "system", Content: BuildSystemPrompt(p, participants)})
    // 会議録は「他者の発言」を含むので assistant/user の2値に素直に写らない。
    // 自分の過去の発言だけ assistant、それ以外（人間 + 他ペルソナ）は user 側に
    // 話者名を付けて流す。こうすると誰の発言かがモデルから見て曖昧にならない。
    for _, t := range transcript {
        if t.Role == "persona" && t.SpeakerName == p.Name {
            messages = append(messages, ChatMessage{Role: "assistant", Content: t.Content})
            continue
        }
        messages = append(messages, ChatMessage{
            Role:    "user",
            Content: fmt.Sprintf("【%s】%s", t.SpeakerName, t.Content),
        })
    }
    messages = append(messages, ChatMessage{Role: "user", Content: expanded + utterance})
    ```
- **MIRROR**: `SERVICE_PATTERN`（`knowledgeSearcher` / `contextExpander` の seam は維持）。`ERROR_HANDLING`（`fmt.Errorf("reply: ...: %w", err)`）。
- **IMPORTS**: `golang.org/x/sync/errgroup` は `expandContext` で継続利用。`github.com/google/uuid` は `conversationID` のログ用途が消えるので不要になれば削除。
- **GOTCHA**: `engine.go:116-126` のコメントが示す通り、**資料ブロックは user ターンに載せる**。system に入れると `BuildSystemPrompt` がターンごとに変わり、OpenAI の自動プレフィックスキャッシュが毎回失効する。会議録も同じ理由で system に入れない（参加者名だけは会議中不変なので system でよい）。
- **VALIDATE**: `cd backend && go test ./internal/chat/... -v`。`engine_test.go` の既存テスト（イベント順序 `sources`→`token`*→`done`）が新シグネチャで通ること。

### Task 6: BuildSystemPrompt に参加者を渡す

- **ACTION**: `backend/internal/chat/prompt.go` の `BuildSystemPrompt` を更新。
- **IMPLEMENT**:
  - `func BuildSystemPrompt(p persona.Persona, participants []string) string`。
  - 既存の出力（立場 / 重視する観点 / 懐疑 / 冗長度 / 資料参照の指示）はそのまま。末尾の資料指示の**前**に会議の文脈を挿入:
    ```go
    if others := othersOf(participants, p.Name); len(others) > 0 {
        fmt.Fprintf(&b, "\nこれは複数人の会議です。他の参加者: %s。"+
            "他の参加者の発言には【名前】が付いています。"+
            "直前の発言に同意・反論する場合は誰のどの点に対してかを明示してください。"+
            "他の参加者の発言を代弁したり、自分の発言として繰り返さないでください。",
            strings.Join(others, "、"))
    }
    ```
  - `othersOf` は自分の名前を除いた**ソート済み**スライスを返す。
- **MIRROR**: `prompt.go:18-60` の組み立て方（`strings.Builder` + `fmt.Fprintf`、日本語の指示文）。
- **GOTCHA**: `participants` の順序が呼び出しごとに揺れると system プロンプトのバイト列が変わり、キャッシュが効かなくなる。**必ずソートする**。`prompt.go:13-17` のコメントが要求している「ターン間でバイト同一」は、会議では「同じ会議の同じペルソナに対してバイト同一」に読み替わる。
- **VALIDATE**: `cd backend && go test ./internal/chat/... -run TestBuildSystemPrompt -v`。参加者順を入れ替えた2回の呼び出しが同一文字列を返すテストを追加する。

### Task 7: internal/a2aconv — A2A ワイヤ型とドメイン型の変換

- **ACTION**: `backend/internal/a2aconv/a2aconv.go` と `card.go` を新規作成。
- **IMPLEMENT**:
  - 会議録のワイヤ形式（司会 → ペルソナ）:
    ```go
    // TranscriptPayload rides in a DataPart alongside the utterance TextPart.
    // The persona pod is stateless, so this is the entire history it gets.
    type TranscriptPayload struct {
        MeetingID    string       `json:"meetingId"`
        Round        int          `json:"round"`
        Participants []string     `json:"participants"`
        Transcript   []TurnWire   `json:"transcript"`
    }

    type TurnWire struct {
        Role        string `json:"role"`        // "user" | "persona"
        SpeakerName string `json:"speakerName"`
        Content     string `json:"content"`
    }
    ```
  - `func NewRequestMessage(p TranscriptPayload, utterance string) *a2a.Message`:
    ```go
    data := a2a.NewDataPart(p)
    data.SetMeta(MetaKind, KindTranscript)
    return a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(utterance), data)
    ```
  - `func TranscriptFrom(msg *a2a.Message) (TranscriptPayload, string, error)` — ペルソナ pod 側。`msg.Parts` を走査し、`kind=transcript` の DataPart を `remarshal` で型付けし、TextPart を発話として返す。
  - `func remarshal(src any, dst any) error { b, err := json.Marshal(src); ...; return json.Unmarshal(b, dst) }`
  - 引用のワイヤ形式（ペルソナ → 司会）:
    ```go
    type CitationWire struct {
        ChunkID    string  `json:"chunkId"`
        DocumentID string  `json:"documentId"`
        Title      string  `json:"title"`
        ObjectKey  string  `json:"objectKey"`
        URL        string  `json:"url"`
        Relevance  float32 `json:"relevance"`
        Affinity   float32 `json:"affinity"`
    }
    type CitationsPayload struct {
        Citations []CitationWire `json:"citations"`
    }
    ```
  - `func NewCitationsPart(cs []CitationWire) *a2a.Part` — `kind=citations` のメタ付き DataPart。
  - `func CitationsFrom(part *a2a.Part) (CitationsPayload, bool, error)` — メタが `citations` でなければ `(zero, false, nil)`。
  - メタキー定数:
    ```go
    const (
        MetaKind       = "kaigi.kind"
        KindTranscript = "transcript"
        KindCitations  = "citations"
    )
    ```
- **MIRROR**: `NAMING_CONVENTION`（パッケージコメント、`New*` コンストラクタ）。`ERROR_HANDLING`（`fmt.Errorf("a2aconv: ...: %w", err)`）。
- **IMPORTS**: `encoding/json`, `fmt`, `github.com/a2aproject/a2a-go/v2/a2a`
- **GOTCHA**: **`p.Data()` の戻りは `any` で、JSON-RPC 越しでは `map[string]any` になる。**`p.Data().(TranscriptPayload)` は同一プロセス内（テスト）では通り、ネットワーク越しでは必ず失敗する。`remarshal` を必ず通すこと。この非対称性がテストをすり抜けるので、`a2aconv_test.go` には「`map[string]any` に落としてから復元する」ケースを必ず入れる。
- **GOTCHA**: `a2a.NewMessage(role, parts...)` は `ID` を自動生成する（`a2a.NewMessageID()`）。自前で採番しないこと。
- **VALIDATE**: `cd backend && go test ./internal/a2aconv/... -v`

### Task 8: a2aconv.PersonaCard — ペルソナのエージェントカード生成

- **ACTION**: `backend/internal/a2aconv/card.go`。
- **IMPLEMENT**:
  ```go
  // PersonaCard renders a persona as the A2A manifest other agents discover it
  // by. Interests become skill tags, which is what makes the registry's card
  // listing useful for a human picking participants.
  func PersonaCard(p persona.Persona, slug, publicURL string) *a2a.AgentCard {
      tags := make([]string, 0, len(p.Personality.Interests))
      for _, in := range p.Personality.Interests {
          if in.Weight > 0 {
              tags = append(tags, in.Topic)
          }
      }
      sort.Strings(tags) // カードは起動ごとにバイト同一であるべき

      return &a2a.AgentCard{
          Name:        p.Name,
          Description: p.Personality.Stance,
          Version:     cardVersion,
          SupportedInterfaces: []*a2a.AgentInterface{
              a2a.NewAgentInterface(publicURL, a2a.TransportProtocolJSONRPC),
          },
          Capabilities: a2a.AgentCapabilities{
              Streaming:         true,
              PushNotifications: false,
              ExtendedAgentCard: false,
          },
          Skills: []a2a.AgentSkill{{
              ID:          "opinion",
              Name:        "会議での意見表明",
              Description: "知識ベースを自分の関心と懐疑度で検索し、会議録を踏まえて意見を述べる",
              Tags:        tags,
              InputModes:  []string{"text/plain"},
              OutputModes: []string{"text/plain"},
          }},
          DefaultInputModes:  []string{"text/plain"},
          DefaultOutputModes: []string{"text/plain"},
      }
  }
  ```
  - `cardVersion` は `const cardVersion = "1.0.0"`。
  - `publicURL` は env `A2A_PUBLIC_URL`（k8s では `http://persona-critic.kaigi.svc.cluster.local:8082`）。
- **MIRROR**: `a2asrv/example_test.go` の `ExampleNewStaticAgentCardHandler`（カードの最小構成）。
- **GOTCHA**: `a2a.NewAgentInterface` が `ProtocolVersion` に `a2a.Version`（`"1.0"`）を入れてくれる。`AgentInterface` を構造体リテラルで直接作ると `ProtocolVersion` が空になり、クライアントのトランスポート選択が失敗する。**必ずコンストラクタを使う**。
- **VALIDATE**: `cd backend && go test ./internal/a2aconv/... -run TestPersonaCard -v`

### Task 9: internal/personaexec — a2asrv.AgentExecutor の実装

- **ACTION**: `backend/internal/personaexec/executor.go`。
- **IMPLEMENT**:
  ```go
  // replyEngine is the narrow seam over *chat.Engine so the event sequence this
  // executor emits can be unit tested without a live OpenAI/Postgres/MinIO stack
  // — the same seam style internal/api uses for chatEngine.
  type replyEngine interface {
      Reply(ctx context.Context, p persona.Persona, participants []string,
          transcript []chat.Turn, utterance string, out chan<- chat.Event) error
  }

  type Executor struct {
      engine  replyEngine
      persona persona.Persona
      log     *slog.Logger
  }

  func New(engine replyEngine, p persona.Persona, log *slog.Logger) *Executor

  func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
      return func(yield func(a2a.Event, error) bool) {
          payload, utterance, err := a2aconv.TranscriptFrom(execCtx.Message)
          if err != nil {
              yield(nil, fmt.Errorf("personaexec: parse request: %w", err))
              return
          }

          if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
              return
          }
          if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
              return
          }

          // yield が false を返す（消費側が離脱した）ときに Reply の goroutine を
          // 宙吊りにしないよう、ここで作った ctx を必ずキャンセルして戻る。
          runCtx, cancel := context.WithCancel(ctx)
          defer cancel()

          events := make(chan chat.Event, 16)
          go func() {
              defer close(events)
              if err := e.engine.Reply(runCtx, e.persona, payload.Participants,
                  toTurns(payload.Transcript), utterance, events); err != nil {
                  e.log.Error("reply failed", "meeting_id", payload.MeetingID, "error", err)
                  events <- chat.Event{Type: "error", Error: err.Error()}
              }
          }()

          replyArtifact := a2a.NewArtifactID()
          for ev := range events {
              switch ev.Type {
              case "sources":
                  part := a2aconv.NewCitationsPart(toCitationWires(ev.Sources))
                  if !yield(a2a.NewArtifactEvent(execCtx, part), nil) {
                      return
                  }
              case "token":
                  if !yield(a2a.NewArtifactUpdateEvent(execCtx, replyArtifact,
                      a2a.NewTextPart(ev.Text)), nil) {
                      return
                  }
              case "error":
                  yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed,
                      a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Error))), nil)
                  return
              case "done":
                  yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
                  return
              }
          }
      }
  }

  func (e *Executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
      return func(yield func(a2a.Event, error) bool) {
          yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
      }
  }
  ```
- **MIRROR**: `SERVICE_PATTERN`（`replyEngine` を利用側に定義）。`api/chat.go:60-67` の「goroutine + channel + defer close」の橋渡し方。`a2asrv/example_test.go` の `ExampleAgentExecutor`（submitted → working → artifact → completed の順序）。
- **IMPORTS**: `context`, `fmt`, `iter`, `log/slog`, `github.com/a2aproject/a2a-go/v2/a2a`, `github.com/a2aproject/a2a-go/v2/a2asrv`, 自リポジトリの `a2aconv` / `chat` / `persona`
- **GOTCHA**: **`yield` の戻り値を必ず見る。** `false` は「消費側が range を抜けた」を意味し、以降 yield してはいけない。無視すると SDK 内部でパニックまたはブロックする。`if !yield(...) { return }` の形を崩さないこと。
- **GOTCHA**: `NewArtifactEvent` は毎回新しい ArtifactID を振る。トークンの追記には `NewArtifactUpdateEvent(execCtx, 同じID, ...)`（`Append: true`）を使う。引用は1回だけなので `NewArtifactEvent` でよい。
- **GOTCHA**: `execCtx` 自体が `a2a.TaskInfoProvider` を満たすので、イベント構築にそのまま渡せる（`TaskID`/`ContextID` を自前で詰めない）。
- **VALIDATE**: `cd backend && go test ./internal/personaexec/... -v`。fake engine で「submitted → working → artifact(citations) → artifact(token)×n → completed」の順序と、`yield` が途中で false を返したとき goroutine が終了すること（`runCtx.Done()` の確認）をテストする。

### Task 10: cmd/persona — A2A サーバの main

- **ACTION**: `backend/cmd/persona/main.go`。
- **IMPLEMENT**:
  - `PERSONA_SLUG`（必須）で自分が誰かを決める。未設定なら起動失敗。
  - 2つの pool を開く:
    ```go
    personaPool, err := db.New(ctx, cfg.Databases.Persona, "persona", log)
    knowledgePool, err := db.New(ctx, cfg.Databases.Knowledge, "knowledge", log)
    ```
  - `persona.Store` に `personaPool`、`retrieval.Searcher` に `knowledgePool` を渡す。
  - `persona.Store` に `GetBySlug(ctx, slug)` を追加（Task 11 と併せて）。起動時に自分のペルソナを1回ロードする。ペルソナ定義は seed 後に変わらない前提なのでキャッシュでよい。行が無ければ起動失敗（`persona %q not found in kaigi_persona; run the seed job first`）。
  - executor と HTTP を組む:
    ```go
    engine := chat.NewEngine(llm, searcher, objects, cfg)
    exec := personaexec.New(engine, p, log)
    handler := a2asrv.NewHandler(exec)
    card := a2aconv.PersonaCard(p, slug, cfg.A2A.PublicURL)

    mux := http.NewServeMux()
    mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
    mux.HandleFunc("GET /healthz", healthz(personaPool, knowledgePool))
    mux.Handle("/", a2asrv.NewJSONRPCHandler(handler))
    ```
  - `registry.Client` を起動し、自己登録＋ハートビートを goroutine で回す（Task 12）。
  - `http.Server` の設定は `cmd/server/main.go:69-78` をそのまま踏襲: `ReadHeaderTimeout: 10s`、**`ReadTimeout`/`WriteTimeout` は未設定**、`IdleTimeout: 60s`、graceful shutdown 10s。
- **MIRROR**: `backend/cmd/server/main.go:22-102` 全体（`main`/`run` 分離、`errCh`、`signal.NotifyContext`、`srv.Shutdown`）。
- **IMPORTS**: `github.com/a2aproject/a2a-go/v2/a2asrv` ほか
- **GOTCHA**: `WriteTimeout` を設定すると A2A の SSE ストリームが途中で切れる。`cmd/server/main.go:73-76` のコメントがまさにこれを説明している。SDK の JSON-RPC ハンドラも SSE を使うので同じ制約がかかる。
- **GOTCHA**: `/healthz` は `/.well-known/...` と `/` の**両方と衝突しない**ように `GET /healthz` のメソッド付きパターンで登録する。`mux.Handle("/", ...)` は最長一致で負けるので順序は問わないが、`net/http` のパターン優先順位（より具体的なパターンが勝つ）を前提にしている。
- **VALIDATE**:
  ```sh
  PERSONA_SLUG=critic go run ./cmd/persona &
  curl -s localhost:8082/.well-known/agent-card.json | jq .name
  curl -s localhost:8082 -X POST -H 'Content-Type: application/json' -d '{
    "jsonrpc":"2.0","id":1,"method":"SendMessage",
    "params":{"message":{"role":"ROLE_USER","parts":[{"text":"合意形成について"}]}}}' | jq .
  ```

### Task 11: persona.Store に GetBySlug を足す

- **ACTION**: `backend/internal/persona/store.go` を更新。
- **IMPLEMENT**:
  - `func (s *Store) GetBySlug(ctx context.Context, slug string) (Persona, error)` — 既存 `Get` と同じ2クエリ構成（personas 1行 + persona_interests 全行）。`pgx.ErrNoRows` → `ErrNotFound`。
  - `CreateInput` に `Slug string` を追加し、`INSERT` に含める。
  - `Persona` 構造体に `Slug string` を追加。
  - 既存 `Get`/`List`/`Update` の SELECT に `slug` を追加。
  - `List` は `ORDER BY slug` を付ける（安定した順序。現状は未指定でノンデターミニスティック）。
- **MIRROR**: `REPOSITORY_PATTERN`、`persona/store.go:112-140` の `Get` 実装。
- **GOTCHA**: `persona_interests.embedding` を `Get` は読んでいない（`chat/store.go:91-94` のコメント参照）。しかしペルソナ pod では **affinity 計算に interest embedding が必要**（`retrieval/search.go:146-164` の `affinity()` が `in.Embedding` を使う）。`GetBySlug` は**埋め込みも読む**こと。ここを落とすと affinity が常に 0 になり、性格重み付けが黙って無効化される — 本機能の中核が壊れるのにエラーは出ない。
- **VALIDATE**: `cd backend && go test ./internal/persona/... -v`。統合テストで `GetBySlug` が `len(Interests[0].Embedding) == 1536` を返すことを確認する。

### Task 12: internal/registry — レジストリのドメイン・永続化・クライアント

- **ACTION**: `backend/internal/registry/{registry.go,store.go,client.go}`。
- **IMPLEMENT**:
  - `registry.go`:
    ```go
    // Agent is one registered persona pod. Present is derived, not stored: a pod
    // that stops heartbeating is absent the moment its TTL lapses, with no
    // writer needed to mark it so.
    type Agent struct {
        Slug       string
        BaseURL    string
        PersonaID  uuid.UUID
        Card       *a2a.AgentCard
        LastSeenAt time.Time
    }

    func (a Agent) Present(now time.Time, ttl time.Duration) bool {
        return now.Sub(a.LastSeenAt) <= ttl
    }

    var ErrNotFound = errors.New("registry: agent not found")
    ```
  - `store.go`: `Upsert(ctx, Agent) error`（`ON CONFLICT (slug) DO UPDATE SET base_url, card, persona_id, last_seen_at = now()`）、`Touch(ctx, slug) error`、`List(ctx) ([]Agent, error)`、`Get(ctx, slug) (Agent, error)`。`card` は `jsonb` なので `json.Marshal`/`Unmarshal` を通す。
  - `client.go`（ペルソナ pod 側）:
    ```go
    // Client keeps this pod's entry in the registry alive. Registration is
    // retried indefinitely rather than fatal: a persona that boots before the
    // discovery pod is ready must not crash-loop, it must keep knocking.
    type Client struct {
        http      *http.Client
        baseURL   string // discovery のURL
        self      Agent
        interval  time.Duration
        log       *slog.Logger
    }

    func (c *Client) Run(ctx context.Context)
    ```
    - `Run` は即座に1回 register し、以降 `interval`（既定 15s）ごとに register を打ち直す。**別の `Touch` エンドポイントは作らない**: register が upsert なので冪等で、エンドポイントが1本で済む。
    - 失敗は `log.Warn` して次の tick を待つ（プロセスは落とさない）。
    - `ctx.Done()` で終了。
- **MIRROR**: `REPOSITORY_PATTERN`。`ERROR_HANDLING`（`ErrNotFound` センチネル、`errors.Is` で拾える形）。
- **IMPORTS**: `encoding/json`, `errors`, `net/http`, `time`, `github.com/a2aproject/a2a-go/v2/a2a`
- **GOTCHA**: TTL（既定 45s）はハートビート間隔（15s）の3倍にする。2倍だと1回の取りこぼしで在席が落ちる。両方 env（`REGISTRY_HEARTBEAT_INTERVAL` / `REGISTRY_TTL`）にし、`Validate` で `ttl > interval*2` を確認する。
- **GOTCHA**: `Card` を `jsonb` で往復させるとき、`a2a.AgentCard` は独自の `MarshalJSON` を持つ型（`TaskState` 等）を含むので、**必ず `encoding/json` を通す**。`pgx` に構造体を直接渡さない。
- **VALIDATE**: `cd backend && go test ./internal/registry/... -v`。`Present` の境界（`LastSeenAt` が ttl ちょうど → true、ttl+1ns → false）を単体で、upsert/List を統合テストで。

### Task 13: internal/agentapi + cmd/discovery

- **ACTION**: `backend/internal/agentapi/agentapi.go` と `backend/cmd/discovery/main.go`。
- **IMPLEMENT**:
  - ルート:
    | Method | Path | 説明 |
    |---|---|---|
    | GET | `/healthz` | `{"status":"ok|degraded","db":"ok"}` |
    | POST | `/registry/agents` | 自己登録。`{slug, baseURL, personaId}` |
    | GET | `/registry/agents` | 登録済み一覧（`present` フラグ付き） |
    | GET | `/registry/agents/{slug}` | 1件 |
  - `POST /registry/agents` の処理:
    1. リクエストを検証（`slug` / `baseURL` 必須、`baseURL` は `url.Parse` が通ること）。
    2. **discovery 側からカードを解決する**: `agentcard.DefaultResolver.Resolve(ctx, baseURL)`。
    3. `store.Upsert(ctx, Agent{...Card: card})`。
    4. `201 Created` + 解決したカードを返す。
  - `GET /registry/agents` のレスポンス DTO:
    ```go
    type agentDTO struct {
        Slug      string          `json:"slug"`
        Name      string          `json:"name"`
        PersonaID string          `json:"personaId"`
        BaseURL   string          `json:"baseUrl"`
        Present   bool            `json:"present"`
        Skills    []skillDTO      `json:"skills"`
        Card      *a2a.AgentCard  `json:"card"`
    }
    ```
  - ルーティング・ミドルウェアは `internal/api` と同じ形（`requestLogger` + `cors`）。`writeJSON` も同形で再実装する（パッケージを跨いで共有しない — `api` は moderator 専用にする）。
- **MIRROR**: `api/api.go:44-63`（`NewHandler(cfg, Deps)` + `http.NewServeMux` のメソッド付きパターン）。`api/api.go:69-100`（health の degraded 方針: 依存が落ちていても 5xx にしない）。`ERROR_HANDLING` の HTTP 版。
- **IMPORTS**: `github.com/a2aproject/a2a-go/v2/a2aclient/agentcard`
- **GOTCHA**: `agentcard.DefaultResolver.Resolve(ctx, baseURL)` に渡すのは **base URL**（`http://persona-critic:8082`）。`/.well-known/agent-card.json` は内部で付加される。フルパスを渡すと `/.well-known/agent-card.json/.well-known/agent-card.json` を引いて 404 になる。
- **GOTCHA**: カードの解決を登録時にしか行わないと、ペルソナの定義変更が反映されない。ハートビートは15秒ごとに register を打つので、**register のたびに再解決される**。これは意図した設計だが、discovery → persona への HTTP が15秒×N本走る。`DefaultResolver` のタイムアウトは30秒なので、ペルソナが応答不能だと登録ハンドラが30秒ブロックする。`agentcard.NewResolver(&http.Client{Timeout: 5 * time.Second})` で独自クライアントを渡すこと。
- **VALIDATE**:
  ```sh
  go run ./cmd/discovery &
  curl -s localhost:8081/registry/agents | jq .
  curl -s -X POST localhost:8081/registry/agents -H 'Content-Type: application/json' \
    -d '{"slug":"critic","baseUrl":"http://localhost:8082","personaId":"..."}' | jq .
  ```

### Task 14: internal/meeting — ドメイン型と永続化

- **ACTION**: `backend/internal/meeting/{meeting.go,store.go}`。
- **IMPLEMENT**:
  - `meeting.go`:
    ```go
    type Participant struct {
        Slug          string
        Name          string
        BaseURL       string
        SpeakingOrder int
    }

    type Turn struct {
        ID          uuid.UUID
        Seq         int
        Round       int
        Role        string // "user" | "persona"
        SpeakerSlug string // "" for the human
        SpeakerName string
        Content     string
        Citations   []Citation
        CreatedAt   time.Time
    }

    type Citation struct {
        ChunkID    uuid.UUID
        DocumentID uuid.UUID
        Rank       int
        Title      string
        ObjectKey  string
        URL        string
        Relevance  float32
        Affinity   float32
    }

    type Meeting struct {
        ID           uuid.UUID
        Topic        string
        Participants []Participant
        Turns        []Turn
    }

    // Event is one unit of progress the HTTP layer turns into an SSE frame.
    // Unlike chat.Event it carries a speaker: a meeting has many.
    type Event struct {
        Type        string     `json:"type"` // speaker_start|sources|token|speaker_end|speaker_error|round_end|done|error
        Round       int        `json:"round,omitempty"`
        PersonaSlug string     `json:"personaSlug,omitempty"`
        PersonaName string     `json:"personaName,omitempty"`
        Text        string     `json:"text,omitempty"`
        Citations   []Citation `json:"citations,omitempty"`
        Turn        *Turn      `json:"turn,omitempty"`
        Error       string     `json:"error,omitempty"`
    }

    var (
        ErrMeetingNotFound = errors.New("meeting: not found")
        ErrNoParticipants  = errors.New("meeting: at least one participant is required")
    )
    ```
  - `store.go`: `Create(ctx, topic string, ps []Participant) (uuid.UUID, error)`（`meetings` + `meeting_participants` を1トランザクションで）、`Get(ctx, id) (Meeting, error)`、`Transcript(ctx, id) ([]Turn, error)`、`AppendTurn(ctx, id uuid.UUID, t Turn) (Turn, error)`（`seq` は `SELECT coalesce(max(seq),-1)+1` をトランザクション内で取る）、`SaveCitations(ctx, turnID, []Citation) error`。
- **MIRROR**: `REPOSITORY_PATTERN`（`tx` + `defer Rollback`、生 SQL）。`chat/store.go:50-140` の構造をほぼ写す。
- **GOTCHA**: `seq` の採番を `max(seq)+1` でやる場合、同一会議に対する同時 `AppendTurn` が競合する。`turns` の `UNIQUE (meeting_id, seq)` が守ってくれるが、**エラーになる**。会議は司会1本が逐次で書くので実際には競合しないが、`AppendTurn` は明示的に「1会議に対する書き込みは司会が直列化する前提」とコメントすること。
- **GOTCHA**: `turn_citations` に FK が無いので、`chunk_id` が実在するかは誰も検証しない（Task 3 の設計判断）。`SaveCitations` はそれを前提に、`rank` の連番だけ自分で保証する。
- **VALIDATE**: `cd backend && go test ./internal/meeting/... -v`（`TEST_MEETING_DATABASE_URL` gate）。Create → AppendTurn×3 → Get で `seq` が 0,1,2 と並び、`Participants` が `speaking_order` 順であることを確認。

### Task 15: internal/meeting/moderator.go — ラウンド進行

- **ACTION**: `backend/internal/meeting/moderator.go`。**本機能の中核。**
- **IMPLEMENT**:
  ```go
  // agentClient is the narrow seam over *a2aclient.Client so the round loop —
  // the core of what makes this a meeting rather than N independent answers —
  // can be unit tested without live persona pods.
  type agentClient interface {
      SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
  }

  // agentDialer resolves a participant to a client. Separate from agentClient so
  // a fake can hand back per-persona scripted streams.
  type agentDialer interface {
      Dial(ctx context.Context, p Participant) (agentClient, error)
  }

  type Moderator struct {
      store     *Store
      dialer    agentDialer
      log       *slog.Logger
      maxRounds int
  }

  // Run drives the whole meeting turn: for each round, every participant speaks
  // once, in speaking_order, and each one receives every statement made before
  // it — including statements made earlier in the same round. That is what makes
  // personas react to each other, so the participant loop MUST stay sequential.
  func (m *Moderator) Run(ctx context.Context, meetingID uuid.UUID, utterance string, rounds int, out chan<- Event) error
  ```
  - 手順:
    1. `rounds` を `1..m.maxRounds` にクランプ。
    2. `mtg, err := m.store.Get(ctx, meetingID)`。参加者0なら `ErrNoParticipants`。
    3. 人間の発言を `AppendTurn`（`Round: 0`, `Role: "user"`, `SpeakerName: "user"`）。**生成前に保存する**（`engine.go:97-101` と同じ理由: クライアントが切れても発話は失われない）。
    4. `for round := 1; round <= rounds; round++ { for _, p := range mtg.Participants { ... } }`
    5. 各参加者について:
       - `out <- Event{Type: "speaker_start", Round: round, PersonaSlug: p.Slug, PersonaName: p.Name}`
       - `transcript, _ := m.store.Transcript(ctx, meetingID)` — **毎回読み直す**。直前の参加者の発言を含める必要がある。
       - `payload := a2aconv.TranscriptPayload{MeetingID: ..., Round: round, Participants: names, Transcript: toWire(transcript)}`
       - `msg := a2aconv.NewRequestMessage(payload, utterance)`
       - `client, err := m.dialer.Dial(ctx, p)` — 失敗は `speaker_error` を出して**次の参加者へ進む**
       - `for ev, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: msg})` でイベントを回し、型スイッチ:
         - `*a2a.TaskArtifactUpdateEvent`: パートを見る。`kind=citations` の DataPart → `out <- Event{Type:"sources", Citations: ...}`。TextPart → `out <- Event{Type:"token", Text: p.Text()}` かつ `sb.WriteString`
         - `*a2a.TaskStatusUpdateEvent`: `TaskStateFailed` → `speaker_error` で中断、`TaskStateCompleted` → ループ終了
         - `*a2a.Message`: 非ストリームの完了応答。パートのテキストを全部 `sb` に入れて完了扱い
         - `*a2a.Task`: 初期状態。無視
       - 完了後、`context.WithoutCancel(ctx)` で `AppendTurn` + `SaveCitations`（`engine.go:133-143` と同じ理由）
       - `out <- Event{Type: "speaker_end", Turn: &turn, ...}`
    6. ラウンド末に `out <- Event{Type: "round_end", Round: round}`
    7. 最後に `out <- Event{Type: "done"}`
  - 1参加者の失敗（Dial 失敗 / ストリームエラー / `TaskStateFailed`）は `speaker_error` イベントにして**会議は続行**する。`Run` が error を返すのは会議自体が進められないとき（会議が無い、参加者0、人間の発言が保存できない）だけ。
- **MIRROR**: `engine.go:86-147` の `Reply`（イベントを channel に押し、永続化は `WithoutCancel`）。`SERVICE_PATTERN`。
- **IMPORTS**: `iter`, `strings`, `github.com/a2aproject/a2a-go/v2/a2a`
- **GOTCHA**: **参加者ループを並列化してはいけない。** `errgroup` で回すと各ペルソナが同じ会議録（前ラウンドまで）を見ることになり、互いの発言に反応しない N 本の独立回答になる。`expandContext` の並列化（`engine.go:189-209`）とは話が違う — あちらは独立な Range GET、こちらは因果のある逐次列。
- **GOTCHA**: `Transcript` をラウンド開始時に1回読んで使い回すと、同じラウンドの先行発言が落ちる。**参加者ごとに読み直す。** N×rounds 回のクエリが走るが、1会議の規模ではコストではない。
- **GOTCHA**: `iter.Seq2` の range 内で `return` すると、SDK 側のイテレータに「消費側が離脱した」が伝わる。これは正しい振る舞い（ペルソナ側の `yield` が false を受け取って goroutine を畳む）。**ただし range を抜けた後にストリームの残りを読もうとしないこと。**
- **VALIDATE**: `cd backend && go test ./internal/meeting/... -run TestModeratorRounds -v`
  - **これが本機能の受け入れ条件そのもの**: fake dialer に2体のペルソナを登録し、2番目のペルソナが受け取った `TranscriptPayload` に**1番目のペルソナの発言が含まれている**ことを検証する。これが通らなければ「ペルソナが相互にやりとりする」は実現していない。
  - ラウンド2の1番目のペルソナが、ラウンド1の全発言を受け取っていることも検証する。

### Task 16: internal/meeting の A2A ダイアラ

- **ACTION**: `backend/internal/meeting/dialer.go`。
- **IMPLEMENT**:
  ```go
  // A2ADialer builds an a2aclient per participant from the card the registry
  // resolved. Clients are cached by slug: NewFromCard does transport
  // negotiation, which is wasted work to repeat on every turn.
  type A2ADialer struct {
      mu      sync.Mutex
      clients map[string]*a2aclient.Client
      cards   cardSource // discovery から引く
      http    *http.Client
  }

  func (d *A2ADialer) Dial(ctx context.Context, p Participant) (agentClient, error) {
      // キャッシュヒットなら返す。無ければ:
      //   card := d.cards.Card(ctx, p.Slug)
      //   client, err := a2aclient.NewFromCard(ctx, card,
      //       a2aclient.WithJSONRPCTransport(d.http))
  }
  ```
  - `cardSource` は discovery の `GET /registry/agents/{slug}` を叩く薄いクライアント（`internal/registry` の HTTP クライアント側に置いてもよい）。
  - `http.Client` のタイムアウトは**設定しない**（ストリーミングなので絶対期限は張れない）。代わりに `Transport` に `ResponseHeaderTimeout: 30 * time.Second` を設定する。
- **MIRROR**: `objectstore/store.go:29-57`（2つのクライアントを作り分ける発想と、なぜそうするかのコメント）。
- **GOTCHA**: `&http.Client{Timeout: 30*time.Second}` を SSE ストリームに使うと、**30秒でストリームが切れる**。`Timeout` はレスポンス body の読み切りまでを含む絶対期限。`ResponseHeaderTimeout` はヘッダまでしか見ないので安全。`a2aclient/example_test.go` の例は `Timeout: 30s` を使っているが、あれは非ストリーミングの `SendMessage` の例である。
- **GOTCHA**: ペルソナ pod が再起動するとカードの URL は変わらないが、キャッシュしたクライアントのコネクションは死ぬ。`SendStreamingMessage` が接続エラーを返したら該当 slug のキャッシュを捨てる。
- **VALIDATE**: `cd backend && go vet ./internal/meeting/`。実挙動は Task 22 の E2E で確認。

### Task 17: internal/api を moderator の REST+SSE に書き換える

- **ACTION**: `backend/internal/api/api.go` / `meeting.go`（新規）を更新し、`persona.go` / `conversation.go` / `chat.go` を差し替え。
- **IMPLEMENT**:
  - ルート:
    | Method | Path | 説明 |
    |---|---|---|
    | GET | `/api/health` | `{"status","db","discovery"}` |
    | GET | `/api/personas` | discovery の一覧を persona DTO に変換（`present` 付き） |
    | POST | `/api/meetings` | `{topic, personaSlugs[]}` → 会議作成 |
    | GET | `/api/meetings/{id}` | 会議と全発言 |
    | POST | `/api/meetings/{id}/turns` | `{content, rounds}` → SSE |
    | GET | `/api/documents/{id}` | 既存どおり presigned URL へリダイレクト |
  - `Deps` を差し替え:
    ```go
    type Deps struct {
        Log       *slog.Logger
        Pool      *pgxpool.Pool      // kaigi_meeting
        Meetings  *meeting.Store
        Moderator moderatorRunner
        Agents    agentLister         // discovery クライアント
        Objects   *objectstore.Store
    }

    // moderatorRunner is the one seam interfaced here (satisfied by
    // *meeting.Moderator): the SSE handler's frame-by-frame behavior needs to be
    // testable without live persona pods.
    type moderatorRunner interface {
        Run(ctx context.Context, meetingID uuid.UUID, utterance string, rounds int, out chan<- meeting.Event) error
    }
    ```
  - `POST /api/meetings` は `personaSlugs` を discovery で検証する。在席していない slug は `400` で `{"error":"persona %q is not present"}`。参加者の `speakingOrder` はリクエストの配列順。
  - SSE ハンドラは `api/chat.go` を**ほぼそのまま**流用し、`chat.Event` → `meeting.Event` に差し替える。`keepAliveInterval` / `frameWriteTimeout` / `writeSSE` はそのまま。
  - `/api/documents/{id}` は `chat.Store.DocumentObjectKey` を使っていたが、それは `kaigi_knowledge` のテーブルを引く。moderator は knowledge DB に繋がない設計なので、**このルートは削除する**。引用の presigned URL は `sources` イベントで既に渡っている（`conversation.go:91-95` のコメントが「通常フローでは使わない」と明言している）。
- **MIRROR**: `api/api.go:20-63`、`api/chat.go:14-110` をそのまま。`api/middleware.go` は無変更で流用（`statusRecorder.Unwrap` が必須）。
- **GOTCHA**: `/api/documents/{id}` を削除したら、`objectstore.Store` を moderator が持つ理由が無くなる。`Deps.Objects` も落とす。presigned URL はペルソナ pod 側で生成され、引用ペイロードに載って司会を通過するだけになる。
- **GOTCHA**: `statusRecorder` に `Unwrap` が無いと SSE が黙ってバッファされる（`middleware.go:43-47`）。ミドルウェアを新規に書かずファイルごと流用すること。
- **VALIDATE**: `cd backend && go test ./internal/api/... -v`。fake `moderatorRunner` で `speaker_start`→`token`→`speaker_end`→`done` のフレームが順序どおり出ることを検証する（既存の `fakeChatEngine` テストを写す）。

### Task 18: cmd/moderator と cmd/server の削除

- **ACTION**: `backend/cmd/moderator/main.go` を作成、`backend/cmd/server/main.go` を削除。
- **IMPLEMENT**:
  - `db.New(ctx, cfg.Databases.Meeting, "meeting", log)` の1 pool だけ。
  - `meeting.NewStore(pool)`、`meeting.NewA2ADialer(...)`、`meeting.NewModerator(store, dialer, log, cfg.Meeting.MaxRounds)`。
  - discovery クライアント（`GET /registry/agents`）を組む。
  - `api.NewHandler(cfg, api.Deps{...})`。
  - `http.Server` は `cmd/server/main.go:69-78` と同一設定。
  - **`cfg.Validate()` は呼ばない**: moderator は OpenAI キーも retrieval 設定も使わない。代わりに `cfg.Databases.Meeting != ""` と `cfg.Registry.URL != ""` を確認する。
  - `git rm backend/cmd/server/main.go`。
- **MIRROR**: `cmd/server/main.go:22-102` 全体。
- **GOTCHA**: `config.Validate()` が `OPENAI_API_KEY` 必須を強制している（`config.go:128-131`）。moderator と discovery はキーを持たないので、**`Validate` を全サービスで呼ぶと起動できない**。`Validate` はペルソナ pod と seed だけが呼ぶ。この分岐をコメントで明記する。
- **VALIDATE**: `cd backend && go build ./... && go vet ./...`。`grep -rn "cmd/server" .` が Makefile / Dockerfile / README に残っていないこと。

### Task 19: cmd/seed を分割データベースに対応させる

- **ACTION**: `backend/cmd/seed/main.go` を更新。
- **IMPLEMENT**:
  - 2つの pool: `personaPool`（`"persona"` セット）と `knowledgePool`（`"knowledge"` セット）。
  - `seedDocuments` は `knowledgePool`、`seedPersonas` は `personaPool`。
  - `seedPersonas` のフィクスチャに `Slug` を足す: `critic` / `pragmatist`。**k8s の Deployment の `PERSONA_SLUG` と一致していなければペルソナ pod が起動しない**ので、slug は seed とマニフェストの契約である旨をコメントに書く。
  - 冪等性は維持（`slug` の UNIQUE 制約で判定できるようになったので、名前一致より堅い）。
- **MIRROR**: `cmd/seed/main.go:45-73` の `run` 構造。
- **GOTCHA**: `retrieval.NewEmbedder(cfg.Embed)` は OpenAI を叩くので seed には `OPENAI_API_KEY` が必要。seed だけは `cfg.Validate()` を呼ぶ。
- **VALIDATE**: `make docker-seed` 相当を2回実行して、`personas` が2行のままであること。

### Task 20: Dockerfile を3バイナリに

- **ACTION**: `backend/Dockerfile` を更新。
- **IMPLEMENT**:
  ```dockerfile
  RUN CGO_ENABLED=0 go build -o /out/moderator ./cmd/moderator && \
      CGO_ENABLED=0 go build -o /out/discovery ./cmd/discovery && \
      CGO_ENABLED=0 go build -o /out/persona   ./cmd/persona && \
      CGO_ENABLED=0 go build -o /out/seed      ./cmd/seed
  ```
  - 4バイナリを1イメージに入れ、k8s 側は `command: ["/app/persona"]` で選ぶ。`ENTRYPOINT` は置かない（どのバイナリも「既定」ではないので、明示させる）。
  - `COPY testdata /app/testdata` は維持（seed が相対パスで読む）。
  - `EXPOSE` は書かない（バイナリごとにポートが違う）。
- **MIRROR**: `backend/Dockerfile:1-33`（マルチステージ、`go mod download` のキャッシュ分離、distroless/static、exec 形式）。
- **GOTCHA**: 現行 compose の `seed` サービスは `entrypoint: ["/app/seed"]` で ENTRYPOINT を**上書き**している（`docker-compose.yml:108-110` のコメント参照）。ENTRYPOINT を消すので、compose の全サービスで `command` を明示する必要がある。
- **VALIDATE**: `docker build -t kaigi-backend ./backend` が成功し、`docker run --rm --entrypoint /app/moderator kaigi-backend` が DB 接続エラーで落ちる（＝バイナリが存在する）。

### Task 21: docker-compose を新トポロジに書き換える

- **ACTION**: `docker-compose.yml` を更新。
- **IMPLEMENT**:
  - サービス: `postgres`（initdb で4 DB）、`minio`、`minio-init`、`discovery`、`persona-critic`、`persona-pragmatist`、`moderator`、`frontend`、`seed`（profile）。
  - 共通 env アンカー:
    ```yaml
    x-postgres-env: &postgres-env
      POSTGRES_BASE_URL: postgres://postgres:kaigi@postgres:5432?sslmode=disable

    x-persona-env: &persona-env
      <<: *postgres-env
      MINIO_ENDPOINT: minio:9000
      MINIO_PUBLIC_ENDPOINT: localhost:9000
      REGISTRY_URL: http://discovery:8081
    ```
  - ペルソナは2サービス。差分は `PERSONA_SLUG` と `A2A_PUBLIC_URL` のみ:
    ```yaml
    persona-critic:
      build: ./backend
      command: ["/app/persona"]
      environment:
        <<: *persona-env
        PERSONA_SLUG: critic
        A2A_PUBLIC_URL: http://persona-critic:8082
        ADDR: ":8082"
    ```
  - `moderator` は `ADDR: ":8080"` を 8080 で publish。`frontend` は 5173→80。
  - ペルソナは `depends_on: discovery`（`service_started` で可。自己登録はリトライするので healthy を待つ必要はない）。
- **MIRROR**: `docker-compose.yml:14-31` のアンカーと `depends_on` の使い方、冒頭のコメント（本番構成ではないことの明示）。
- **GOTCHA**: `A2A_PUBLIC_URL` は「**他のコンテナから見た自分の URL**」。`localhost:8082` にすると discovery がカードを解決できず、司会も接続できない。compose ではサービス名、k8s では Service の FQDN。この値を間違えると「登録は成功するがカード解決だけ失敗する」という分かりにくい壊れ方をするので、`.env.example` と README に明記する。
- **VALIDATE**: `docker compose up -d --build` → `curl -s localhost:8081/registry/agents | jq '.[].slug'` が `critic` と `pragmatist` を返す。

### Task 22: k8s マニフェスト（base）

- **ACTION**: `k8s/base/*.yaml` + `k8s/base/personas/*.yaml` + `kustomization.yaml`。
- **IMPLEMENT**:
  - `namespace.yaml`: namespace `kaigi`。
  - `config.yaml` ConfigMap `kaigi-config`: `POSTGRES_BASE_URL`, `MINIO_ENDPOINT`, `MINIO_PUBLIC_ENDPOINT`, `MINIO_BUCKET`, `MINIO_REGION`, `REGISTRY_URL`, `CHAT_MODEL`, `EMBED_MODEL`, retrieval チューニング一式, `MEETING_MAX_ROUNDS`。
  - `secret.yaml` Secret `kaigi-secrets`: `OPENAI_API_KEY`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `POSTGRES_PASSWORD`。**base には空値のテンプレートを置き、overlay で上書きする**。
  - `postgres.yaml`: StatefulSet（`pgvector/pgvector:pg17`、PVC 5Gi、`/docker-entrypoint-initdb.d` に ConfigMap をマウント、`pg_isready` の readiness/liveness）+ headless Service。
  - `minio.yaml`: StatefulSet + Service（9000/9001）。
  - `minio-init-job.yaml`: `minio/mc` の Job。バケット作成。
  - `discovery.yaml`: Deployment（`command: ["/app/discovery"]`, `ADDR: ":8081"`）+ Service。
  - `personas/persona-critic.yaml`: Deployment + Service。
    ```yaml
    # 3体目を追加するときはこのファイルをコピーし、以下4箇所だけ変える:
    #   metadata.name / selector / template.labels の persona-<slug>
    #   PERSONA_SLUG
    #   A2A_PUBLIC_URL のホスト名
    #   Service の metadata.name と selector
    # そして kustomization.yaml の resources に足す。
    # slug は seed のフィクスチャ（backend/cmd/seed/main.go）と一致していること。
    ```
  - `frontend.yaml`: Deployment 2コンテナ（`web`: frontend イメージ、`moderator`: backend イメージ `command: ["/app/moderator"]`, `ADDR: ":8080"`）+ Service（80）。
  - `ingress.yaml`: ingress-nginx。SSE 用アノテーション:
    ```yaml
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    ```
  - `seed-job.yaml`: Job（`command: ["/app/seed"]`, `restartPolicy: Never`, `backoffLimit: 3`）。
  - 全 Deployment に readiness probe（`GET /healthz` or `/api/health`）と `resources.requests`。
- **MIRROR**: `docker-compose.yml` の env 分担（何をサービス名にし、何を公開エンドポイントにするか）をそのまま k8s の Service FQDN に写す。
- **GOTCHA**: **Ingress のバッファリングを切らないと SSE が動かない。** `proxy-buffering: off` が無いと、nginx がレスポンスを溜め込み、トークンが会議終了時に一括で届く。`Caddyfile:5-10` が同じ問題に触れている。
- **GOTCHA**: `MINIO_PUBLIC_ENDPOINT` は**ブラウザから解決できる名前**でなければならない（presigned URL に焼かれる）。クラスタ内 `minio:9000` にすると引用リンクが全部死ぬ。Ingress でホスト名を生やすか、`kubectl port-forward` 前提の `localhost:9000` にする。overlay で切り替える。
- **GOTCHA**: `minio-init` Job と `seed` Job は完了後も Pod が `Completed` で残る。`kubectl apply -k` を再実行すると Job の spec は immutable なので更新が弾かれる。`ttlSecondsAfterFinished: 300` を付け、再実行時は `kubectl delete job` してから apply する手順を README に書く。
- **VALIDATE**: `kubectl apply -k k8s/base --dry-run=client` がエラーなく通る。`kustomize build k8s/base | kubectl apply --dry-run=server -f -`（クラスタ接続時）。

### Task 23: k8s overlay と kind クラスタ

- **ACTION**: `k8s/overlays/local/kustomization.yaml`、`k8s/kind-cluster.yaml`。
- **IMPLEMENT**:
  - `kind-cluster.yaml`:
    ```yaml
    kind: Cluster
    apiVersion: kind.x-k8s.io/v1alpha4
    nodes:
      - role: control-plane
        kubeadmConfigPatches:
          - |
            kind: InitConfiguration
            nodeRegistration:
              kubeletExtraArgs:
                node-labels: "ingress-ready=true"
        extraPortMappings:
          - containerPort: 80
            hostPort: 80
            protocol: TCP
          - containerPort: 30900   # MinIO を NodePort で出す（presigned URL 用）
            hostPort: 9000
            protocol: TCP
    ```
  - `overlays/local/kustomization.yaml`:
    - `namespace: kaigi`
    - `images:` で `kaigi-backend` / `kaigi-frontend` を `newTag: local` に固定
    - `patches` で全 Deployment に `imagePullPolicy: Never`（kind load したイメージを使い、レジストリを引きに行かせない）
    - `configMapGenerator` で `MINIO_PUBLIC_ENDPOINT=localhost:9000` を上書き
    - `secretGenerator` で `OPENAI_API_KEY` を `.env` から `envs:` 参照
  - MinIO の Service を `type: NodePort` `nodePort: 30900` にする patch。
- **GOTCHA**: `imagePullPolicy: Never` を付けないと、kind は `kaigi-backend:local` を Docker Hub に探しに行って `ImagePullBackOff` になる。`kind load docker-image` でノードに入れたイメージでも同じ。
- **GOTCHA**: `secretGenerator` の `envs:` はファイルをリポジトリ外に置くこと。`.gitignore` に `k8s/overlays/local/.env` を追加する。
- **VALIDATE**:
  ```sh
  kind create cluster --config k8s/kind-cluster.yaml --name kaigi
  kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
  make kind-load
  kubectl apply -k k8s/overlays/local
  kubectl -n kaigi get pods   # 全部 Running/Completed
  ```

### Task 24: フロントエンドの API クライアント

- **ACTION**: `frontend/src/api/client.ts` を更新。
- **IMPLEMENT**:
  - 型:
    ```ts
    export type Agent = {
      slug: string
      name: string
      personaId: string
      present: boolean
      skills: { id: string; name: string; tags: string[] }[]
    }

    export type Citation = {
      chunkId: string
      documentId: string
      rank: number
      title: string
      url: string
      relevance: number
      affinity: number
    }

    export type Turn = {
      id: string
      seq: number
      round: number
      role: 'user' | 'persona'
      speakerSlug: string
      speakerName: string
      content: string
      citations?: Citation[]
      createdAt: string
    }

    export type Meeting = {
      id: string
      topic: string
      participants: { slug: string; name: string; speakingOrder: number }[]
      turns: Turn[]
    }

    export type MeetingEvent =
      | { type: 'speaker_start'; round: number; personaSlug: string; personaName: string }
      | { type: 'sources'; personaSlug: string; citations: Citation[] }
      | { type: 'token'; personaSlug: string; text: string }
      | { type: 'speaker_end'; personaSlug: string; turn: Turn }
      | { type: 'speaker_error'; personaSlug: string; personaName: string; error: string }
      | { type: 'round_end'; round: number }
      | { type: 'done' }
      | { type: 'error'; error: string }
    ```
  - 関数: `listAgents`、`createMeeting(topic, personaSlugs, signal)`、`getMeeting(id, signal)`、`async function* sendTurn(meetingId, content, rounds, signal)`。
  - **`sendMessage` の SSE パーサ（`client.ts:84-121`）はそのまま流用する**。`ChatEvent` → `MeetingEvent` に型を差し替えるだけ。フレーム形式は変わっていない。
- **MIRROR**: `FRONTEND_PATTERN`。`client.ts:63-68` のコメント（なぜ `EventSource` ではなく fetch なのか）は `sendTurn` にそのまま持ち越す。
- **GOTCHA**: `parseSSEFrame` は `event:` 行を無視して `data:` だけ見る（`client.ts:116-118`）。サーバ側も `data` の JSON に `type` を含めているのでこれで動く。新イベント型を足すときも**必ず `data` の JSON に `type` を入れる**こと。
- **VALIDATE**: `cd frontend && npm run build`（`tsc -b` が型エラーなし）

### Task 25: フロントエンドの会議 UI

- **ACTION**: `frontend/src/App.tsx` / `App.css` を更新。
- **IMPLEMENT**:
  - state: `agents`, `selectedSlugs: Set<string>`, `rounds: number`, `meetingId`, `turns: DisplayTurn[]`, `activeSpeaker`, `sending`, `error`。
  - `DisplayTurn = Turn & { pending?: boolean }` — ストリーム中の発言は `pending: true` で1エントリを確保し、`token` で `content` を伸ばす（現行 `App.tsx:86-114` の `assistantIndex` 方式を話者ごとに一般化する）。
  - 参加者は複数選択のチェックボックス。`present: false` の agent は `disabled` + 「不在」バッジ。
  - 「開始」で `createMeeting` → `sendTurn`。イベント処理:
    - `speaker_start`: `pending: true` の空の発言を push し、`activeSpeaker` を立てる
    - `sources`: その発言に `citations` を入れる
    - `token`: `content` を追記
    - `speaker_end`: サーバから来た `turn` で置き換え（`pending` を落とす）
    - `speaker_error`: その発言をエラー表示に置き換え、会議は続行
    - `round_end`: ラウンド区切りを push
  - ラウンド区切りは発言リスト内の疑似要素として描く（`round` の変化で `<hr>` + ラベル）。
  - `AbortController` の使い方は現行どおり（`App.tsx:30-33` の StrictMode 二重実行対策）。
- **MIRROR**: `App.tsx:71-130` の「同一リスト entry にトークンを積む」方式。`App.tsx:34-50` の effect + `AbortController` パターン。
- **GOTCHA**: React 19 StrictMode で effect が二重に走る。`createMeeting` を effect で呼ぶと会議が2つできる。**会議作成はユーザー操作（ボタン）でのみ行う**（現行の「ペルソナ選択 effect で会話を作る」設計から変える）。
- **GOTCHA**: `setTurns` 内で `turns.length` をインデックス取得に使うと、React のバッチ更新で前の `setTurns` が反映されていない可能性がある。`setTurns(prev => ...)` の中でインデックスを決めること（現行コードの `assistantIndex` も同じ罠を抱えており、`App.tsx:90-93` は関数形式で回避している）。
- **VALIDATE**: `cd frontend && npm run build && npm run lint`。ブラウザで2ペルソナ×2ラウンドを実行し、話者が交互に現れることを目視確認。

### Task 26: Makefile / .env.example / README

- **ACTION**: 3ファイルを更新。
- **IMPLEMENT**:
  - Makefile 追加ターゲット:
    ```make
    kind-up:     ## kind クラスタを作り ingress-nginx を入れる
    kind-load:   ## backend/frontend イメージをビルドして kind に load
    k8s-up:      ## overlays/local を apply して Pod が Ready になるまで待つ
    k8s-seed:    ## seed Job を作り直して実行
    k8s-logs:    ## moderator/discovery/persona のログを追う
    k8s-down:    ## namespace を削除
    kind-down:   ## kind クラスタを削除
    ```
  - `dev` を4プロセスに:
    ```make
    dev: up
    	@trap 'kill 0' EXIT INT TERM; \
    	$(MAKE) dev-discovery & \
    	sleep 2; \
    	$(MAKE) dev-persona-critic & \
    	$(MAKE) dev-persona-pragmatist & \
    	$(MAKE) dev-moderator & \
    	$(MAKE) dev-frontend & \
    	wait
    ```
    ポート割り当て: moderator 8080 / discovery 8081 / persona-critic 8082 / persona-pragmatist 8083 / Vite 5173。
  - `.env.example`: `DATABASE_URL` を削除し `POSTGRES_BASE_URL` に。`REGISTRY_URL` / `REGISTRY_HEARTBEAT_INTERVAL` / `REGISTRY_TTL` / `PERSONA_SLUG` / `A2A_PUBLIC_URL` / `MEETING_MAX_ROUNDS` を追加。各値がどのバイナリに効くかを既存の書式で注記する。
  - README: アーキテクチャ図を pod 構成に差し替え、DB 分割表、起動手順3通り（compose / ホスト / kind）、新 API 表、A2A のデバッグ手順（`curl` で JSON-RPC を直接叩く例）、「スコープ外」の更新。
- **MIRROR**: `Makefile:5-6` の self-documenting help、`.env.example` の「どのバイナリが読むか」を書く形式、`README.md` の表とコードフェンスの使い方。
- **GOTCHA**: `make dev` でペルソナを discovery より先に起動すると自己登録が最初の1回失敗する。`registry.Client` がリトライするので致命的ではないが、`sleep 2` を入れて初回ログをきれいにする。
- **VALIDATE**: `make help` が新ターゲットを列挙する。`make dev` で5プロセスが起動し、`curl localhost:8080/api/personas` が2体返す。

---

## Testing Strategy

### Unit Tests

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| `TestDatabaseURLFor` | `postgres://u:p@h:5432?sslmode=disable`, `kaigi_meeting` | `postgres://u:p@h:5432/kaigi_meeting?sslmode=disable` | クエリ文字列の保持 |
| `TestDatabaseURLFor_Override` | `MEETING_DATABASE_URL` 設定済み | ベース URL を無視してオーバーライドを使う | 優先順位 |
| `TestDatabaseURLFor_Invalid` | `not a url` | error | ✓ |
| `TestMigrationSetUnknown` | `set = "nope"` | `db: unknown migration set` | ✓ |
| `TestBuildSystemPromptStableAcrossParticipantOrder` | `["A","B"]` と `["B","A"]` | 同一文字列 | キャッシュ前提 |
| `TestBuildSystemPromptSingleParticipant` | 参加者1名 | 会議の文脈節を含まない | ✓ |
| `TestTranscriptRoundTrip` | `TranscriptPayload` | `map[string]any` 経由で復元して等価 | **JSON-RPC の実挙動** |
| `TestTranscriptFromMissingDataPart` | TextPart のみ | 空の payload + error なし（会議の初回として扱う） | ✓ |
| `TestPersonaCardSortsTags` | 関心を逆順で渡す | tags がソート済み | カードのバイト安定性 |
| `TestPersonaCardProtocolVersion` | — | `SupportedInterfaces[0].ProtocolVersion == "1.0"` | コンストラクタ使用の検証 |
| `TestExecutorEventOrder` | fake engine が sources→token×3→done | submitted→working→artifact(data)→artifact(text)×3→completed | イベント順序 |
| `TestExecutorYieldFalseCancels` | 消費側が1イベントで離脱 | engine の ctx がキャンセルされ goroutine が終了 | ✓ リーク |
| `TestExecutorEngineError` | engine が error | `TaskStateFailed` の status イベント | ✓ |
| **`TestModeratorRounds`** | fake dialer / 2ペルソナ / 2ラウンド | 2体目が受け取る transcript に1体目の発言が含まれる。ラウンド2の1体目がラウンド1の全発言を受け取る | **本機能の受け入れ条件** |
| `TestModeratorSequential` | fake dialer が呼び出し順を記録 | `[r1:critic, r1:pragmatist, r2:critic, r2:pragmatist]` | 並列化の回帰防止 |
| `TestModeratorParticipantFailureContinues` | 1体目の Dial が失敗 | `speaker_error` が出て2体目は正常に発言。`Run` は nil を返す | ✓ 部分失敗 |
| `TestModeratorClampsRounds` | `rounds = 99` | `maxRounds` 回で止まる | ✓ |
| `TestModeratorNoParticipants` | 参加者0の会議 | `ErrNoParticipants` | ✓ |
| `TestAgentPresent` | `LastSeenAt = now - ttl` / `- ttl - 1ns` | true / false | ✓ 境界 |
| `TestMeetingSSEFrameOrder` | fake moderator | `speaker_start`→`token`→`speaker_end`→`done` のフレーム | 既存 SSE テストの写し |
| `TestCreateMeetingRejectsAbsentPersona` | `present: false` の slug | 400 | ✓ |
| `TestStatusRecorderSupportsFlush` | — | Flush が成功 | 既存テストを維持 |

### Integration Tests（env gate）

| Test | Gate | 検証内容 |
|---|---|---|
| `TestMigrateAllSets` | `TEST_POSTGRES_BASE_URL` | 4セットが適用され、`chunks.embedding` が `vector(1536)`、HNSW インデックスが存在 |
| `TestGetBySlugLoadsEmbeddings` | `TEST_PERSONA_DATABASE_URL` | `Interests[0].Embedding` が 1536 要素。**affinity 無効化の回帰防止** |
| `TestMeetingStoreSeq` | `TEST_MEETING_DATABASE_URL` | AppendTurn×3 で seq が 0,1,2 |
| `TestRegistryUpsertAndTTL` | `TEST_REGISTRY_DATABASE_URL` | upsert が `last_seen_at` を更新し、List が `present` を正しく返す |
| `TestSearchAgainstPostgres` | `TEST_KNOWLEDGE_DATABASE_URL` | **既存の受け入れ条件を維持**: 批評家と実務家で異なる資料が返る |

### Edge Cases Checklist

- [ ] 参加者0で会議作成 → 400
- [ ] 参加者1で会議 → 旧単一ペルソナ相当の挙動になる
- [ ] 全ペルソナが不在（discovery に登録なし）→ `/api/personas` は空配列、会議作成は 400
- [ ] ペルソナ pod がラウンド途中で死ぬ → `speaker_error`、会議は残りの参加者で続行
- [ ] discovery pod が落ちている → moderator の `/api/health` が degraded、会議作成は 503
- [ ] 検索が0件（懐疑心の閾値で全部落ちた）→ `sources` が空配列で生成は続く（既存挙動の維持）
- [ ] ブラウザが会議途中で切断 → 既に生成された発言は `kaigi_meeting` に残る
- [ ] `rounds` に 0 / 負数 / 巨大値 → 1..maxRounds にクランプ
- [ ] 同一 slug の重複登録 → upsert で1行のまま
- [ ] ペルソナの `A2A_PUBLIC_URL` が解決不能 → 登録は 400（カード解決失敗）
- [ ] Postgres の PVC が古く `kaigi_meeting` が無い → 起動時に明示的なエラー
- [ ] OpenAI のレート制限 → その参加者だけ `speaker_error`
- [ ] 会議録が長大（20ラウンド分）→ 現状は無制限に送る。Risks 参照

---

## Validation Commands

### Static Analysis

```sh
cd backend && go vet ./...
cd frontend && npm run lint
cd frontend && npx tsc -b --noEmit
```
EXPECT: 出力なし、終了コード 0

### Unit Tests

```sh
cd backend && go test ./internal/config/... ./internal/a2aconv/... ./internal/personaexec/... ./internal/meeting/... ./internal/registry/... ./internal/api/... -v
```
EXPECT: すべて PASS。`TestModeratorRounds` と `TestModeratorSequential` が特に PASS していること

### Full Test Suite

```sh
cd backend && go test ./...
cd backend && go test ./... -race
cd frontend && npm run build
```
EXPECT: リグレッションなし。`-race` でデータ競合なし（moderator は goroutine と channel を多用するので `-race` は必須）

### Database Validation

```sh
docker compose up -d postgres
docker compose exec postgres psql -U postgres -lqt | cut -d'|' -f1 | grep kaigi
# → kaigi_meeting / kaigi_registry / kaigi_persona / kaigi_knowledge の4行

export POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable"
export TEST_POSTGRES_BASE_URL="$POSTGRES_BASE_URL"
cd backend && go test ./internal/db/... -v

# 各データベースのテーブルを確認
for db in meeting registry persona knowledge; do
  echo "--- kaigi_$db ---"
  docker compose exec postgres psql -U postgres -d kaigi_$db -c '\dt'
done
```
EXPECT: 4 DB が存在し、それぞれ想定どおりのテーブルを持つ

### A2A Validation（プロトコルの直接確認）

```sh
# 1. エージェントカードが well-known パスで引ける
curl -s localhost:8082/.well-known/agent-card.json | jq '{name, version, protocolVersion: .supportedInterfaces[0].protocolVersion, streaming: .capabilities.streaming}'
# EXPECT: {"name":"批評家","version":"1.0.0","protocolVersion":"1.0","streaming":true}

# 2. JSON-RPC の非ストリーミング呼び出し
curl -s localhost:8082 -X POST -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"SendMessage",
  "params":{"message":{"role":"ROLE_USER","parts":[{"text":"合意形成について"}]}}}' \
  | jq '.result.status.state'
# EXPECT: "TASK_STATE_COMPLETED"

# 3. ストリーミング（SSE が逐次届くこと）
curl -sN localhost:8082 -X POST -H 'Content-Type: application/json' -d '{
  "jsonrpc":"2.0","id":1,"method":"SendStreamingMessage",
  "params":{"message":{"role":"ROLE_USER","parts":[{"text":"合意形成について"}]}}}' | head -20
# EXPECT: data: {...} のフレームが時間差で流れる（一括ではない）

# 4. レジストリにペルソナが登録されている
curl -s localhost:8081/registry/agents | jq '[.[] | {slug, present, name}]'
# EXPECT: critic と pragmatist が present: true
```

### Browser / E2E Validation

```sh
# compose 経路
make docker-up && make docker-seed
open http://localhost:5173

# kind 経路
make kind-up && make kind-load && make k8s-up && make k8s-seed
kubectl -n kaigi get pods
open http://kaigi.localtest.me
```
EXPECT:
- 参加者チェックボックスに2体が `present` で並ぶ
- 2体選択・2ラウンドで開始すると、`critic → pragmatist → critic → pragmatist` の順に発言が流れる
- 2番目以降の発言が**前の発言者に言及している**（これが A2A 会議として機能している証拠）
- 発言ごとに引用が付き、リンクが MinIO の presigned URL で開ける

### Manual Validation

- [ ] `kubectl -n kaigi get pods` が `frontend`(2/2) / `discovery`(1/1) / `persona-critic`(1/1) / `persona-pragmatist`(1/1) / `postgres`(1/1) / `minio`(1/1) と `minio-init`/`seed` の Completed を示す
- [ ] `kubectl -n kaigi logs deploy/persona-critic | grep registered` に自己登録のログがある
- [ ] `kubectl -n kaigi delete pod -l app=persona-pragmatist` → 45秒以内に `/api/personas` で `present: false` になり、UI で「不在」になる
- [ ] Pod 復帰後15秒以内に `present: true` に戻る
- [ ] 会議中に `kubectl -n kaigi delete pod -l app=persona-critic` → その発言だけ `speaker_error` になり、実務家は発言を続ける
- [ ] `kubectl -n kaigi exec -it postgres-0 -- psql -U postgres -d kaigi_meeting -c 'select round, speaker_name, left(content,40) from turns order by seq'` で会議録が順に並ぶ
- [ ] ブラウザの DevTools Network で `/api/meetings/{id}/turns` が `text/event-stream` として**逐次**受信されている（Content-Length が無く、タイムラインが伸びる）
- [ ] `MEETING_MAX_ROUNDS=1` にして再起動 → UI で3ラウンドを選んでも1ラウンドで終わる

---

## Acceptance Criteria

- [ ] 全26タスクが完了している
- [ ] 全 Validation Commands が PASS する
- [ ] `TestModeratorRounds` が PASS する — **2体目のペルソナが1体目の発言を含む会議録を受け取っている**
- [ ] `TestModeratorSequential` が PASS する — 参加者ループが逐次である
- [ ] `TestSearchAgainstPostgres`（既存の受け入れ条件）が引き続き PASS する — 性格による資料の出し分けが壊れていない
- [ ] `TestGetBySlugLoadsEmbeddings` が PASS する — affinity が黙って 0 にならない
- [ ] `go vet ./...` / `npm run lint` / `tsc -b` がクリーン
- [ ] `go test ./... -race` でデータ競合なし
- [ ] kind 上で `kubectl apply -k k8s/overlays/local` 一発で全 Pod が Ready になる
- [ ] ペルソナ pod は `kaigi_meeting` / `kaigi_registry` に接続していない（`grep` で確認できる）
- [ ] moderator は OpenAI を呼んでいない（`grep -rn "openai" backend/cmd/moderator backend/internal/meeting` が空）
- [ ] UI が UX Design の After と一致する
- [ ] 3体目のペルソナが「YAML 1枚 + seed フィクスチャ1件」で追加できる

## Completion Checklist

- [ ] コードが Patterns to Mirror に従っている（エラーラップの前置詞、`slog` のキー、生 SQL、seam を利用側に定義）
- [ ] エラーハンドリングが既存のスタイルと一致している
- [ ] ロギングが既存の規約（snake_case キー、小文字の動詞句）に従っている
- [ ] テストが既存のテストパターン（標準 `testing`、`io.Discard`、`got/want`、env gate）に従っている
- [ ] ハードコードされた値がない（ポート・URL・TTL・ラウンド上限はすべて env）
- [ ] A2A の定数をリテラルで書いていない（`a2asrv.WellKnownAgentCardPath`、`a2a.TaskStateWorking`、`a2a.TransportProtocolJSONRPC`）
- [ ] `a2a.NewAgentInterface` をコンストラクタ経由で使っている
- [ ] `yield` の戻り値を全箇所で確認している
- [ ] DataPart の読み出しが全箇所 `remarshal` を通っている
- [ ] README / `.env.example` / Makefile が更新されている
- [ ] `cmd/server` への参照が1つも残っていない
- [ ] 不要なスコープ追加がない（NOT Building を守っている）
- [ ] 自己完結している — 実装中に追加の質問が必要ない

---

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **会議録が膨らんでトークン代とレイテンシが爆発する** | 高 | 高 | ラウンド数を `MEETING_MAX_ROUNDS`（既定 3）で制限。`chat.Engine` の `historyTurns` 相当の上限を `transcript` に対しても設ける（直近 N 発言のみ送る）。要約・圧縮は NOT Building だが、上限は Task 5 で入れる |
| **`turn_citations` に FK が無いので引用が chunk の実体と乖離する** | 中 | 中 | `title` / `object_key` / スコアを非正規化コピーで持つので、チャンクが消えても引用の表示は壊れない。ただし presigned URL の再生成はできなくなる。マイグレーションのコメントに明記済み |
| **A2A Go SDK は v2 系で API が動く可能性がある** | 中 | 中 | `go.mod` で `v2.5.0` を**厳密にピン**する。SDK に触るのは `a2aconv` / `personaexec` / `meeting/dialer.go` の3箇所だけに閉じ込める。仕様バージョンは `a2a.Version`（"1.0"）で参照し、リテラルを書かない |
| **`iter.Seq2` の `yield` 契約違反で goroutine リーク** | 中 | 高 | `TestExecutorYieldFalseCancels` で回帰を防ぐ。`go test -race` を必須にする。`defer cancel()` を `Execute` の先頭付近に置く |
| **参加者ループを後から誰かが並列化して議論が成立しなくなる** | 中 | 高 | `TestModeratorSequential` が呼び出し順を assert する。`Run` の doc comment に「MUST stay sequential」を書く。この計画の「確定した決定事項」に理由を残す |
| **initdb が既存 volume で走らず4 DB が無い** | 高 | 中 | 起動時のエラーメッセージに「`docker volume rm` / `kubectl delete pvc` が必要」と書く。README の移行手順に明記 |
| **Ingress / Caddy のバッファリングで SSE が一括配信になる** | 中 | 高 | `proxy-buffering: off` と `X-Accel-Buffering: no` を両方入れる。Manual Validation に DevTools での逐次受信確認を入れる |
| **`MINIO_PUBLIC_ENDPOINT` の設定ミスで引用リンクが全部死ぬ** | 中 | 中 | overlay で明示的に上書き。README に「ブラウザから解決できる名前」であることを書く。`.env.example` に注記 |
| **`A2A_PUBLIC_URL` の設定ミスでカード解決だけ失敗する** | 中 | 中 | discovery の登録ハンドラが解決失敗を 400 + 理由付きで返す。ペルソナ pod の `registry.Client` が Warn ログを出す |
| **`GetBySlug` が interest embedding を読まず affinity が黙って 0 になる** | 中 | 高 | `TestGetBySlugLoadsEmbeddings` で `len(Embedding) == 1536` を assert。`PERSONA_INFLUENCE` を上げても結果が変わらなければこれを疑う |
| **discovery が SPOF になる** | 低 | 中 | moderator はカードを取得後キャッシュするので、discovery が落ちても進行中の会議は続く。新規会議の作成だけ失敗する。レプリカ1のままにするのは NOT Building の範囲 |
| **ペルソナ pod ごとに OpenAI を叩くのでレート制限に当たりやすい** | 中 | 中 | 参加者ループが逐次なので同時リクエストは1本。並列化しない理由がここでも効く |

---

## Notes

### なぜ discovery pod を作るのか（Kubernetes の Service DNS があるのに）

k8s の Service DNS は「`persona-critic` という名前がどの IP か」を答える。答えないのは「今どのペルソナが存在し、それぞれどんな関心と skill を持つか」である。A2A はこれをエージェントカードで表現するが、**カードのカタログ（レジストリ）は仕様に含まれていない**。discovery pod はこのギャップを埋める層で、UI の参加者ピッカーがそのまま消費できる形（`present` + `skills.tags`）に整形して返す。

ハートビート TTL 方式を選んだのは、k8s の Endpoints を watch する実装（RBAC + informer）より依存が軽く、compose でもそのまま動くため。

### なぜペルソナ pod を会話ステートレスにするのか

3つ理由がある。

1. **A2A の設計原則**: エージェントは互いの内部状態・メモリ・ツールにアクセスしない（"opaque"）。会議録を司会が持ち、リクエストで渡すのがプロトコルの想定に沿う。
2. **DB 分割と整合する**: ペルソナが会話履歴を持つなら `kaigi_meeting` に書き込む必要があり、「機能ごとに DB を切り替える」が崩れる。
3. **水平スケールできる**: レプリカを増やしても状態の同期問題が無い（今回はレプリカ1固定だが、設計上の余地を残す）。

代償は、会議録がラウンドごとにネットワークを往復すること。ラウンド上限と発言数上限で抑える（Risks の1行目）。

### 「相互にやりとり」のトポロジについて

ユーザーの当初の表現は「それぞれのペルソナがネットワークを介して相互にやりとり」だったが、選択されたのはスター型（司会経由）である。これは要求の後退ではない:

- ペルソナは**ネットワーク越し**に独立したプロセスとして存在し、A2A という標準プロトコルで話す
- ペルソナは**互いの発言に反応する**（会議録の共有による）
- 変わるのは物理トポロジ（メッシュではなくスター）だけで、観測される振る舞いは「議論」である

完全ピアツーピアを後から足す場合、必要なのは (a) ペルソナ pod への discovery クライアント、(b) ホップ数上限、(c) 子タスクのストリームを親ストリームに合流させる機構の3点。(a) は `registry.Client` の逆方向として既に半分ある。

### `cmd/server` を消す判断について

単一プロセス版を残すと、`chat.Engine` のシグネチャ変更（Task 5）に対して2つの呼び出し側を維持し続けることになり、「A2A 経路では動くが単一プロセス経路では壊れている」という状態が生まれやすい。compose を新トポロジにすることで、**開発・compose・k8s の3経路すべてが同じプロセス構成・同じ A2A 経路**を通る。デバッグの一貫性が上がる。

代償は `make dev` が5プロセスになることと、compose の起動が重くなること。ホットリロードの速さは各プロセスが小さいので実用上変わらない。

### マイグレーションの「初版に統合」について

既存環境からの移行パス（`0001` → `0002` で 768→1536 次元に変更）は捨て、新しい4セットは最初から 1536 次元で書く。理由は、**データベース名自体が変わるので既存 DB からの連続的なマイグレーションが存在しない**こと。旧 `kaigi` データベースから移行したい場合は、`make seed` の再実行が唯一の経路になる（README の移行手順に書く）。会話履歴は失われるが、`conversations` → `meetings` はスキーマが変わっているので、いずれにせよ手作業の移行が必要だった。

### A2A のバージョンについて

- **仕様バージョン**: 1.0.0（2026年1月に production-ready 宣言）
- **Go SDK**: `github.com/a2aproject/a2a-go/v2`、`go list -m -versions` で確認した最新は v2.5.0
- SDK のメジャーバージョン（v2）は仕様バージョン（1.0）とは独立している。v2 が「spec 1.0 実装」である

JSON-RPC のメソッド名は SDK が内部で扱うので Go コードには現れないが、`curl` でのデバッグには必要: `SendMessage` / `SendStreamingMessage` / `GetTask` / `ListTasks` / `CancelTask` / `SubscribeToTask` / `GetExtendedAgentCard` ほか push notification 系。v0.x 系の `message/send` 形式とは**異なる**ので、古い記事のコマンドをコピーしないこと。

### 未検証のまま残る点

- `a2asrv.NewJSONRPCHandler` が `SendStreamingMessage` に対して返す SSE のフレーム形式（`event:` 行を出すか `data:` のみか）。司会側は SDK のクライアントを使うので影響しないが、`curl` でのデバッグ手順の期待値は実機で確認してから README に書く。
- `a2aclient` が `SupportedInterfaces` から JSONRPC を選ぶときの挙動（複数候補があるときの優先順位）。今回はカードに JSONRPC 1本しか書かないので問題にならない。
- ingress-nginx の `proxy-read-timeout: 3600` が A2A の SSE に十分か。ラウンド上限3・1発言30秒程度なら余裕があるが、実測で確認する。
