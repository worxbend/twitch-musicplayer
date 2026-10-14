interface Props { active: 'player' | 'scenes' | 'console'; }

export function TerminalHeader(props: Props) {
  return (
    <>
      <a class="skip-link" href="#main">Skip to content</a>
      <header class="terminal-header">
        <a class="brand" href="/" aria-label="Lofi Radio home">
          <span class="brand-mark" aria-hidden="true">▂▅█▅▂</span>
          <span><span class="brand-name">lofi<span>.radio</span></span><span class="brand-sub">a self-hosted frequency</span></span>
        </a>
        <nav class="terminal-nav" aria-label="Main navigation">
          <a href="/" aria-current={props.active === 'player' ? 'page' : undefined}><span class="nav-symbol" aria-hidden="true">&gt;_</span>Player</a>
          <a href="/scenes" aria-current={props.active === 'scenes' ? 'page' : undefined}><span class="nav-symbol" aria-hidden="true">~/</span>Scenes</a>
          <a href="/admin" aria-current={props.active === 'console' ? 'page' : undefined}><span class="nav-symbol" aria-hidden="true">#</span>Console</a>
        </nav>
        <span class="header-tag">local-first<br />always yours.</span>
      </header>
    </>
  );
}
