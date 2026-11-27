import { render } from 'solid-js/web';
import { App } from './App';
import { NowPlayingOverlay } from './NowPlayingOverlay';
import { parseNowPlayingOptions } from './nowPlaying';
import { OverlayApp } from './OverlayApp';
import { resolveRoute } from './routes';
import { SceneApp } from './SceneApp';
import { ScenesIndex } from './ScenesIndex';
import { SCENES } from './viz/scenes';
import './styles/global.css';

const { pathname, search } = window.location;

// Any visualizer in the catalogue, opaque at /<slug> and transparent at
// /<slug>-overlay. Adding a scene to viz/scenes.ts routes it automatically.
const route = resolveRoute(pathname, search, SCENES.map(s => s.slug));
const ramp = new URLSearchParams(search).get('ramp') ?? undefined;

render(
  () => {
    switch (route.kind) {
      case 'logo-overlay':
        return <OverlayApp transparent showBackground={false} />;
      case 'overlay':
        return <OverlayApp />;
      case 'scenes':
        return <ScenesIndex />;
      case 'now-playing':
        return <NowPlayingOverlay {...parseNowPlayingOptions(search)} />;
      case 'scene':
        return (
          <SceneApp
            scene={SCENES.find(s => s.slug === route.slug)!}
            transparent={route.transparent}
            ramp={ramp}
          />
        );
      case 'player':
        return <App />;
    }
  },
  document.getElementById('root')!,
);
