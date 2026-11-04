/** Constraint for event maps: event name to listener signature. */
export type EventMap<T> = { [K in keyof T]: (...args: never[]) => void };

interface Registration {
  listener: (...args: never[]) => void;
  once: boolean;
}

/**
 * Minimal strongly typed event emitter.
 *
 * Listeners run synchronously in registration order. An exception thrown by a listener
 * propagates to the caller of {@link EventEmitter.emit} and skips the remaining listeners.
 *
 * @example
 * ```ts
 * interface Events { message: (text: string) => void }
 * const emitter = new EventEmitter<Events>();
 * emitter.on("message", (text) => console.log(text));
 * emitter.emit("message", "hello");
 * ```
 */
export class EventEmitter<Events extends EventMap<Events>> {
  #listeners = new Map<keyof Events, Registration[]>();

  /** Registers `listener` for `event`. */
  on<K extends keyof Events>(event: K, listener: Events[K]): this {
    return this.#add(event, listener, false);
  }

  /** Registers `listener` for the next emission of `event` only. */
  once<K extends keyof Events>(event: K, listener: Events[K]): this {
    return this.#add(event, listener, true);
  }

  /** Removes every registration of `listener` for `event`. */
  off<K extends keyof Events>(event: K, listener: Events[K]): this {
    this.#remove(event, (r) => r.listener !== listener);
    return this;
  }

  /**
   * Calls the listeners of `event` with `args`.
   *
   * @returns `true` if at least one listener was called.
   */
  emit<K extends keyof Events>(event: K, ...args: Parameters<Events[K]>): boolean {
    const list = this.#listeners.get(event);
    if (!list) return false;
    // Snapshot so listeners may add or remove listeners while we iterate.
    const snapshot = [...list];
    if (snapshot.some((r) => r.once)) this.#remove(event, (r) => !r.once);
    for (const reg of snapshot) {
      (reg.listener as (...a: Parameters<Events[K]>) => void)(...args);
    }
    return true;
  }

  /** Number of listeners registered for `event`. */
  listenerCount(event: keyof Events): number {
    return this.#listeners.get(event)?.length ?? 0;
  }

  /** Removes all listeners of `event`, or of every event when omitted. */
  removeAllListeners(event?: keyof Events): this {
    if (event === undefined) this.#listeners.clear();
    else this.#listeners.delete(event);
    return this;
  }

  #remove(event: keyof Events, keep: (r: Registration) => boolean): void {
    const rest = this.#listeners.get(event)?.filter(keep) ?? [];
    if (rest.length > 0) this.#listeners.set(event, rest);
    else this.#listeners.delete(event);
  }

  #add(event: keyof Events, listener: (...args: never[]) => void, once: boolean): this {
    const list = this.#listeners.get(event);
    if (list) list.push({ listener, once });
    else this.#listeners.set(event, [{ listener, once }]);
    return this;
  }
}

/** Events emitted by {@link Room}. See `RoomEventMap` for listener signatures. */
export enum RoomEvent {
  ParticipantConnected = "participantConnected",
  ParticipantDisconnected = "participantDisconnected",
  TrackPublished = "trackPublished",
  TrackUnpublished = "trackUnpublished",
  TrackSubscribed = "trackSubscribed",
  TrackUnsubscribed = "trackUnsubscribed",
  TrackMuted = "trackMuted",
  TrackUnmuted = "trackUnmuted",
  ActiveSpeakersChanged = "activeSpeakersChanged",
  ConnectionQualityChanged = "connectionQualityChanged",
  Reconnecting = "reconnecting",
  Reconnected = "reconnected",
  Disconnected = "disconnected",
  DataReceived = "dataReceived",
  RoomMetadataChanged = "roomMetadataChanged",
}
