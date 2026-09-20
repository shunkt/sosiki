import { cjk } from '@streamdown/cjk'
import remarkBreaks from 'remark-breaks'
import { defaultRemarkPlugins, Streamdown } from 'streamdown'
import 'streamdown/styles.css'

const plugins = { cjk }
// Passing remarkPlugins replaces Streamdown's defaults (remark-gfm among them),
// so the defaults are spread back in ahead of remark-breaks.
const remarkPlugins = [...Object.values(defaultRemarkPlugins), remarkBreaks]

// Streamdown's built-in chrome (code/table toolbars, the external-link
// confirmation modal) is styled with Tailwind utility classes, which this
// app does not ship — so it is switched off here and the plain elements are
// styled by the `.markdown` rules in App.css instead. Raw HTML stays
// sanitized (Streamdown's default) because content is LLM output derived
// from crawled documents.
export function Markdown({ children, streaming = false }: { children: string; streaming?: boolean }) {
  return (
    <div className="markdown">
      <Streamdown
        mode={streaming ? 'streaming' : 'static'}
        isAnimating={streaming}
        plugins={plugins}
        remarkPlugins={remarkPlugins}
        controls={false}
        linkSafety={{ enabled: false }}
      >
        {children}
      </Streamdown>
    </div>
  )
}
