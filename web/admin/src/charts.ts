import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';

export interface SeriesDef {
  label: string;
  /** CSS variable name of the line colour, e.g. "--c1". */
  color: string;
  get: (i: number) => number;
  fill?: boolean;
}
export interface ChartDef {
  title: string;
  series: SeriesDef[];
  fmt?: (v: number) => string;
  /** Fix the lower y bound to 0 (default true). */
  zero?: boolean;
  /** Whole-number y ticks (counts, ms), so small ranges do not repeat a rounded label. */
  int?: boolean;
}

const INT_INCRS = [1, 2, 5, 10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4, 2e4, 5e4, 1e5, 2e5, 5e5, 1e6, 2e6, 5e6, 1e7];

const cssVar = (n: string): string => getComputedStyle(document.documentElement).getPropertyValue(n).trim() || '#888';

/** A chart bound to a column source; call update() with the times (seconds) and length. */
export class Chart {
  private plot: uPlot;
  readonly el: HTMLElement;
  constructor(private def: ChartDef, parent: HTMLElement) {
    this.el = document.createElement('div');
    this.el.className = 'chart';
    this.el.innerHTML = `<div class="chart-title">${def.title}</div><div class="chart-body"></div>`;
    parent.appendChild(this.el);
    const body = this.el.querySelector('.chart-body') as HTMLElement;
    const fg = cssVar('--muted'), grid = cssVar('--grid');
    const fmt = def.fmt ?? ((v: number) => String(Math.round(v * 100) / 100));
    this.plot = new uPlot(
      {
        width: Math.max(200, body.clientWidth || 400),
        height: 160,
        legend: { show: true, live: true },
        cursor: { drag: { x: false, y: false } },
        scales: { x: { time: true }, y: { range: (_u, min, max) => [def.zero === false ? min : 0, Math.max(max * 1.1, def.zero === false ? max + 1 : 1)] } },
        axes: [
          { stroke: fg, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid } },
          { stroke: fg, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid }, size: 80, values: (_u, vals) => vals.map((v) => fmt(v)), ...(def.int ? { incrs: INT_INCRS } : {}) },
        ],
        series: [
          {},
          ...def.series.map((s) => ({
            label: s.label,
            stroke: cssVar(s.color),
            width: 1.6,
            fill: s.fill ? cssVar(s.color) + '33' : undefined,
            value: (_u: uPlot, v: number | null) => (v == null ? '–' : fmt(v)),
            points: { show: false },
          })),
        ],
      },
      [[], ...def.series.map(() => [])] as uPlot.AlignedData,
      body,
    );
    new ResizeObserver(() => this.plot.setSize({ width: Math.max(200, body.clientWidth), height: 160 })).observe(body);
  }
  update(times: number[], n: number): void {
    const data: number[][] = [times];
    for (const s of this.def.series) {
      const col = new Array<number>(n);
      for (let i = 0; i < n; i++) col[i] = s.get(i);
      data.push(col);
    }
    this.plot.setData(data as uPlot.AlignedData);
    // Without the pointer over the chart, the legend shows the latest values instead of dashes.
    if (n > 0 && (this.plot.cursor.left == null || this.plot.cursor.left < 0)) this.plot.setLegend({ idx: n - 1 });
  }
  destroy(): void {
    this.plot.destroy();
    this.el.remove();
  }
}
