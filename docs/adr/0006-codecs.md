# ADR-0006: Codec set: Opus, VP8 and H.264 simulcast

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

The browsers we support (Chrome, Edge, Firefox, Safari; latest two versions) all implement
Opus, VP8 and H.264 (Constrained Baseline, `packetization-mode=1`) for WebRTC, and all can
send simulcast for VP8 and H.264 using RIDs (Safari supports H.264 and VP8 simulcast since
Safari 14.1/15; Firefox supports RID-based simulcast). Recording wants H.264 because it can
be remuxed into fMP4 without transcoding (ADR-0007).

## Decision

1. **Audio: Opus** (`48000/2`), with:
   - `useinbandfec=1` (in-band FEC) always offered;
   - `usedtx=1` (DTX) on by default, disabled per track with `AddTrackRequest.disable_dtx`;
   - `stereo=1` when the publisher asks for stereo;
   - **RED** (`audio/red`, RFC 2198, `111/111` redundancy) optional: negotiated only when the
     publisher requests it (`AddTrackRequest.red`) and the subscriber supports it. The SFU
     forwards RED as is to RED-capable subscribers and **strips RED to primary Opus** for
     others (pure header/payload slicing, no decode).
   - Header extensions: `urn:ietf:params:rtp-hdrext:ssrc-audio-level` (RFC 6464, required for
     speaker detection), `abs-send-time` not used, TWCC used.
2. **Video: VP8 and H.264**, both with **simulcast** of up to three spatial layers
   (`q`, `h`, `f` RIDs = low/medium/high, default 1/4, 1/2 and full resolution) plus temporal
   layers for VP8 (L1T3 in each simulcast layer when the browser supports it).
   - H.264 profile: `42e01f` (Constrained Baseline 3.1) and `42001f`; `packetization-mode=1`;
     `level-asymmetry-allowed=1`. High profile may be added later; not required for remuxing.
   - Header extensions: `sdes:mid`, `sdes:rtp-stream-id`, `sdes:repaired-rtp-stream-id`,
     transport-wide-cc, `abs-capture-time` not used initially, video orientation passed through.
   - RTX (`video/rtx`) is negotiated with publishers so their retransmissions arrive on a
     separate SSRC; towards subscribers the SFU retransmits on the **primary SSRC** by
     default (simpler, saves an SSRC per downtrack); RTX towards subscribers is an optional
     later optimisation.
   - VP9/AV1 (SVC) are **out of scope for v1**; the `simulcast` package is designed so an SVC
     forwarder can be added (layer = spatial+temporal tuple).
3. **Codec preference per room**: `Room.enabled_codecs` limits what clients may publish.
   Rooms with **recording enabled prefer H.264**: the server orders H.264 before VP8 in its
   answer to the publisher and the browser SDK selects H.264 by default when
   `JoinResponse.enabled_publish_codecs` lists it first. If a publisher cannot send H.264, VP8
   is accepted and the recorder transcodes (ADR-0007).
4. **Munging requirements** for layer switching (implemented from scratch in WP1):
   - Sequence numbers: continuous per DownTrack across switches, gaps preserved for real
     loss (so NACKs still work), with an extended-SN to original-SN map for NACK lookup.
   - Timestamps: continuous across switches using the reference layer's clock offset and the
     RTCP SR NTP/RTP mapping of each layer.
   - VP8: rewrite `PictureID` (15-bit), `TL0PICIDX` and `KEYIDX` to be continuous; drop
     temporal layers above the target with picture-id gap handling.
   - H.264: switch only on IDR (detected from NAL types 5/7/8 in single NAL, STAP-A, FU-A);
     no payload rewriting needed.
   - Opus: no rewriting; DTX-aware timestamp handling.

## Consequences

- Covers every target browser without transcoding in the SFU.
- H.264 hardware encoders on some devices do not support simulcast well; the SDK falls back
  to VP8 or single-layer H.264 when the browser reports encoder errors.
- Not supporting VP9/AV1 initially leaves some bandwidth efficiency on the table.

## Alternatives considered

- **VP9 SVC as primary**: better efficiency, but Safari support is partial and recording
  would always need transcoding. Deferred.
- **H.264 only**: Firefox on some platforms lacks H.264 (OpenH264 download), so VP8 stays.
