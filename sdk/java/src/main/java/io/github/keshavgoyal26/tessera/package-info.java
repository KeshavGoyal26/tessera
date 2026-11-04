/**
 * Tessera server SDK for Java.
 *
 * <ul>
 *   <li>{@link io.github.keshavgoyal26.tessera.AccessToken} and {@link
 *       io.github.keshavgoyal26.tessera.RoomGrant}: mint HS256 JWT access tokens for clients and
 *       backends (ADR-0004).
 *   <li>{@link io.github.keshavgoyal26.tessera.RoomServiceClient} and {@link
 *       io.github.keshavgoyal26.tessera.RecordingServiceClient}: call the Twirp admin API with
 *       binary protobuf (ADR-0008), configured by {@link
 *       io.github.keshavgoyal26.tessera.TesseraConfig}.
 *   <li>{@link io.github.keshavgoyal26.tessera.WebhookReceiver}: verify and decode webhook
 *       requests.
 *   <li>{@link io.github.keshavgoyal26.tessera.TesseraException}: errors carrying the Twirp code.
 * </ul>
 *
 * <p>Request, response and model types are generated from {@code proto/tessera/v1} into {@code
 * io.github.keshavgoyal26.tessera.proto.v1}. Classes in {@code io.github.keshavgoyal26.tessera.internal}
 * are implementation details and may change without notice.
 */
package io.github.keshavgoyal26.tessera;
