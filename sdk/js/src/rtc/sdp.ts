/**
 * Returns the `a=msid:<stream> <track>` ids of the media section with `a=mid:<mid>`.
 *
 * Remote `MediaStreamTrack.id` is not the msid track id in every browser (Firefox generates
 * its own ids), so the server's track SID must be read from the remote description.
 */
export function msidForMid(sdp: string | undefined, mid: string | null): { streamId: string; trackId: string } | undefined {
  if (!sdp || mid === null) return undefined;
  for (const section of sdp.split(/\r?\nm=/).slice(1)) {
    const lines = section.split(/\r?\n/);
    if (!lines.some((l) => l === `a=mid:${mid}`)) continue;
    for (const l of lines) {
      if (!l.startsWith("a=msid:")) continue;
      const [streamId, trackId] = l.slice("a=msid:".length).trim().split(/\s+/);
      if (streamId && trackId) return { streamId, trackId };
    }
    return undefined;
  }
  return undefined;
}
