import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Markdown } from './Markdown'

const render = (md: string, streaming = false) =>
  renderToStaticMarkup(<Markdown streaming={streaming}>{md}</Markdown>)

describe('Markdown', () => {
  it('renders bold', () => {
    expect(render('**結論**')).toMatch(/data-streamdown="strong"[^>]*>結論</)
  })

  it('renders lists', () => {
    const html = render('- a\n- b')
    expect(html).toContain('<ul')
    expect(html.match(/<li/g)).toHaveLength(2)
  })

  it('renders fenced code', () => {
    expect(render('```go\nx := 1\n```')).toContain('x := 1')
  })

  it('renders GFM tables', () => {
    expect(render('|a|b|\n|-|-|\n|1|2|')).toContain('<table')
  })

  it('keeps single newlines as line breaks', () => {
    expect(render('a\nb')).toContain('<br')
  })

  it('does not emit raw script tags', () => {
    expect(render('<script>alert(1)</script>')).not.toContain('<script')
  })

  it('does not emit javascript: links', () => {
    expect(render('[x](javascript:alert(1))')).not.toContain('href="javascript:')
  })

  it('keeps citation markers as text', () => {
    expect(render('根拠 [1]')).toContain('[1]')
  })

  it('tolerates unterminated markdown while streaming', () => {
    expect(() => render('**途中', true)).not.toThrow()
    expect(() => render('```go\nx', true)).not.toThrow()
  })

  it('tolerates empty input', () => {
    expect(() => render('')).not.toThrow()
  })
})
