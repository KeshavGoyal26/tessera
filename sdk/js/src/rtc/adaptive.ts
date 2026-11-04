/** Rendered state of one element a track is attached to. */
export interface ElementState {
  /** CSS pixels. */
  width: number;
  height: number;
  visible: boolean;
}

/** Subscriber settings derived from the attached elements. */
export interface AdaptiveSettings {
  /** True when no element shows the track: forwarding can pause. */
  disabled: boolean;
  /** Pixel size to request (0 when disabled). */
  width: number;
  height: number;
}

/**
 * Decides what to ask the server for: the largest visible element wins (in device pixels),
 * and a track nobody can see is disabled.
 */
export function computeAdaptiveSettings(elements: readonly ElementState[], pixelRatio = 1): AdaptiveSettings {
  let width = 0;
  let height = 0;
  for (const e of elements) {
    if (!e.visible || e.width <= 0 || e.height <= 0) continue;
    width = Math.max(width, e.width);
    height = Math.max(height, e.height);
  }
  if (width === 0) return { disabled: true, width: 0, height: 0 };
  return { disabled: false, width: Math.ceil(width * pixelRatio), height: Math.ceil(height * pixelRatio) };
}

/**
 * Watches the elements a video track is attached to (size via `ResizeObserver`, visibility
 * via `IntersectionObserver` and the page visibility) and reports changed settings, debounced.
 */
export class AdaptiveStream {
  readonly #states = new Map<HTMLElement, ElementState>();
  #resize: ResizeObserver | undefined;
  #intersect: IntersectionObserver | undefined;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #last: AdaptiveSettings | undefined;
  readonly #apply: (s: AdaptiveSettings) => void;
  readonly #debounceMs: number;
  readonly #onVisibility = () => this.#schedule();

  constructor(apply: (settings: AdaptiveSettings) => void, debounceMs = 100) {
    this.#apply = apply;
    this.#debounceMs = debounceMs;
    if (typeof ResizeObserver !== "undefined") {
      this.#resize = new ResizeObserver((entries) => {
        for (const en of entries) {
          const st = this.#states.get(en.target as HTMLElement);
          if (st) {
            st.width = en.contentRect.width;
            st.height = en.contentRect.height;
          }
        }
        this.#schedule();
      });
    }
    if (typeof IntersectionObserver !== "undefined") {
      this.#intersect = new IntersectionObserver((entries) => {
        for (const en of entries) {
          const st = this.#states.get(en.target as HTMLElement);
          if (st) st.visible = en.isIntersecting;
        }
        this.#schedule();
      });
    }
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", this.#onVisibility);
  }

  /** Starts observing `el`. */
  observe(el: HTMLElement): void {
    const rect = el.getBoundingClientRect();
    this.#states.set(el, { width: rect.width, height: rect.height, visible: true });
    this.#resize?.observe(el);
    this.#intersect?.observe(el);
    this.#schedule();
  }

  /** Stops observing `el`. */
  unobserve(el: HTMLElement): void {
    this.#states.delete(el);
    this.#resize?.unobserve(el);
    this.#intersect?.unobserve(el);
    this.#schedule();
  }

  /** Releases observers and timers. */
  dispose(): void {
    clearTimeout(this.#timer);
    this.#resize?.disconnect();
    this.#intersect?.disconnect();
    if (typeof document !== "undefined") document.removeEventListener("visibilitychange", this.#onVisibility);
    this.#states.clear();
  }

  /** Current settings, computed immediately. */
  current(): AdaptiveSettings {
    const hidden = typeof document !== "undefined" && document.hidden;
    const dpr = typeof devicePixelRatio === "number" ? devicePixelRatio : 1;
    return hidden ? { disabled: true, width: 0, height: 0 } : computeAdaptiveSettings([...this.#states.values()], dpr);
  }

  #schedule(): void {
    clearTimeout(this.#timer);
    this.#timer = setTimeout(() => {
      const s = this.current();
      const l = this.#last;
      if (l && l.disabled === s.disabled && l.width === s.width && l.height === s.height) return;
      this.#last = s;
      this.#apply(s);
    }, this.#debounceMs);
  }
}
