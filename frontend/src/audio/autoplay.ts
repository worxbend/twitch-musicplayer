type ObsWindow = Window & {
  obsstudio?: unknown;
};

export function shouldAutoStartAudio(
  defaultValue = false,
  buildValue = import.meta.env?.VITE_AUTO_START_AUDIO as string | undefined,
) {
  const queryValue = parseBoolean(new URLSearchParams(window.location.search).get('autoplay'));
  if (queryValue !== null) return queryValue;

  // OBS browser sources never get a user gesture, so a build-time "false"
  // must not silence them; only ?autoplay= can opt an OBS source out.
  if ((window as ObsWindow).obsstudio !== undefined) return true;

  const envValue = parseBoolean(buildValue);
  if (envValue !== null) return envValue;

  return defaultValue;
}

function parseBoolean(value: string | null | undefined) {
  if (value === null || value === undefined) return null;
  switch (value.trim().toLowerCase()) {
    case '1':
    case 'true':
    case 'yes':
    case 'on':
      return true;
    case '0':
    case 'false':
    case 'no':
    case 'off':
      return false;
    default:
      return null;
  }
}
