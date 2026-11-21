/* Latest-request gate: a fetch may apply its response only while no newer
 * fetch of the same kind has begun, so out-of-order replies cannot overwrite
 * newer state.
 */
export interface LatestGate {
  begin(): number;
  isLatest(id: number): boolean;
}

export function createLatest(): LatestGate {
  let counter = 0;
  return {
    begin: () => ++counter,
    isLatest: id => id === counter,
  };
}
