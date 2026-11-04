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
}

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
          { stroke: fg, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid }, size: 80, values: (_u, vals) => vals.map((v) => fmt(v)) },
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
  }
  destroy(): void {
    this.plot.destroy();
    this.el.remove();
  }
}
