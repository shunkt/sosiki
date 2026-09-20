# Plan: Markdown Message Rendering

## Summary
ペルソナ(LLM)の発言は Markdown で返ってくるが、フロントエンドは `<p className="message-content">` に生テキストを `white-space: pre-wrap` で流し込んでいるだけで、`**太字**` や箇条書き、コードブロックが記号のまま表示される。`react-markdown` + `remark-gfm` で発言本文を描画し、既存のグローバルCSS(巨大な h1/h2、`code{display:inline-flex}`、`#root{text-align:center}`)と衝突しないよう `.markdown` スコープのスタイルを追加する。

## User Story
As a 会議の参加者(人間), I want ペルソナの発言が Markdown として整形表示されること, so that 箇条書き・強調・コードが読みやすくなる。

## Problem → Solution
Markdown記法が生テキストで見える (`frontend/src/App.tsx:268-270`) → 安全に Markdown を HTML 描画し、ストリーミング中も崩れず、引用 `[1]` や既存の失敗表示も維持する。

## Metadata
- **Complexity**: Small
- **Source PRD**: N/A
- **PRD Phase**: N/A
- **Estimated Files**: 6 (CREATE 2 / UPDATE 4: package.json, package-lock.json, App.tsx, App.css)

---

## UX Design

### Before
```
┌ 批評家 ────────────────────────────┐
│ **結論**: 次の3点が問題です。\n     │
│ - コスト [1]\n- `foo()` の設計      │   ← 記号がそのまま見える
└────────────────────────────────────┘
```

### After
```
┌ 批評家 ────────────────────────────┐
│ 結論: 次の3点が問題です。           │   ← 太字
│  • コスト [1]                       │
│  • foo() の設計                     │   ← インラインコード
└────────────────────────────────────┘
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| ペルソナ発言 | 生テキスト | Markdown描画 | ストリーミング中も逐次描画 |
| ユーザー発言 | 生テキスト | 生テキストのまま | 人間の入力は整形しない |
| 失敗(`failed`)表示 | 赤イタリック | 同じ(生テキスト) | エラー文字列を Markdown 解釈しない |
| リンク | なし | 新規タブ `rel="noreferrer"` | 生HTMLは無効のまま |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `frontend/src/App.tsx` | 261-287 | 置換対象の描画箇所 |
| P0 | `frontend/src/App.css` | 125-155 | `.message-content` (`white-space: pre-wrap`) |
| P0 | `frontend/src/index.css` | 53-111 | グローバル汚染: `#root{text-align:center}`, `h1/h2` の 56px/24px, `p{margin:0}`, `code{display:inline-flex;padding}` |
| P1 | `frontend/package.json` | all | 依存追加先。`build` は `tsc -b && vite build`、lint は `oxlint` |
| P2 | `backend/internal/chat/prompt.go` | 22-75 | システムプロンプトにMarkdown指定なし。**バックエンド変更は不要**(LLMが自発的にMarkdownを出力) |

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| react-markdown | npm `react-markdown` | 既定で生HTMLを描画しない(XSS安全)。`rehype-raw` は入れない。`components` で `a` を差し替え可能。v9+ は `className` prop を廃止 → ラッパー要素にクラスを付ける |
| remark-gfm | npm `remark-gfm` | 表・取り消し線・タスクリスト |
| remark-breaks | npm `remark-breaks` | 単一改行を `<br>` にする(従来の `pre-wrap` 挙動の維持) |

実装時に `npm view react-markdown version peerDependencies` で React 19 対応版を確認すること(未検証)。

---

## Patterns to Mirror

### NAMING_CONVENTION
```tsx
// SOURCE: frontend/src/App.tsx:14-22 / App.css
type DisplayTurn = Turn & { pending?: boolean; failed?: boolean }
className="message-content"   // kebab-case, 修飾は `message--failed`
```
コンポーネントは PascalCase、CSSは kebab-case + `--` 修飾。

### COMMENT_STYLE
```tsx
// SOURCE: frontend/src/App.tsx:14-17
// DisplayTurn extends the persisted Turn shape with in-flight streaming
// state (...).  — 「なぜ」を英語で書く長めのコメント
```

### STREAM_PATTERN
```tsx
// SOURCE: frontend/src/App.tsx:146-158
case 'token': ... content: item.turn.content + event.text
```
本文は文字列を逐次連結。Markdown コンポーネントは `content` 文字列を受けるだけにし、状態管理には触れない。

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `frontend/package.json` (+ `package-lock.json`) | UPDATE | `streamdown`, `@streamdown/cjk`, `remark-breaks`, dev: `vitest`(react-markdown から変更) |
| `frontend/src/Markdown.tsx` | CREATE | Markdown描画コンポーネント(リンク処理・プラグイン集約) |
| `frontend/src/Markdown.test.tsx` | CREATE | `renderToStaticMarkup` による描画テスト |
| `frontend/src/App.tsx` | UPDATE | ペルソナ発言で `<Markdown>` を使用 |
| `frontend/src/App.css` | UPDATE | `.markdown` スコープCSS追加 |

## NOT Building
- バックエンドのプロンプト変更(Markdown出力の強制/禁止)
- シンタックスハイライト(`rehype-highlight` 等)
- 生HTML描画(`rehype-raw`)、数式、Mermaid
- ユーザー発言・エラー文の Markdown 化
- 過去会議の再読込UI(`getMeeting` は未使用)

---

## Step-by-Step Tasks

### Task 1: 依存追加
- **ACTION**: `cd frontend && npm ci && npm i react-markdown remark-gfm remark-breaks && npm i -D vitest`
- **IMPLEMENT**: `package.json` に `"test": "vitest run"` を追加
- **GOTCHA**: node_modules は現状未インストールの可能性 → 先に `npm ci`。vitest が vite 8 対応か `npm view vitest peerDependencies` で確認。非対応なら vitest を諦め、Task 5 を手動検証に置換
- **VALIDATE**: `npm ls react-markdown remark-gfm remark-breaks` がエラーなし

### Task 2: `Markdown.tsx` 作成
- **ACTION**: 共通コンポーネントを作る
- **IMPLEMENT**:
```tsx
import ReactMarkdown from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'

const remarkPlugins = [remarkGfm, remarkBreaks]

// Raw HTML stays disabled (react-markdown's default; rehype-raw is deliberately
// not added) because content is LLM output derived from crawled documents.
export function Markdown({ children }: { children: string }) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        components={{
          a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noreferrer" />,
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
```
- **MIRROR**: COMMENT_STYLE / NAMING_CONVENTION
- **GOTCHA**: plugins 配列はモジュールスコープに置く(ストリーミングで高頻度再描画されるため)。`urlTransform` は既定のまま(`javascript:` を除去)。oxlint が未使用変数 `_node` を警告する場合は書き方を調整
- **VALIDATE**: `npx tsc -b` 通過

### Task 3: `App.tsx` 差し替え
- **ACTION**: `App.tsx:268-270` を置換
- **IMPLEMENT**:
```tsx
{item.turn.role === 'persona' && !item.turn.failed && item.turn.content ? (
  <Markdown>{item.turn.content}</Markdown>
) : (
  <div className="message-content">{item.turn.content || (item.turn.pending ? '…' : '')}</div>
)}
```
`import { Markdown } from './Markdown'` を追加
- **GOTCHA**: 元は `<p>` だが Markdown は `<p>`/`<ul>` を含むため、生テキスト側も `<div>` に変更。`.message--user .message-content` / `.message--failed .message-content` の既存セレクタはそのまま効く
- **VALIDATE**: `npm run build`

### Task 4: CSS (`App.css` に追記)
- **IMPLEMENT**:
  - `.markdown { text-align: left; line-height: 1.5; overflow-wrap: anywhere }`
  - `.markdown p { margin: 0 0 0.6em }`、`.markdown > :last-child { margin-bottom: 0 }`
  - `.markdown h1..h6 { font-size: 1.1em; margin: 0.8em 0 0.4em }` (グローバル `h1{56px}`/`h2{24px}` と `index.css:76-93` の media query を上書き。`.markdown h1` の詳細度で勝てる)
  - `.markdown ul, .markdown ol { margin: 0 0 0.6em; padding-left: 1.4em }`
  - `.markdown pre { overflow-x: auto; padding: 0.75rem; background: var(--code-bg); border-radius: 6px }`
  - `.markdown code { display: inline; padding: 0.1em 0.35em; font-size: 0.9em }` — **`index.css:98-111` の `display:inline-flex` と `padding:4px 8px` を必ず打ち消す**
  - `.markdown pre code { display: block; padding: 0; background: none }`
  - `.markdown table { border-collapse: collapse; display: block; overflow-x: auto }`、`th, td { border: 1px solid var(--border); padding: 0.25rem 0.5rem }`、`th { text-align: left }`
  - `.markdown blockquote { margin: 0 0 0.6em; padding-left: 0.75rem; border-left: 3px solid var(--border) }`
  - `.markdown a { color: var(--accent) }`
- **GOTCHA**: `#root{text-align:center}` を継承するので `th` 等が中央寄せになる点に注意。ダークモードは既存CSS変数(`--code-bg`, `--border`, `--accent`)を使えば追従する。`index.css` 自体は変更しない
- **VALIDATE**: ブラウザで light/dark 両方を目視

### Task 5: テスト
- **ACTION**: `Markdown.test.tsx` 作成(`renderToStaticMarkup` を使うため jsdom 不要)
- **IMPLEMENT**: 下記テスト表のケースを `describe/it/expect` で実装
- **GOTCHA**: vitest 既定の `node` 環境で動くこと
- **VALIDATE**: `npm test`

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 太字 | `**結論**` | `<strong>結論</strong>` を含む | |
| 箇条書き | `- a\n- b` | `<ul>` と 2 つの `<li>` | |
| コードブロック | ` ```go\nx\n``` ` | `<pre><code` を含む | |
| 単一改行 | `a\nb` | `<br/>` を含む | ✔ 従来挙動維持 |
| 生HTML無効 | `<script>alert(1)</script>` | `<script>` タグが出力に無い | ✔ セキュリティ |
| javascript: リンク | `[x](javascript:alert(1))` | `href="javascript:` が出力に無い | ✔ |
| 外部リンク | `[x](https://e.com)` | `target="_blank"` と `rel="noreferrer"` | |
| 引用マーカー | `根拠 [1]` | テキスト `[1]` がそのまま残る | ✔ |
| 未完了Markdown(ストリーミング) | `**途中` / ` ```go\nx` | 例外なく描画 | ✔ |
| 空文字 | `` | 例外なし | ✔ |

### Edge Cases Checklist
- [ ] 空入力
- [ ] 巨大な本文(コードブロック/表が横スクロールし、ページ幅を壊さない)
- [ ] 日本語の長文(`overflow-wrap`)
- [ ] ストリーミング中の閉じていない記法
- [ ] ダークモード

---

## Validation Commands

### Static Analysis
```bash
cd frontend && npx tsc -b && npm run lint
```
EXPECT: エラーなし

### Unit Tests
```bash
cd frontend && npm test
```
EXPECT: 全テスト通過

### Build
```bash
cd frontend && npm run build
```
EXPECT: 成功

### Browser Validation
```bash
cd frontend && npm run dev   # backend も起動しておく (docker compose up)
```
EXPECT: ペルソナ発言の太字・箇条書き・コード・表が整形される

### Manual Validation
- [ ] 議題に「Markdownの見出し、箇条書き、コードブロック、表を使って説明して」と入力して全ペルソナの描画を確認
- [ ] ストリーミング中にレイアウトが大きく跳ねない
- [ ] ダーク/ライト両方で読める
- [ ] 出典リスト(`.sources`)が従来どおり表示
- [ ] 発言失敗時は赤イタリックの生テキスト

---

## Acceptance Criteria
- [ ] ペルソナ発言が Markdown として描画される
- [ ] 生HTML/`javascript:` が無効
- [ ] tsc / lint / test / build がすべて通る
- [ ] グローバルCSSの副作用(巨大見出し、inline-flex の code)が出ない

## Completion Checklist
- [ ] 既存の命名・コメントスタイルに合致
- [ ] `index.css` は変更しない(スコープはすべて `.markdown`)
- [ ] バックエンド無変更
- [ ] スコープ外を追加していない

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `index.css` のグローバル `code`/`h1`/`h2` が描画を崩す | 高 | 中 | `.markdown` 配下で明示的に打ち消し(Task 4) |
| vitest が vite 8 / TS 6 と非互換 | 中 | 低 | 非互換なら手動検証 + build のみに縮退 |
| 高頻度トークンで Markdown 再パースが重い | 低 | 低 | 発言は数百〜数千字。問題が出たら確定済み発言のみ Markdown 化 / `React.memo` |
| LLM出力由来のリンクが悪用される | 低 | 中 | 生HTML無効・`urlTransform` 既定・`rel="noreferrer"` |

## Notes
- 根本原因: `frontend` に Markdown パーサが存在しない(依存は react/react-dom のみ)。バックエンドは素のテキストをSSEで流しており変更不要。
- `remark-breaks` は「単一改行=改行」を維持する互換措置。不要と判断すれば外してよい。
