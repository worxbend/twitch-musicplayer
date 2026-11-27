const SCENES = {
  player: { route: '/', text: 'Orbital record, spectral needles and fine rain.', alt: 'Ambient player: an orbital record, fine rain and a floating track deck' },
  spectrum: { route: '/spectrum', text: 'Triggered phosphor traces and a phase portrait.', alt: 'Spectrum scene: phosphor oscilloscope traces' },
  bars: { route: '/bars', text: 'Segmented spectral towers with peak markers.', alt: 'Bars scene: segmented spectral towers and a dim reflection' },
  mirror: { route: '/mirror', text: 'Mirrored filaments woven inside the waveform.', alt: 'Mirror scene: mirrored filaments inside a waveform envelope' },
  dots: { route: '/dots', text: 'A curved constellation matrix lit by frequency.', alt: 'Dots scene: a curved matrix of frequency-lit dots' },
  ribbon: { route: '/ribbon', text: 'Five translucent aurora-silk surfaces.', alt: 'Ribbon scene: translucent audio-shaped ribbons' },
  line: { route: '/line', text: 'A receding topographic landscape of recent audio.', alt: 'Line scene: a topographic landscape of waveform lines' },
  lens: { route: '/lens', text: 'Glass prisms with frequency-driven height.', alt: 'Lens scene: glass prisms with refracted seams' },
};

function tuneTo(name, stations) {
  const scene = SCENES[name];
  if (!scene) return;
  const shot = document.getElementById('tuner-shot');
  shot.src = `shots/${name}-desktop.png`;
  // The <source> wins over img.src in WebP-capable browsers, so swap it too.
  const webp = document.getElementById('tuner-shot-webp');
  if (webp) webp.srcset = `shots/${name}-desktop.webp`;
  shot.alt = scene.alt;
  document.getElementById('tuner-route').textContent = scene.route;
  document.getElementById('tuner-text').textContent = scene.text;
  const index = Object.keys(SCENES).indexOf(name);
  const position = (index + 0.5) / stations.length;
  document.getElementById('needle').style.setProperty('--pos', String(position));
}

function setUpTuner() {
  const stations = document.querySelectorAll('#stations input');
  stations.forEach((input) => input.addEventListener('change', () => tuneTo(input.value, stations)));
  tuneTo('player', stations);
}

async function copyBlock(button) {
  const text = button.parentElement.querySelector('code').textContent;
  const status = document.getElementById('copy-status');
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = 'Copied';
    status.textContent = 'Command copied to clipboard';
  } catch (error) {
    button.textContent = 'Select and copy';
    status.textContent = `Copy failed: ${error.message}`;
  }
  setTimeout(() => { button.textContent = 'Copy'; }, 1800);
}

function setUpCopyButtons() {
  document.querySelectorAll('.copy').forEach((button) => {
    button.addEventListener('click', () => copyBlock(button));
  });
}

setUpTuner();
setUpCopyButtons();
