# Implementation Report: Kubernetes 上の A2A ペルソナ会議

## Summary

単一プロセスの kaigi バックエンドを、A2A（Agent2Agent）プロトコルで会話する4種類の独立サービスに解体した。ペルソナは1体1プロセスの A2A サーバーになり、moderator が会議録を共有しながら参加者を逐次呼び出すことで、ペルソナが互いの発言に反応する会議を実現する。

**ステータス: 全26タスク完了。**

段階的実行についてユーザーの承認を得ながら3フェーズ（Go バックエンドのコア / Docker Compose 化 / k8s+フロントエンド）に分けて進行し、各フェーズの完了ごとにコミットした。フェーズ3では実際に kind クラスタを構築し、Ingress 経由で実 OpenAI 生成を含む完全な会議ターンが動作することまで確認済み。

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | XL | XL（想定通り） |
| Confidence | 7/10 | 実装完了部分は高信頼（実機E2E検証済み）。未着手部分（k8s/フロントエンド）は未検証 |
| Files Changed | 48（新規37 / 更新10 / 削除1） | 64（Task 1-21 分。新規39 / 更新23 / 削除2 — フロントエンド・k8s・README等の残タスク分を除く） |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | config に DSN 組み立てを追加 | ✅ Complete | |
| 2 | db パッケージのマイグレーションセット対応 | ✅ Complete | `go:embed migrations`（サブディレクトリ含む）に変更 |
| 3 | 4セットのマイグレーションを書く | ✅ Complete | |
| 4 | Postgres の4データベース初期化 | ✅ Complete | Docker Compose 分は完了。k8s 側（ConfigMap）は未着手 |
| 5 | chat.Engine を会話DB非依存に | ✅ Complete | `chat.Store` は完全削除。`Reply` シグネチャを刷新 |
| 6 | BuildSystemPrompt に参加者を渡す | ✅ Complete | 参加者名はソートしてキャッシュ安定性を維持 |
| 7 | internal/a2aconv | ✅ Complete | JSON round-trip の実機検証込み |
| 8 | a2aconv.PersonaCard | ✅ Complete | |
| 9 | internal/personaexec | ✅ Complete | 実装中に見つけた artifact-ID バグを含め修正済み |
| 10 | cmd/persona | ✅ Complete | |
| 11 | persona.Store に GetBySlug | ✅ Complete | embedding 読み込み漏れの回帰テスト付き |
| 12 | internal/registry | ✅ Complete | |
| 13 | internal/agentapi + cmd/discovery | ✅ Complete | |
| 14 | internal/meeting ドメイン・永続化 | ✅ Complete | |
| 15 | internal/meeting/moderator.go | ✅ Complete | **本機能の受け入れ条件** `TestModeratorRounds`/`TestModeratorSequential` が PASS |
| 16 | internal/meeting の A2A ダイアラ | ✅ Complete | |
| 17 | internal/api を moderator 用に書き換え | ✅ Complete | `/api/documents/{id}` は削除（計画から逸脱、理由は後述） |
| 18 | cmd/moderator + cmd/server 削除 | ✅ Complete | |
| 19 | cmd/seed を分割DBに対応 | ✅ Complete | |
| 20 | Dockerfile を3バイナリに | ✅ Complete（実際は4バイナリ） | moderator/discovery/persona/seed |
| 21 | docker-compose.yml を新トポロジに | ✅ Complete | `docker compose up` で全8サービス起動を実機確認 |
| 22 | k8s マニフェスト（base） | ✅ Complete | 実際の kind クラスタで apply・全Pod Running を確認 |
| 23 | k8s overlay と kind クラスタ | ✅ Complete | `kind` を公式バイナリで直接インストール（Homebrew は Xcode ライセンス要求で断念）。`configMapGenerator`/`secretGenerator` の namespace 解決バグを踏み、直接パッチ方式に変更 |
| 24 | フロントエンドの API クライアント | ✅ Complete | |
| 25 | フロントエンドの会議 UI | ✅ Complete | 実機（kind + Ingress）で実 OpenAI 生成による完全な会議ターンを確認 |
| 26 | Makefile / .env.example / README | ✅ Complete | |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis (`go vet`) | ✅ Pass | |
| Unit Tests | ✅ Pass | 全13パッケージ・87件超のテスト、`-race` 付き |
| Build | ✅ Pass | `go build ./...` クリーン |
| Integration (実 Postgres) | ✅ Pass | 使い捨て Postgres コンテナで4DB全マイグレーション検証 |
| **実機 E2E（実 OpenAI + Postgres + MinIO + A2A）** | ✅ Pass | 批評家ペルソナが raft-paper.md / paxos-made-simple.md を引用した約2000字の実質的な日本語回答を生成・保存・SSE配信 |
| Docker Compose | ✅ Pass | `docker compose up` で全8サービスが Up、`/api/health`・`/api/personas`・フロントエンド経由のプロキシまで確認 |
| Edge Cases | 部分的 | ユニットテストでカバー済みの分のみ（並行失敗、ラウンド上限クランプ等）。手動チェックリストの大半（k8s関連）は未実施 |

## Files Changed（Task 1-21 分、64ファイル）

主要なもののみ抜粋（全量は `git show --stat` 参照）:

| File | Action |
|---|---|
| `backend/cmd/moderator/main.go` | CREATED |
| `backend/cmd/discovery/main.go` | CREATED |
| `backend/cmd/persona/main.go`, `health.go` | CREATED |
| `backend/cmd/server/main.go` | DELETED |
| `backend/internal/a2aconv/*.go` | CREATED（4ファイル） |
| `backend/internal/personaexec/*.go` | CREATED |
| `backend/internal/registry/*.go` | CREATED（5ファイル） |
| `backend/internal/agentapi/*.go` | CREATED（4ファイル） |
| `backend/internal/meeting/*.go` | CREATED（8ファイル） |
| `backend/internal/chat/store.go` | DELETED |
| `backend/internal/chat/types.go` | CREATED |
| `backend/internal/chat/engine.go`, `prompt.go` | UPDATED |
| `backend/internal/api/*.go` | 3削除・3新規・2更新 |
| `backend/internal/config/dsn.go` | CREATED |
| `backend/internal/db/migrations/{meeting,registry,persona,knowledge}/0001_init.sql` | CREATED |
| `backend/internal/db/migrations/000{1,2}_*.sql` | DELETED（4セットに再編） |
| `backend/Dockerfile`, `docker-compose.yml` | UPDATED |
| `postgres-init/001-create-databases.sql` | CREATED |
| `frontend/Caddyfile` | UPDATED |

## Deviations from Plan

1. **`chat.Store`（`DocumentObjectKey` 含む）を全削除**。プランの Files to Change 表では「`DocumentObjectKey` は残す」としていたが、Task 17 の詳細検討で `/api/documents/{id}` ルート自体を削除する決定と矛盾していたため、より具体的な決定（Task 17）を優先し全削除した。moderator は `kaigi_knowledge` に接続しないため、そもそも当該メソッドは機能しない。

2. **Dockerfile は計画の「3バイナリ」ではなく4バイナリ**。プランのタイトルは「3バイナリに」だが、本文中の IMPLEMENT は最初から moderator/discovery/persona/seed の4つを明記しており、タイトルの誤記を実装で修正した。

3. **`turn_citations` の `URL` は永続化しない**。presigned URL は15分で失効するため、DB に保存しても再読み込み時には無効。この挙動は旧実装（`message_citations` にも URL 列がなかった）と同じで、退行ではない。

## Issues Encountered（実機テストで発見した3つのバグ）

いずれもユニットテスト（フェイクベース）では検出できず、実際のインフラに対する実行でのみ顕在化した。

1. **`encoding/gob` 型未登録**: a2a-go SDK の既定タスクストアが `Task` を gob エンコードでディープコピーする際、`DataPart` の payload（`any` 型フィールド越しに格納）の具体型が未登録だとエンコードに失敗し、全リクエストが `gob: type not registered for interface: a2aconv.CitationsPayload` で失敗していた。`a2aconv` パッケージの `init()` で `gob.Register` を追加して解決。

2. **アーティファクト未作成でのUPDATE**: `personaexec.Executor` がトークンを常に `NewArtifactUpdateEvent`（追記）で送っており、対応する `NewArtifactEvent`（作成）を一度も呼んでいなかった。SDK 側は `"no artifact found for update"` で失敗し、全ての返信が空文字列になっていた（症状は見えにくく、`speaker_end` イベント自体は正常に届くため一見成功しているように見えた）。最初のトークンで artifact を作成し、以降は同じIDに追記する実装に修正し、両者が同一 artifact ID を持つことを検証する回帰テストを追加した。

3. **マイグレーションの並行実行レース**: `docker compose up` で複数の persona pod が同時起動すると、共有DBである `kaigi_persona`/`kaigi_knowledge` への `CREATE EXTENSION IF NOT EXISTS` が競合し `duplicate key value violates unique constraint "pg_extension_name_index"` で失敗することを実機で確認。`db.Migrate` に `pg_advisory_lock` を追加し、実際に5並行での `db.New` 呼び出しを再現する統合テストも追加した。

4. **SSEイベントのJSONキー不整合**: `meeting.Turn`/`Citation`/`Participant` に `json` タグが無く、REST API（DTO層経由で camelCase）と SSE（`meeting.Event` を直接 marshal するため Go の既定 PascalCase）でフィールド名が食い違っていた。実際にキャプチャした SSE ストリームを見て発覚。フロントエンドの型と一致するよう `json` タグを追加し、JSON キーの形式を直接検証する回帰テストを追加した。

5.（k8sデプロイ特有）**`minio/minio:latest`/`minio/mc:latest` が匿名 pull 不可**: Docker Hub 側の変更で `pull access denied` になった。`quay.io/minio/minio`・`quay.io/minio/mc`（同一イメージのミラー）に切り替えて解決。

6.（k8sデプロイ特有）**`kind load docker-image`/`image-archive` が multi-platform マニフェストで失敗**: kind v0.30.0 で `ctr images import --all-platforms` が欠けているプラットフォームのコンテンツダイジェストを要求し失敗する既知の問題に遭遇（`docker save` 経由でも同様）。quay.io への切り替えで kind ノードが直接 pull するようになり、この問題を回避。

## Tests Written

| Test File | Tests | Coverage |
|---|---|---|
| `internal/config/dsn_test.go` | 5 | DSN組み立て・オーバーライド優先順位 |
| `internal/db/db_test.go`, `concurrent_migrate_test.go` | 5 | マイグレーション冪等性・未知セット・**並行実行レース** |
| `internal/chat/engine_test.go`, `prompt_test.go` | 15 | イベント順序・話者ラップ・システムプロンプトのキャッシュ安定性 |
| `internal/a2aconv/a2aconv_test.go`, `card_test.go` | 11 | **JSON round-trip（gob問題を再現する条件）**、カードの安定性 |
| `internal/personaexec/executor_test.go` | 6 | イベント順序・**artifact ID 一貫性**・goroutine リーク防止 |
| `internal/persona/store_test.go` | 3 | **GetBySlug の embedding 読み込み**（affinity 無効化の回帰防止） |
| `internal/registry/registry_test.go`, `store_test.go` | 9 | TTL境界・upsert冪等性 |
| `internal/agentapi/agentapi_test.go` | 5 | カード解決失敗時の拒否 |
| `internal/meeting/store_test.go`, `moderator_test.go`, `dialer_test.go` | 16 | **`TestModeratorRounds`/`TestModeratorSequential`（受け入れ条件）**、部分失敗時の継続、ラウンド上限クランプ |
| `internal/api/api_test.go` | 14 | SSEフレーム分離・改行エンコード・エラーイベント |

合計 約90テスト、全て `-race` 付きで PASS。

## Validation Results（Task 22-26 追加分）

| Level | Status | Notes |
|---|---|---|
| `kubectl kustomize` (base / overlay) | ✅ Pass | |
| 実際の kind クラスタへの apply | ✅ Pass | 全7 Workload Pod（discovery/persona×2/frontend/postgres/minio + seed Job）が Running/Completed |
| k8s Ingress 経由の疎通 | ✅ Pass | `/api/health`・`/api/personas`・SPA配信 |
| **k8s 経由の実機E2E（実 OpenAI 生成）** | ✅ Pass | Ingress 経由で会議ターン送信 → `speaker_start`→`sources`→236個の`token`→`speaker_end`→`round_end`→`done`、camelCase JSON キー確認済み |
| frontend `npm run build` / `npm run lint` | ✅ Pass | |

## Next Steps

- [ ] `/code-review` でこのコミットのレビュー
- [ ] PR 作成（`/prp-pr` または `gh pr create`）
- [ ] （任意）3体目以降のペルソナ追加、ペルソナ間メッシュ型通信への拡張検討
