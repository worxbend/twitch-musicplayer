interface Props { active: 'player' | 'scenes' | 'console'; }

export function TerminalHeader(props: Props) {
  return (
    <>
      <a class="skip-link" href="#main">Skip to content</a>
      <header class="terminal-header">
        <a class="brand" href="/" aria-label="Lofi Radio home">
          <svg class="brand-mark" viewBox="0 0 40 40" fill="none" aria-hidden="true"><circle cx="20" cy="20" r="17" stroke="currentColor" stroke-width="1.2" /><ellipse cx="20" cy="20" rx="8" ry="17" transform="rotate(38 20 20)" stroke="currentColor" stroke-width="1.2" /><circle cx="20" cy="20" r="4" fill="currentColor" /><circle cx="32" cy="8" r="3" fill="var(--coral)" /></svg>
          <span><span class="brand-name">lofi<span>.radio</span></span><span class="brand-sub">Signal observatory</span></span>
        </a>
        <nav class="terminal-nav" aria-label="Main navigation">
          <a href="/" aria-current={props.active === 'player' ? 'page' : undefined}>Player</a>
          <a href="/scenes" aria-current={props.active === 'scenes' ? 'page' : undefined}>Scenes</a>
          <a href="/admin" aria-current={props.active === 'console' ? 'page' : undefined}>Console</a>
        </nav>
        <span class="header-tag">Independent frequency</span>
      </header>
    </>
  );
}
