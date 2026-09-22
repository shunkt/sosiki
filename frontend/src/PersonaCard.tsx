import type { Agent, Profile } from './api/client'
import { formatWeight } from './format'

// Japanese labels for Profile['verbosity']. Unknown values (a newer backend
// than this build) fall through to the raw string rather than being hidden.
const VERBOSITY_LABEL: Record<string, string> = {
  concise: '簡潔',
  balanced: '標準',
  detailed: '詳細',
}

function clamp01(n: number): number {
  return Math.min(1, Math.max(0, n))
}

function ProfileSection({ profile }: { profile: Profile }) {
  const skepticism = clamp01(profile.skepticism)
  return (
    <>
      {profile.stance && <p className="persona-card__stance">{profile.stance}</p>}
      <dl className="persona-card__facts">
        <dt>懐疑度</dt>
        <dd>
          <meter
            className="meter"
            min={0}
            max={1}
            value={skepticism}
            aria-label={`懐疑度 ${skepticism.toFixed(2)}`}
          />{' '}
          {skepticism.toFixed(2)}
        </dd>
        <dt>発言量</dt>
        <dd>{VERBOSITY_LABEL[profile.verbosity] ?? profile.verbosity}</dd>
      </dl>
      {profile.interests.length > 0 && (
        <ul className="interest-list" aria-label="関心トピック">
          {profile.interests.map((i) => (
            <li
              key={i.topic}
              className={`interest ${i.weight < 0 ? 'interest--negative' : 'interest--positive'}`}
            >
              {i.topic} <span className="interest-weight">{formatWeight(i.weight)}</span>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

// PersonaCard is purely presentational: it never fetches, so PersonasPage owns
// loading/error state and this stays trivially testable with renderToStaticMarkup.
export function PersonaCard({ agent }: { agent: Agent }) {
  return (
    <article className={`persona-card ${agent.present ? '' : 'persona-card--absent'}`}>
      <header className="persona-card__header">
        <h2 className="persona-card__name">{agent.name}</h2>
        <span className="persona-card__slug">{agent.slug}</span>
        <span className={`persona-card__presence ${agent.present ? 'persona-card__presence--on' : ''}`}>
          {agent.present ? '● 在席' : '○ 不在'}
        </span>
      </header>
      {agent.profile ? (
        <ProfileSection profile={agent.profile} />
      ) : (
        <p className="persona-card__missing">詳細未登録</p>
      )}
    </article>
  )
}
