/**
 * Wires an aria-pressed button so no click is lost: each click flips the wanted state at
 * once and its change runs after the previous one has finished (a second click while the
 * first is still in progress, e.g. in the screen picker, undoes it afterwards).
 */
export function toggle(btn: HTMLElement, apply: (on: boolean) => Promise<unknown>, onError: (err: unknown) => void): void {
  let chain: Promise<unknown> = Promise.resolve();
  btn.addEventListener("click", () => {
    const on = btn.getAttribute("aria-pressed") !== "true";
    btn.setAttribute("aria-pressed", String(on));
    chain = chain.then(() => apply(on)).catch((err) => {
      btn.setAttribute("aria-pressed", String(!on));
      onError(err);
    });
  });
}
