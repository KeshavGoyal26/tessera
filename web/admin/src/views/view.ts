import { Chart, type ChartDef } from '../charts';

export interface View {
  mount(root: HTMLElement, args: string[]): void;
  update(): void;
  destroy(): void;
}

/** Helper that owns a set of charts fed from a shared time axis. */
export class ChartGroup {
  charts: Chart[] = [];
  constructor(private parent: HTMLElement) {}
  add(def: ChartDef): void { this.charts.push(new Chart(def, this.parent)); }
  update(times: number[], n: number): void { this.charts.forEach((c) => c.update(times, n)); }
  destroy(): void { this.charts.forEach((c) => c.destroy()); this.charts = []; }
}
