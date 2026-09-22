import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { Agent } from './api/client'
import { formatWeight } from './format'
import { PersonaCard } from './PersonaCard'

const agent = (over: Partial<Agent> = {}): Agent => ({
  slug: 'critic',
  name: '批評家',
  personaId: '00000000-0000-0000-0000-000000000001',
  present: true,
  skills: [],
  profile: {
    stance: '根拠のない主張には懐疑的',
    skepticism: 0.85,
    verbosity: 'concise',
    interests: [
      { topic: '形式的検証', weight: 0.8 },
      { topic: 'マーケティング', weight: -0.5 },
    ],
  },
  ...over,
})

const render = (a: Agent) => renderToStaticMarkup(<PersonaCard agent={a} />)

describe('PersonaCard', () => {
  it('shows name, slug and stance', () => {
    const html = render(agent())
    expect(html).toContain('批評家')
    expect(html).toContain('critic')
    expect(html).toContain('根拠のない主張には懐疑的')
  })

  it('switches presence badge and absent class', () => {
    const present = render(agent())
    expect(present).toContain('● 在席')
    expect(present).not.toContain('persona-card--absent')

    const absent = render(agent({ present: false }))
    expect(absent).toContain('○ 不在')
    expect(absent).toContain('persona-card--absent')
  })

  it('formats negative weights with a minus sign', () => {
    expect(render(agent())).toContain('−0.50')
    expect(formatWeight(0.6)).toBe('+0.60')
    expect(formatWeight(-0.5)).toBe('−0.50')
  })

  it('shows skepticism and verbosity label', () => {
    const html = render(agent())
    expect(html).toContain('0.85')
    expect(html).toContain('簡潔')
  })

  it('shows a placeholder when profile is null', () => {
    expect(render(agent({ profile: null }))).toContain('詳細未登録')
  })

  it('tolerates an unknown verbosity', () => {
    const base = agent().profile!
    const html = render(agent({ profile: { ...base, verbosity: 'verbose' as never } }))
    expect(html).toContain('verbose')
  })

  it('tolerates empty interests', () => {
    const base = agent().profile!
    expect(() => render(agent({ profile: { ...base, interests: [] } }))).not.toThrow()
  })

  it('escapes markup in the stance', () => {
    const base = agent().profile!
    const html = render(agent({ profile: { ...base, stance: '<script>alert(1)</script>' } }))
    expect(html).not.toContain('<script>')
  })
})
