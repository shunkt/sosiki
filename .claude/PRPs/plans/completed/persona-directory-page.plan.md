# Plan: Persona Directory Page (登録ペルソナ一覧画面)

## Summary
登録済みペルソナ(在席/不在・スタンス・関心トピックと重み・懐疑度・冗長度)を一覧で確認できる `/personas` 画面を追加する。現状のフロントは会議の参加者選択チップ(名前+在席のみ)しか無く、人格詳細は API に出ていない。persona pod が自分の A2A AgentCard に「人格プロファイル」を A2A extension として載せ、discovery → moderator の既存 `GET /api/personas` 経路でそのまま運び、フロントは `react-router` で `/`(会議)と `/personas`(一覧)に分ける。

## User Story
As a 会議の主催者(人間), I want 登録されているペルソナの人格と在席状況を一覧で見たい, so that 誰を会議に呼ぶか判断できる。

## Problem → Solution
`GET /api/personas` は `{slug,name,personaId,present,skills}` だけを返し、UI はチップ表示のみ (`frontend/src/App.tsx:226-239`) → 人格プロファイルを API に追加し、専用ページ `/personas` でカード一覧表示する。

## Metadata
- **Complexity**: Large(バックエンド3層 + フロント新規ルーティング)
- **Source PRD**: N/A
- **PRD Phase**: N/A
- **Estimated Files**: 17(CREATE 6 / UPDATE 11)

## 確定済みの要件(ユーザー回答)
- 表示内容: 既存 API のみでなく **人格詳細(スタンス・関心と重み・懐疑度・冗長度)も表示**
- 画面の置き場所: **react-router で別 URL(`/personas`)**

---

## UX Design

### Before
```
┌ kaigi 会議        backend: ok   ● 2/2 在席 ┐
│ (◻批評家) (◻実務家)   ラウンド:[2]          │  ← 名前と在席のみ。人格は見えない
│ ...会議ログ...                              │
└─────────────────────────────────────────────┘
```

### After
```
/personas
┌ kaigi  [会議][ペルソナ一覧]   backend: ok   ● 2/2 在席 ┐
│ ┌ 批評家 (critic)                       ● 在席 ┐        │
│ │ 根拠のない主張には懐疑的                     │        │
│ │ 懐疑度 ▓▓▓▓▓▓▓▓░░ 0.85   発言量: 簡潔       │        │
│ │ 関心: Raft +0.60  形式的検証 +0.80           │        │
│ │       マーケティング −0.50                   │        │
│ └──────────────────────────────────────────────┘        │
│ ┌ 実務家 (pragmatist)                   ○ 不在 ┐        │
│ └──────────────────────────────────────────────┘        │
└─────────────────────────────────────────────────────────┘
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| ヘッダー | 会議のみ | 「会議 / ペルソナ一覧」の `NavLink` | 両ページ共通 `AppHeader` |
| `/personas` | 未定義(SPA は `/` を返す) | 一覧ページ | Caddy は `try_files` で既に対応済み |
| プロファイル未取得の pod | — | 「詳細未登録」表示 | 旧 pod は次の heartbeat で更新される |
| 会議中にページ遷移 | — | 会議状態は破棄、ストリーム中断 | 既知の制約(NOT Building 参照) |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `backend/internal/a2aconv/card.go` | 16-56 | カード生成。ここに extension を足す |
| P0 | `backend/internal/registry/client.go` | 93-115 | `Skill` / `AgentDTO`。`Profile` を足す |
| P0 | `backend/internal/agentapi/dto.go` | 10-26 | `toAgentDTO`。Profile を抽出して詰める |
| P0 | `backend/internal/persona/persona.go` | 7-44 | `Interest`(Weight -1..1)/`Personality`(Skepticism 0..1, Verbosity) |
| P0 | `frontend/src/App.tsx` | 25-54, 212-251 | 既存画面。`MeetingPage` として移設 |
| P0 | `frontend/src/api/client.ts` | 1-10, 50-56, 74 | `Agent` 型・`getJSON`・`listAgents` |
| P1 | `backend/internal/api/personas.go` | 13-21 | パススルー。**変更不要**(DTO をそのまま返す) |
| P1 | `backend/internal/api/api_test.go` | 31-38, 108-128 | `fakeAgentLister` / `TestListPersonas` |
| P1 | `backend/internal/a2aconv/card_test.go` | 9-36 | `testCardPersona()`(懐疑度 0.85・負の関心あり)を再利用 |
| P1 | `backend/internal/registry/store.go` | 24-41, 68-92 | card は JSONB に丸ごと保存 → extension は往復する |
| P1 | `frontend/src/Markdown.test.tsx` | 1-10 | フロントのテスト様式(`renderToStaticMarkup` + vitest) |
| P2 | `frontend/src/App.css` | 1-90 | CSS 命名(kebab-case + `--` 修飾)、`.participant-chip` |
| P2 | `frontend/Caddyfile` | 18-30 | SPA フォールバック済み。**変更不要** |
| P2 | `frontend/vite.config.ts` | all | dev サーバは history fallback が既定。変更不要 |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| A2A AgentCard extensions | A2A 仕様 `capabilities.extensions[]` (`uri`, `description`, `required`, `params`) | `params` は任意 JSON。人格プロファイルの搬送に使う |
| a2a-go v2.5.0 `AgentCard` | `github.com/a2aproject/a2a-go/v2/a2a` | **未検証**。`Capabilities.Extensions []AgentExtension`(`Params map[string]any`)が存在するか Task 1 で確認 |
| react-router v7 | npm `react-router` | v7 は `react-router` 単一パッケージから `BrowserRouter/Routes/Route/NavLink` を import。`react-router-dom` は不要 |

計画作成時、a2a-go はローカルのモジュールキャッシュ(`C:\Users\shun9\go\pkg\mod`)に無く、構造体を直接確認できていない。**Task 1 が最初のゲート**。

---

## Patterns to Mirror

### NAMING_CONVENTION
```go
// SOURCE: backend/internal/registry/client.go:93-99
type Skill struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}
```
JSON は camelCase、Go は PascalCase。フロントの型はこの JSON 形に 1:1 で合わせる (`frontend/src/api/client.ts:3-10`)。CSS は `persona-card`, `persona-card--absent` のように kebab-case + `--`。

### DTO_REUSE
```go
// SOURCE: backend/internal/agentapi/dto.go:10-26
func toAgentDTO(a registry.Agent, present bool) registry.AgentDTO {
	dto := registry.AgentDTO{ Slug: a.Slug, ... Card: a.Card }
	if a.Card != nil {
		dto.Name = a.Card.Name
		dto.Skills = make([]registry.Skill, len(a.Card.Skills))
		...
```
DTO は `registry.AgentDTO` を唯一の定義とする(ファイル内コメントにある過去のドリフト事故を繰り返さない)。`agentapi` に第二の DTO を作らない。

### ERROR_HANDLING
```go
// SOURCE: backend/internal/api/personas.go:14-19
if err != nil {
	h.deps.Log.Error("list personas (via discovery)", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list personas"})
	return
}
```
```ts
// SOURCE: frontend/src/App.tsx:48-52
listAgents(controller.signal)
  .then((list) => setAgents(list))
  .catch(() => { if (!controller.signal.aborted) setError('ペルソナの取得に失敗しました') })
```
中断由来のエラーは表示しない。エラーメッセージは日本語。

### LOGGING_PATTERN
```go
// SOURCE: backend/internal/agentapi/agentapi.go:122
h.deps.Log.Warn("resolve agent card failed", "slug", req.Slug, "base_url", req.BaseURL, "error", err)
```
`slog` の key-value。プロファイル解釈失敗は表示用の任意情報なので、ログを出さず nil(=詳細未登録)にする。

### FRONTEND_FETCH_EFFECT
```ts
// SOURCE: frontend/src/App.tsx:41-54
useEffect(() => {
  const controller = new AbortController()
  ...
  return () => controller.abort()
}, [])
```
`AbortController` + cleanup。StrictMode の二重実行に耐える形。

### TEST_STRUCTURE
```go
// SOURCE: backend/internal/api/api_test.go:31-38
type fakeAgentLister struct { agents []registry.AgentDTO; err error }
func (f fakeAgentLister) ListAgents(context.Context) ([]registry.AgentDTO, error) { return f.agents, f.err }
```
```tsx
// SOURCE: frontend/src/Markdown.test.tsx:5-10
const render = (md: string, streaming = false) =>
  renderToStaticMarkup(<Markdown streaming={streaming}>{md}</Markdown>)
describe('Markdown', () => { it('renders bold', () => { expect(render('**結論**')).toMatch(...) }) })
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `backend/internal/registry/profile.go` | CREATE | `Profile`/`ProfileInterest` 型、拡張 URI 定数、`ProfileFromCard` |
| `backend/internal/registry/profile_test.go` | CREATE | 往復・欠落・不正 params のテスト |
| `backend/internal/registry/client.go` | UPDATE | `AgentDTO` に `Profile *Profile` を追加 |
| `backend/internal/a2aconv/card.go` | UPDATE | カードに人格プロファイル extension を追加 |
| `backend/internal/a2aconv/card_test.go` | UPDATE | extension のテスト追加 |
| `backend/internal/agentapi/dto.go` | UPDATE | `toAgentDTO` で `Profile` を詰める |
| `backend/internal/agentapi/agentapi_test.go` | UPDATE | list 応答に profile が含まれるテスト |
| `backend/internal/api/api_test.go` | UPDATE | `/api/personas` が profile を素通しするテスト |
| `frontend/package.json` (+lock) | UPDATE | `react-router` 追加 |
| `frontend/src/main.tsx` | UPDATE | `BrowserRouter` で包む |
| `frontend/src/App.tsx` | UPDATE | ルーティングシェルに縮小(`Routes`) |
| `frontend/src/MeetingPage.tsx` | CREATE | 既存 `App` 本体の移設(挙動変更なし) |
| `frontend/src/AppHeader.tsx` | CREATE | 共通ヘッダー(NavLink・status・在席数) |
| `frontend/src/PersonasPage.tsx` | CREATE | 一覧ページ(取得・状態表示) |
| `frontend/src/PersonaCard.tsx` (+`.test.tsx`) | CREATE | 純粋な表示コンポーネントとテスト |
| `frontend/src/api/client.ts` | UPDATE | `Profile` 型、`Agent.profile` |
| `frontend/src/App.css` | UPDATE | ナビ・カード・メーターのスタイル |
| `README.md` | UPDATE | 画面と API フィールドの追記 |

## NOT Building
- ペルソナの作成・編集・削除 UI(seed ジョブ経由のまま)
- `/personas/:slug` 個別詳細ルート(一覧カード内で全項目を表示するため不要)
- 会議状態の遷移越え保持(状態のリフト / Context 化)
- `GET /api/personas` から `baseUrl` / `card` を除去する整理(既存挙動。別課題)
- discovery が `kaigi_persona` DB を直接読む方式(DB-per-function 分離を崩すため不採用)
- 知識ベース(文書・チャンク)の一覧
- 検索・並べ替え・フィルタ

## Alternatives Considered
| 案 | 不採用理由 |
|---|---|
| discovery が persona DB を読む | discovery は `kaigi_registry` のみ開く設計 (`cmd/discovery/main.go:44`)。境界違反 |
| moderator が各 persona pod に別 API で問い合わせ | fan-out・タイムアウト・不在 pod の扱いが増える。カードは既に登録経路で運ばれている |
| スタンスを `card.description`、関心を `skills.tags` から復元 | 負の関心・重み・懐疑度・冗長度が落ちる |

---

## Step-by-Step Tasks

### Task 1: a2a-go の Extension API を確認(ゲート)
- **ACTION**: `cd backend && go mod download && go doc github.com/a2aproject/a2a-go/v2/a2a AgentCapabilities && go doc github.com/a2aproject/a2a-go/v2/a2a AgentExtension`
- **IMPLEMENT**: `Capabilities.Extensions` と `Params`(任意 JSON)の有無・型を確認し、以降のコードをその型に合わせる
- **MIRROR**: `card.go:40-44` の `a2a.AgentCapabilities{...}` リテラル
- **IMPORTS**: なし
- **GOTCHA**: `Params` が `map[string]any` の場合、JSONB 往復後の数値は `float64`、配列は `[]any` になる。`ProfileFromCard` は `json.Marshal(params)` → `json.Unmarshal(&Profile{})` の二段変換で型差を吸収する。**Extension が無い/使えない場合は実装を止め、ユーザーに代替案(persona pod に `GET /profile` を追加し discovery 登録時に取得)を確認する**
- **VALIDATE**: `go doc` が該当フィールドを表示する

### Task 2: `registry.Profile` と `ProfileFromCard`
- **ACTION**: `backend/internal/registry/profile.go` を作成
- **IMPLEMENT**:
  ```go
  const ProfileExtensionURI = "https://kaigi.local/ext/persona-profile/v1"
  type ProfileInterest struct { Topic string `json:"topic"`; Weight float32 `json:"weight"` }
  type Profile struct {
      Stance     string            `json:"stance"`
      Skepticism float32           `json:"skepticism"`
      Verbosity  string            `json:"verbosity"`
      Interests  []ProfileInterest `json:"interests"`
  }
  // ProfileFromCard returns nil when the extension is absent or malformed.
  func ProfileFromCard(card *a2a.AgentCard) *Profile
  ```
  interests は Weight 降順(同値は Topic 昇順)で返す
- **MIRROR**: NAMING_CONVENTION、`registry.go` のパッケージコメント様式(「なぜ」を英語で書く)
- **IMPORTS**: `encoding/json`, `sort`, `github.com/a2aproject/a2a-go/v2/a2a`
- **GOTCHA**: `registry` は `persona` を import しない(循環回避)。`Verbosity` は `persona.Verbosity` ではなく `string`
- **VALIDATE**: Task 3 のテストが通る

### Task 3: `profile_test.go`
- **ACTION**: テストを作成
- **IMPLEMENT**: (a) extension なし → nil (b) 正常 params → 全項目一致 (c) params が文字列など不正型 → nil でパニックしない (d) `nil` card → nil (e) `json.Marshal(card)` → `Unmarshal` → `ProfileFromCard` の往復で値が保たれる
- **MIRROR**: TEST_STRUCTURE(標準 `testing`、`t.Fatalf("… = %v, want …")` 形式)
- **VALIDATE**: `go test ./internal/registry/...`

### Task 4: カード生成に extension を追加
- **ACTION**: `a2aconv.PersonaCard` を更新
- **IMPLEMENT**: 全 interests(**負の重み含む**)を Weight 降順→Topic 昇順に `sort.Slice` し、`Capabilities.Extensions` に `{URI: registry.ProfileExtensionURI, Description: "persona personality profile", Params: {...}}` を追加。既存の `Description: Stance` と `Skills[0].Tags`(正の関心のみ)は**変更しない**
- **MIRROR**: `card.go:21-29` の「sort で byte 安定」コメントと手順
- **IMPORTS**: `github.com/shun/kaigi/backend/internal/registry`
- **GOTCHA**: `Embedding` はプロファイルに含めない(巨大・非表示)。`Params` へは構造体を `Marshal→Unmarshal` で map 化して渡し、型を JSON 準拠に揃える
- **VALIDATE**: `go build ./...`

### Task 5: `card_test.go` 追加
- **ACTION**: 既存 `testCardPersona()` を使うテストを追加
- **IMPLEMENT**: `ProfileFromCard(PersonaCard(...))` で Stance / Skepticism(0.85)/ interests 3 件(負の「マーケティング −0.5」を含む)/ 並び順を検証。既存 `TestPersonaCardSortsTags` `…ExcludesNegativeWeightInterests` が無変更で通ることも確認
- **VALIDATE**: `go test ./internal/a2aconv/...`

### Task 6: `AgentDTO.Profile` と `toAgentDTO`
- **ACTION**: `registry/client.go` の `AgentDTO` に `Profile *Profile \`json:"profile"\`` を追加、`agentapi/dto.go` で `dto.Profile = registry.ProfileFromCard(a.Card)`
- **MIRROR**: DTO_REUSE
- **GOTCHA**: `nil` は JSON で `null`。フロントは `profile: Profile | null`。フィールド追加のみなので既存利用側(`internal/meeting/dialer.go`)は壊れないが `go build ./...` で確認
- **VALIDATE**: `go build ./... && go vet ./...`

### Task 7: バックエンドのテスト(agentapi / api)
- **ACTION**: `agentapi_test.go` に「extension 付きカードの agent は `GET /registry/agents` が `profile` を返す/無い agent は `null`」、`api_test.go` に「`fakeAgentLister` が返した `Profile` が `/api/personas` の JSON に `profile` として出る」を追加
- **MIRROR**: `api_test.go:108-128` `TestListPersonas`
- **VALIDATE**: `go test ./...`(backend 全体)

### Task 8: フロント依存追加
- **ACTION**: `cd frontend && npm i react-router`
- **GOTCHA**: `npm view react-router version peerDependencies` で React 19 対応を先に確認(未検証)。`react-router-dom` は入れない
- **VALIDATE**: `npm ls react-router`

### Task 9: API クライアント型
- **ACTION**: `frontend/src/api/client.ts` に追加
- **IMPLEMENT**:
  ```ts
  export type ProfileInterest = { topic: string; weight: number }
  export type Profile = {
    stance: string
    skepticism: number
    verbosity: 'concise' | 'balanced' | 'detailed'
    interests: ProfileInterest[]
  }
  ```
  `Agent` に `profile: Profile | null` を追加。`listAgents` は変更しない
- **MIRROR**: `client.ts:3-10` の型定義スタイル(`type` 別名、セミコロン無し)
- **VALIDATE**: `npx tsc -b`

### Task 10: 会議画面を `MeetingPage` へ移設、ルーティング導入
- **ACTION**: `App.tsx` の本体を `MeetingPage.tsx`(`export default function MeetingPage`)へ移し、`App.tsx` を `<Routes>` シェルにする。ヘッダー(`h1`・status・在席数)を `AppHeader` に切り出し、`NavLink to="/" end` と `NavLink to="/personas"` を置く
- **IMPLEMENT**: `main.tsx` は `<StrictMode><BrowserRouter><App/></BrowserRouter></StrictMode>`。`App`: `<Routes><Route path="/" element={<MeetingPage/>}/><Route path="/personas" element={<PersonasPage/>}/><Route path="*" element={<Navigate to="/" replace/>}/></Routes>`
- **MIRROR**: 移設は**ロジック無変更**(`App.tsx:25-211` のコメント含めそのまま)。`import './App.css'` は維持
- **GOTCHA**: ページ遷移でアンマウントされると進行中の SSE が中断される(既知制約)。`useEffect` の依存・StrictMode 対策は変えない
- **VALIDATE**: `npm run build`、`/` の挙動が従来と同一(手動)

### Task 11: `PersonaCard`(純粋コンポーネント)
- **ACTION**: `frontend/src/PersonaCard.tsx` を作成。props は `{ agent: Agent }` のみ(fetch しない)
- **IMPLEMENT**: 名前・slug・在席バッジ(●在席/○不在)・`profile.stance`・懐疑度メーター(0..1。`aria-label` 付き)・冗長度ラベル(`concise→簡潔` `balanced→標準` `detailed→詳細`、未知値はそのまま表示)・関心リスト(正は `+0.60`、負は `−0.50` を符号付きで)・スキル名。`profile === null` は「詳細未登録」。不在なら `persona-card--absent`
- **MIRROR**: `App.tsx:282-283` の `toFixed(2)` + 符号表示 `c.affinity >= 0 ? '+' : ''`、`participant-chip--absent` の流儀
- **GOTCHA**: 重みは色だけに頼らず数値も併記(アクセシビリティ)。`index.css` のグローバル `h2` は大きいので、カード内見出しは `.persona-card__name` でサイズ明示(markdown プランで指摘された `index.css` 汚染と同種)
- **VALIDATE**: Task 13 のテスト

### Task 12: `PersonasPage`
- **ACTION**: `frontend/src/PersonasPage.tsx`。`listAgents` を FRONTEND_FETCH_EFFECT で取得。状態: 読み込み中 / エラー('ペルソナの取得に失敗しました' + 再読込ボタン)/ 空('登録されているペルソナはありません')/ 一覧(`<PersonaCard key={a.slug}>`)
- **IMPLEMENT**: 在席は heartbeat TTL で変わるため 30s 間隔で再取得(`setInterval`)。ロード表示は初回のみ(ちらつき防止)。在席数を `AppHeader` に渡す
- **MIRROR**: ERROR_HANDLING(中断時はエラー非表示)
- **GOTCHA**: StrictMode の二重 effect で interval が二重にならないよう cleanup で `clearInterval` + `abort`
- **VALIDATE**: 手動(下記)

### Task 13: フロントのテスト
- **ACTION**: `PersonaCard.test.tsx`
- **IMPLEMENT**(`renderToStaticMarkup`): (a) 名前・slug・スタンスが出る (b) 在席/不在でバッジと `--absent` クラスが切り替わる (c) 負の重みが `−0.50` 形式 (d) `profile: null` で「詳細未登録」 (e) 未知の `verbosity` でも落ちない (f) `interests: []` で落ちない (g) スタンスに `<script>` を含めてもエスケープされる
- **MIRROR**: TEST_STRUCTURE(`Markdown.test.tsx`)
- **VALIDATE**: `npm test`

### Task 14: スタイル
- **ACTION**: `App.css` に `.app-nav`, `.app-nav a[aria-current="page"]`, `.persona-list`, `.persona-card`, `.persona-card--absent`, `.persona-card__name`, `.meter`, `.interest-list` を追加
- **MIRROR**: `App.css:63-82`(`participant-chip`/`--absent`/`absent-badge`)、余白 `0.5rem/1rem`、枠 `1px solid rgba(128,128,128,0.3)`、在席色 `#2e9e5b`
- **GOTCHA**: `.app` は `height: calc(100vh - 4rem)` + flex column(会議用)。一覧は長くなるので `.persona-list { overflow-y: auto; flex: 1 }` とする
- **VALIDATE**: 手動でスクロールを確認

### Task 15: README
- **ACTION**: 画面(`/`, `/personas`)と `GET /api/personas` の `profile` フィールドを追記
- **VALIDATE**: 目視

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected | Edge? |
|---|---|---|---|
| ProfileFromCard 正常 | extension 付きカード | 全項目一致 | |
| ProfileFromCard 欠落 | extension なし(旧 pod) | nil | ✔ |
| ProfileFromCard 不正 | params が文字列 | nil・パニックなし | ✔ |
| ProfileFromCard 往復 | Marshal→Unmarshal 後 | 数値・順序が保持 | ✔ |
| PersonaCard 負の重み | weight -0.5 | `−0.50` 表示 | ✔ |
| PersonaCard 未登録 | profile null | 「詳細未登録」 | ✔ |
| PersonaCard XSS | stance に `<script>` | エスケープ | ✔ |
| /api/personas | Profile 付き DTO | JSON に `profile` | |

### Edge Cases Checklist
- [ ] ペルソナ 0 件(空表示)
- [ ] 関心トピックが非常に多い/長い(折り返し)
- [ ] `profile` が `null`(旧 pod)
- [ ] 不在ペルソナ(グレーアウト)
- [ ] discovery 停止・moderator 500(エラー表示 + 再読込)
- [ ] `/personas` 直アクセス・リロード(dev: Vite / 本番: Caddy `try_files`)
- [ ] StrictMode 二重 effect でポーリングが二重にならない
- [ ] 未知パス → `/` へリダイレクト

---

## Validation Commands

### Static Analysis
```bash
cd backend && go build ./... && go vet ./...
cd frontend && npx tsc -b && npm run lint
```
EXPECT: エラー 0

### Unit Tests
```bash
cd backend && go test ./internal/registry/... ./internal/a2aconv/... ./internal/agentapi/... ./internal/api/...
cd frontend && npm test
```
EXPECT: 全通過

### Full Test Suite
```bash
cd backend && go test ./...
cd frontend && npm test && npm run build
```
EXPECT: 回帰なし(DB 必須テストの実行方法は `Makefile` を参照)

### Manual Validation
- [ ] スタックを起動(`docker-compose.yml` または kind。手順は `README.md` / `Makefile`)し、seed 済みの critic / pragmatist が並ぶ
- [ ] critic の懐疑度 0.85、負の関心「マーケティング −0.5」が見える
- [ ] persona pod を 1 つ停止 → TTL 経過後 30s 以内に「不在」表示へ変わる
- [ ] `/personas` を直接開いてリロードしても表示される
- [ ] ヘッダーのリンクで `/` と往復でき、会議画面は従来どおり開始できる
- [ ] 旧イメージの pod(profile なし)で「詳細未登録」表示になる

---

## Acceptance Criteria
- [ ] 全タスク完了、検証コマンド全通過
- [ ] `/personas` で在席状況・スタンス・関心(重み付き)・懐疑度・冗長度が見える
- [ ] `GET /api/personas` の既存フィールドは不変(`profile` の追加のみ)
- [ ] 既存の会議画面の挙動が変わらない
- [ ] 型エラー・lint エラーなし

## Completion Checklist
- [ ] 既存パターン(DTO 一本化、日本語エラー、AbortController)に従っている
- [ ] 数値の意味(重み -1..1、懐疑度 0..1)が UI で伝わる
- [ ] `Embedding` などの内部データを API に出していない
- [ ] スコープ外(NOT Building)に手を出していない

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| a2a-go v2.5.0 に Extension/Params が無い | 中 | 高 | Task 1 をゲートにし、無ければ停止してユーザーに代替案を確認 |
| `Params` の数値型が JSONB 往復で変わる | 中 | 中 | 二段変換 + 往復テスト |
| 会議中に遷移すると会議が消える | 中 | 中 | 既知制約として明記。必要なら別計画で状態をリフト |
| 旧 pod は再登録まで profile が空 | 高 | 低 | `null` を許容し「詳細未登録」表示 |
| `/api/personas` が `baseUrl`/`card` をブラウザへ露出(既存) | 既存 | 低 | 本計画では不変。別課題として記録 |
| グローバル CSS(`h2` 等)との衝突 | 中 | 低 | カード専用クラスで明示指定 |

## Notes
- Caddy(`Caddyfile:25-30`)と Vite dev は SPA フォールバック済みのため、サーバ側のルーティング変更は不要。
- a2a-go の実コードは計画作成時に未確認(モジュールキャッシュに無い)。これが Task 1 をゲートにしている理由。
