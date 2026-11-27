/* The client router. nginx serves index.html for every path, so this alone
 * decides which page a URL gets. Kept free of Pixi, solid and the DOM so
 * node --test can load it.
 */

export type Route =
  | { kind: 'overlay' }
  | { kind: 'logo-overlay' }
  | { kind: 'scenes' }
  | { kind: 'now-playing' }
  | { kind: 'scene'; slug: string; transparent: boolean }
  | { kind: 'player' };

const FIXED: Readonly<Record<string, Route>> = {
  '': { kind: 'player' },
  overlay: { kind: 'overlay' },
  logo: { kind: 'logo-overlay' },
  'logo-overlay': { kind: 'logo-overlay' },
  scenes: { kind: 'scenes' },
  'now-playing': { kind: 'now-playing' },
};

/**
 * Leading and trailing slashes are ignored, so `/overlay/` is `/overlay`.
 * A scene is opaque at `/<slug>` and transparent at `/<slug>-overlay`;
 * `?transparent=1` forces transparency on either. Anything else is the player.
 */
export function resolveRoute(pathname: string, search: string, sceneSlugs: readonly string[]): Route {
  const path = pathname.replace(/^\/+|\/+$/g, '');
  if (Object.hasOwn(FIXED, path)) return FIXED[path];

  const forced = new URLSearchParams(search).get('transparent') === '1';
  for (const slug of sceneSlugs) {
    if (path === slug) return { kind: 'scene', slug, transparent: forced };
    if (path === `${slug}-overlay`) return { kind: 'scene', slug, transparent: true };
  }
  return { kind: 'player' };
}

/** The URL params a player page reads, in the order links write them. */
export const PLAYER_LINK_PARAMS = ['ramp', 'autoplay'] as const;

/** Keeps only the non-empty player params of `search`; '' when none remain. */
export function playerLinkSearch(search: string): string {
  const from = new URLSearchParams(search);
  const kept = new URLSearchParams();
  for (const name of PLAYER_LINK_PARAMS) {
    const value = from.get(name);
    if (value) kept.set(name, value);
  }
  const query = kept.toString();
  return query ? `?${query}` : '';
}

/**
 * The link to a scene that keeps the player params. Slug '' is the ambient
 * player at `/`; `overlay` picks the transparent page.
 */
export function sceneHref(slug: string, search: string, overlay = false): string {
  const path = slug ? `/${slug}${overlay ? '-overlay' : ''}` : overlay ? '/overlay' : '/';
  return path + playerLinkSearch(search);
}
