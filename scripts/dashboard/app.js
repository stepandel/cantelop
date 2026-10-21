"use strict";

/* ---------------------------------------------------------------- helpers */

const SVG_NS = "http://www.w3.org/2000/svg";

function svgEl(tag, attributes = {}, textContent = null) {
  const element = document.createElementNS(SVG_NS, tag);
  for (const [name, value] of Object.entries(attributes)) {
    element.setAttribute(name, String(value));
  }
  if (textContent !== null) {
    element.textContent = textContent;
  }
  return element;
}

function htmlEl(tag, className = null, textContent = null) {
  const element = document.createElement(tag);
  if (className) {
    element.className = className;
  }
  if (textContent !== null) {
    element.textContent = textContent;
  }
  return element;
}

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

const NS_PER_S = 1e9;

function formatNs(ns) {
  const abs = Math.abs(ns);
  if (abs >= 1e9) return `${(ns / 1e9).toFixed(abs >= 1e10 ? 1 : 2)} s`;
  if (abs >= 1e6) return `${(ns / 1e6).toFixed(1)} ms`;
  if (abs >= 1e3) return `${(ns / 1e3).toFixed(1)} µs`;
  return `${Math.round(ns)} ns`;
}

function formatSeconds(ns) {
  return `${(ns / NS_PER_S).toFixed(3)} s`;
}

function niceTicks(maxValue, count) {
  const rawStep = maxValue / count;
  const magnitude = 10 ** Math.floor(Math.log10(rawStep));
  const step =
    [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= rawStep) ||
    10 * magnitude;
  const ticks = [0];
  while (ticks[ticks.length - 1] < maxValue - step * 1e-9) {
    ticks.push(ticks.length * step);
  }
  return ticks;
}

/* ------------------------------------------------------------- data model */

async function loadHistory() {
  const response = await fetch("/api/history");
  if (!response.ok) {
    throw new Error(`history request failed: HTTP ${response.status}`);
  }
  return response.json();
}

function parseSeries(payload) {
  // The server validates every summary with report-vm-warmup.py's parser and
  // lists invalid files in payload.errors, so each entry arrives complete.
  return payload.series.map((item) => ({
    file: item.file,
    measuredOn: item.measured_on,
    seriesId: item.series_id,
    shortId: item.series_id.slice(0, 8),
    host: item.host,
    profile: item.profile,
    imageId: item.image_id,
    vcpus: item.vcpus,
    memoryMib: item.memory_mib,
    trials: item.trials,
    startup: item.startup,
    shutdownMedianNs: item.shutdown_median_ns,
    trace: item.trace,
    metrics: item.metrics,
  }));
}

/* ---------------------------------------------------------------- tooltip */

const tooltip = {
  element: null,
  show(clientX, clientY, build) {
    if (!this.element) this.element = document.getElementById("tooltip");
    this.element.replaceChildren();
    build(this.element);
    this.element.hidden = false;
    const { innerWidth, innerHeight } = window;
    const rect = this.element.getBoundingClientRect();
    let x = clientX + 14;
    let y = clientY + 14;
    if (x + rect.width > innerWidth - 8) x = clientX - rect.width - 14;
    if (y + rect.height > innerHeight - 8) y = clientY - rect.height - 14;
    this.element.style.left = `${Math.max(8, x)}px`;
    this.element.style.top = `${Math.max(8, y)}px`;
  },
  hide() {
    if (this.element) this.element.hidden = true;
  },
};

function tooltipRow(table, label, value, keyColor = null) {
  const row = table.insertRow();
  const labelCell = row.insertCell();
  labelCell.className = "label";
  if (keyColor) {
    const key = htmlEl("span", "key-line");
    key.style.background = keyColor;
    labelCell.append(key);
  }
  labelCell.append(document.createTextNode(label));
  row.insertCell().textContent = value;
  row.cells[1].className = "value";
}

/* ---------------------------------------------------------------- KPI row */

function renderKpis(entries) {
  const row = document.getElementById("kpi-row");
  row.replaceChildren();
  const latest = entries[entries.length - 1];
  const previous = entries.length > 1 ? entries[entries.length - 2] : null;

  const hero = htmlEl("div", "kpi kpi-hero");
  hero.append(htmlEl("div", "kpi-label", `Warmup median · ${latest.measuredOn}`));
  hero.append(htmlEl("div", "kpi-value", formatSeconds(latest.startup.median_ns)));
  if (previous) {
    const deltaNs = latest.startup.median_ns - previous.startup.median_ns;
    const pct = (deltaNs / previous.startup.median_ns) * 100;
    const good = deltaNs < 0;
    const delta = htmlEl(
      "div",
      `kpi-delta ${good ? "good" : "bad"}`,
      `${good ? "▼" : "▲"} ${formatNs(Math.abs(deltaNs))} (${good ? "" : "+"}${pct.toFixed(1)}%) vs ${previous.measuredOn}`
    );
    hero.append(delta);
  }
  row.append(hero);

  const tiles = [
    ["Warmup p95 (nearest-rank)", formatSeconds(latest.startup.p95_ns)],
    ["Warmup max", formatSeconds(latest.startup.max_ns)],
    [
      "Shutdown median",
      latest.shutdownMedianNs != null ? formatSeconds(latest.shutdownMedianNs) : "n/a",
    ],
    [
      "Latest series",
      `${latest.trials} trial(s) · ${latest.vcpus} vCPU / ${latest.memoryMib} MiB · ${latest.host}`,
      "kpi-text",
    ],
  ];
  for (const [label, value, extraClass] of tiles) {
    const tile = htmlEl("div", `kpi${extraClass ? ` ${extraClass}` : ""}`);
    tile.append(htmlEl("div", "kpi-label", label));
    tile.append(htmlEl("div", "kpi-value", value));
    row.append(tile);
  }
}

/* ------------------------------------------------------------ trend chart */

function renderTrendChart(entries) {
  const container = document.getElementById("trend-chart");
  container.replaceChildren();

  const medianColor = cssVar("--series-1");
  const p95Color = cssVar("--text-muted");

  const legend = htmlEl("ul", "legend");
  for (const [cls, color, label] of [
    ["key-line", medianColor, "median"],
    ["key-line", p95Color, "p95 (nearest-rank)"],
    ["key-band", medianColor, "min–max range"],
  ]) {
    const item = htmlEl("li");
    const key = htmlEl("span", cls);
    key.style.background = color;
    if (cls === "key-band") key.style.opacity = "0.18";
    item.append(key, document.createTextNode(label));
    legend.append(item);
  }
  container.append(legend);

  const width = Math.max(container.clientWidth || 720, 480);
  const height = 260;
  const margin = { top: 12, right: 64, bottom: 34, left: 64 };
  const plotW = width - margin.left - margin.right;
  const plotH = height - margin.top - margin.bottom;

  const svg = svgEl("svg", {
    viewBox: `0 0 ${width} ${height}`,
    width,
    height,
    role: "img",
    "aria-label": "VM warmup latency by benchmark series",
  });

  const maxNs = Math.max(...entries.map((e) => e.startup.max_ns));
  const yTicks = niceTicks(maxNs / NS_PER_S, 5);
  const yMax = yTicks[yTicks.length - 1] * NS_PER_S;
  const x = (index) =>
    margin.left +
    (entries.length === 1 ? plotW / 2 : (plotW * index) / (entries.length - 1));
  const y = (ns) => margin.top + plotH * (1 - ns / yMax);

  for (const tick of yTicks) {
    const ty = y(tick * NS_PER_S);
    svg.append(
      svgEl("line", {
        x1: margin.left,
        x2: margin.left + plotW,
        y1: ty,
        y2: ty,
        stroke: tick === 0 ? "var(--baseline)" : "var(--gridline)",
        "stroke-width": 1,
      })
    );
    svg.append(
      svgEl(
        "text",
        { x: margin.left - 8, y: ty + 4, "text-anchor": "end", class: "tick-label" },
        `${tick} s`
      )
    );
  }

  for (const [index, entry] of entries.entries()) {
    svg.append(
      svgEl(
        "text",
        { x: x(index), y: height - 14, "text-anchor": "middle" },
        entry.measuredOn
      )
    );
    svg.append(
      svgEl(
        "text",
        { x: x(index), y: height - 2, "text-anchor": "middle" },
        entry.shortId
      )
    );
  }

  const bandPoints = entries
    .map((e, i) => `${x(i)},${y(e.startup.max_ns)}`)
    .concat(entries.map((e, i) => `${x(entries.length - 1 - i)},${y(entries[entries.length - 1 - i].startup.min_ns)}`))
    .join(" ");
  svg.append(
    svgEl("polygon", { points: bandPoints, fill: medianColor, opacity: 0.1 })
  );

  const linePath = (pick, color) => {
    if (entries.length > 1) {
      svg.append(
        svgEl("polyline", {
          points: entries.map((e, i) => `${x(i)},${y(pick(e))}`).join(" "),
          fill: "none",
          stroke: color,
          "stroke-width": 2,
          "stroke-linejoin": "round",
          "stroke-linecap": "round",
        })
      );
    }
    for (const [index, entry] of entries.entries()) {
      svg.append(
        svgEl("circle", {
          cx: x(index),
          cy: y(pick(entry)),
          r: 4,
          fill: color,
          stroke: "var(--surface-1)",
          "stroke-width": 2,
        })
      );
    }
  };
  linePath((e) => e.startup.p95_ns, p95Color);
  linePath((e) => e.startup.median_ns, medianColor);

  const crosshair = svgEl("line", {
    y1: margin.top,
    y2: margin.top + plotH,
    stroke: "var(--baseline)",
    "stroke-width": 1,
    visibility: "hidden",
  });
  svg.append(crosshair);

  const hover = svgEl("rect", {
    x: margin.left,
    y: margin.top,
    width: plotW,
    height: plotH,
    fill: "transparent",
  });
  hover.addEventListener("pointermove", (event) => {
    const rect = svg.getBoundingClientRect();
    const px = ((event.clientX - rect.left) / rect.width) * width;
    let nearest = 0;
    for (let i = 1; i < entries.length; i += 1) {
      if (Math.abs(x(i) - px) < Math.abs(x(nearest) - px)) nearest = i;
    }
    const entry = entries[nearest];
    crosshair.setAttribute("x1", x(nearest));
    crosshair.setAttribute("x2", x(nearest));
    crosshair.setAttribute("visibility", "visible");
    tooltip.show(event.clientX, event.clientY, (root) => {
      root.append(
        htmlEl(
          "div",
          "tooltip-title",
          `${entry.measuredOn} · ${entry.shortId} · ${entry.trials} trial(s)`
        )
      );
      const table = document.createElement("table");
      tooltipRow(table, "median", formatSeconds(entry.startup.median_ns), medianColor);
      tooltipRow(
        table,
        "p95 (nearest-rank)",
        formatSeconds(entry.startup.p95_ns),
        p95Color
      );
      tooltipRow(table, "min", formatSeconds(entry.startup.min_ns));
      tooltipRow(table, "max", formatSeconds(entry.startup.max_ns));
      root.append(table);
    });
  });
  hover.addEventListener("pointerleave", () => {
    crosshair.setAttribute("visibility", "hidden");
    tooltip.hide();
  });
  svg.append(hover);

  container.append(svg);
}

function renderTrendTable(entries) {
  const container = document.getElementById("trend-table");
  container.replaceChildren();
  const table = htmlEl("table", "data");
  const head = table.createTHead().insertRow();
  for (const [label, numeric] of [
    ["Date", false],
    ["Series", false],
    ["Host", false],
    ["Profile", false],
    ["Image", false],
    ["VM", false],
    ["Trials", true],
    ["Min", true],
    ["Median", true],
    ["p95", true],
    ["Max", true],
  ]) {
    const cell = htmlEl("th", numeric ? "num" : null, label);
    head.append(cell);
  }
  const body = table.createTBody();
  for (const entry of entries) {
    const row = body.insertRow();
    const cells = [
      [entry.measuredOn, false],
      [entry.shortId, false],
      [entry.host, false],
      [entry.profile, false],
      [entry.imageId ? entry.imageId.slice(0, 19) : "unrecorded", false],
      [`${entry.vcpus} vCPU / ${entry.memoryMib} MiB`, false],
      [String(entry.trials), true],
      [formatSeconds(entry.startup.min_ns), true],
      [formatSeconds(entry.startup.median_ns), true],
      [formatSeconds(entry.startup.p95_ns), true],
      [formatSeconds(entry.startup.max_ns), true],
    ];
    for (const [value, numeric] of cells) {
      const cell = row.insertCell();
      cell.textContent = value;
      if (numeric) cell.className = "num";
    }
  }
  container.append(table);
}

/* ---------------------------------------------------- warmup step timeline */

const PHASE_GROUPS = [
  { key: "control", label: "control plane", var: "--series-1" },
  { key: "store", label: "store", var: "--series-2" },
  { key: "jail", label: "jail assets", var: "--series-3" },
  { key: "network", label: "network", var: "--series-4" },
  { key: "workspace", label: "workspace", var: "--series-5" },
  { key: "firecracker", label: "firecracker", var: "--series-6" },
  { key: "guest", label: "guest readiness", var: "--series-7" },
];
const CONTROL_PREFIXES = new Set(["http", "service", "supervisor", "admission"]);

function phaseGroup(stepName) {
  const prefix = stepName.split(".", 1)[0];
  const key = CONTROL_PREFIXES.has(prefix) ? "control" : prefix;
  return PHASE_GROUPS.find((group) => group.key === key) || PHASE_GROUPS[0];
}

function hexLuminance(hex) {
  const channel = (offset) => {
    const value = parseInt(hex.slice(offset, offset + 2), 16) / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5);
}

function timelineLegend(groupsInUse) {
  const legend = htmlEl("ul", "legend");
  for (const group of PHASE_GROUPS) {
    if (!groupsInUse.has(group.key)) continue;
    const item = htmlEl("li");
    const key = htmlEl("span", "key-swatch");
    key.style.background = cssVar(group.var);
    item.append(key, document.createTextNode(group.label));
    legend.append(item);
  }
  return legend;
}

function renderTimeline(entry) {
  const container = document.getElementById("timeline-chart");
  const empty = document.getElementById("timeline-empty");
  container.replaceChildren();
  if (!entry.trace) {
    empty.hidden = false;
    return;
  }
  empty.hidden = true;

  const steps = [...entry.trace.steps].sort(
    (a, b) =>
      a.median_started_ns - b.median_started_ns ||
      a.name.localeCompare(b.name)
  );
  const totalNs = entry.trace.median_ns;
  const maxEndNs = Math.max(
    totalNs,
    ...steps.map((s) => s.median_started_ns + s.median_duration_ns)
  );

  container.append(timelineLegend(new Set(steps.map((s) => phaseGroup(s.name).key))));

  const rowH = 17;
  const width = Math.max(container.clientWidth || 960, 720);
  const margin = { top: 26, right: 16, bottom: 6, left: 300 };
  const plotW = width - margin.left - margin.right;
  const height = margin.top + steps.length * rowH + margin.bottom;
  const svg = svgEl("svg", {
    viewBox: `0 0 ${width} ${height}`,
    width,
    height,
    role: "img",
    "aria-label": "Warmup host step timeline",
  });

  const x = (ns) => margin.left + (plotW * ns) / maxEndNs;

  for (const tick of niceTicks(maxEndNs / NS_PER_S, 8)) {
    const tx = x(tick * NS_PER_S);
    if (tx > margin.left + plotW + 1) continue;
    svg.append(
      svgEl("line", {
        x1: tx,
        x2: tx,
        y1: margin.top - 4,
        y2: height - margin.bottom,
        stroke: tick === 0 ? "var(--baseline)" : "var(--gridline)",
        "stroke-width": 1,
      })
    );
    svg.append(
      svgEl(
        "text",
        { x: tx, y: margin.top - 10, "text-anchor": "middle", class: "tick-label" },
        `${tick} s`
      )
    );
  }

  for (const [index, step] of steps.entries()) {
    const group = phaseGroup(step.name);
    const color = cssVar(group.var);
    const rowY = margin.top + index * rowH;
    const startNs = step.median_started_ns;
    const durationNs = step.median_duration_ns;
    const barX = x(startNs);
    const barW = Math.max(x(startNs + durationNs) - barX, 2);
    const share = durationNs / totalNs;

    const rowWash = svgEl("rect", {
      x: 0,
      y: rowY,
      width,
      height: rowH,
      fill: "transparent",
    });
    svg.append(rowWash);

    svg.append(
      svgEl(
        "text",
        {
          x: margin.left - 10,
          y: rowY + rowH / 2 + 4,
          "text-anchor": "end",
          fill: "var(--text-secondary)",
        },
        step.name
      )
    );
    svg.append(
      svgEl("rect", {
        x: barX,
        y: rowY + 3,
        width: barW,
        height: rowH - 6,
        rx: 2,
        fill: color,
      })
    );

    if (share >= 0.05) {
      const label = formatNs(durationNs);
      const estimatedW = label.length * 6.6 + 8;
      const fitsOutside = barX + barW + estimatedW < margin.left + plotW;
      const attributes = fitsOutside
        ? { x: barX + barW + 6, "text-anchor": "start", fill: "var(--text-secondary)" }
        : {
            x: barX + barW - 6,
            "text-anchor": "end",
            fill: hexLuminance(color) > 0.45 ? "#0b0b0b" : "#ffffff",
          };
      if (fitsOutside || barW > estimatedW + 12) {
        svg.append(
          svgEl("text", { ...attributes, y: rowY + rowH / 2 + 4 }, label)
        );
      }
    }

    const showTooltip = (event) => {
      rowWash.setAttribute("fill", "var(--gridline)");
      rowWash.setAttribute("opacity", "0.45");
      const point =
        event.clientX !== undefined
          ? event
          : { clientX: window.innerWidth / 2, clientY: rowY };
      tooltip.show(point.clientX, point.clientY, (root) => {
        root.append(htmlEl("div", "tooltip-title", `${step.name} · ${group.label}`));
        const table = document.createElement("table");
        tooltipRow(table, "median duration", formatNs(durationNs), color);
        tooltipRow(table, "p95 duration", formatNs(step.p95_duration_ns));
        tooltipRow(table, "starts at", formatSeconds(startNs));
        tooltipRow(table, "share of warmup", `${(share * 100).toFixed(1)}%`);
        if (step.occurrences > 1) {
          tooltipRow(table, "occurrences / trial", String(step.occurrences));
        }
        root.append(table);
      });
    };
    const hideTooltip = () => {
      rowWash.setAttribute("fill", "transparent");
      rowWash.removeAttribute("opacity");
      tooltip.hide();
    };
    rowWash.addEventListener("pointermove", showTooltip);
    rowWash.addEventListener("pointerleave", hideTooltip);
    rowWash.setAttribute("tabindex", "0");
    rowWash.addEventListener("focus", showTooltip);
    rowWash.addEventListener("blur", hideTooltip);
  }

  container.append(svg);
}

function renderStepsTable(entry) {
  const container = document.getElementById("steps-table");
  container.replaceChildren();
  if (!entry.trace) return;
  const totalNs = entry.trace.median_ns;
  const steps = [...entry.trace.steps].sort(
    (a, b) => b.median_duration_ns - a.median_duration_ns
  );
  const table = htmlEl("table", "data");
  const head = table.createTHead().insertRow();
  for (const [label, numeric] of [
    ["Step", false],
    ["Group", false],
    ["Occurrences", true],
    ["Median start", true],
    ["Median duration", true],
    ["p95 duration", true],
    ["Share", true],
  ]) {
    head.append(htmlEl("th", numeric ? "num" : null, label));
  }
  const body = table.createTBody();
  for (const step of steps) {
    const row = body.insertRow();
    const cells = [
      [step.name, false],
      [phaseGroup(step.name).label, false],
      [String(step.occurrences), true],
      [formatSeconds(step.median_started_ns), true],
      [formatNs(step.median_duration_ns), true],
      [formatNs(step.p95_duration_ns), true],
      [`${((step.median_duration_ns / totalNs) * 100).toFixed(1)}%`, true],
    ];
    for (const [value, numeric] of cells) {
      const cell = row.insertCell();
      cell.textContent = value;
      if (numeric) cell.className = "num";
    }
  }
  container.append(table);
}

/* --------------------------------------------------------- guest milestones */

function renderMilestones(entry) {
  const container = document.getElementById("milestones-chart");
  const empty = document.getElementById("milestones-empty");
  container.replaceChildren();
  const milestones = (entry.trace && entry.trace.guest_milestones) || [];
  if (!milestones.length) {
    empty.hidden = false;
    return;
  }
  empty.hidden = true;

  const sorted = [...milestones].sort(
    (a, b) =>
      a.median_uptime_ns - b.median_uptime_ns ||
      a.name.localeCompare(b.name)
  );
  const color = cssVar("--series-1");
  const rowH = 22;
  const width = Math.max(container.clientWidth || 960, 720);
  const margin = { top: 26, right: 24, bottom: 6, left: 300 };
  const plotW = width - margin.left - margin.right;
  const height = margin.top + sorted.length * rowH + margin.bottom;
  const maxNs = Math.max(...sorted.map((m) => m.p95_uptime_ns));
  const svg = svgEl("svg", {
    viewBox: `0 0 ${width} ${height}`,
    width,
    height,
    role: "img",
    "aria-label": "Guest boot milestones over guest uptime",
  });
  const x = (ns) => margin.left + (plotW * ns) / (maxNs * 1.05);

  for (const tick of niceTicks((maxNs * 1.05) / NS_PER_S, 8)) {
    const tx = x(tick * NS_PER_S);
    if (tx > margin.left + plotW + 1) continue;
    svg.append(
      svgEl("line", {
        x1: tx,
        x2: tx,
        y1: margin.top - 4,
        y2: height - margin.bottom,
        stroke: tick === 0 ? "var(--baseline)" : "var(--gridline)",
        "stroke-width": 1,
      })
    );
    svg.append(
      svgEl(
        "text",
        { x: tx, y: margin.top - 10, "text-anchor": "middle", class: "tick-label" },
        `${tick} s`
      )
    );
  }

  for (const [index, milestone] of sorted.entries()) {
    const rowY = margin.top + index * rowH;
    const centerY = rowY + rowH / 2;
    const medianX = x(milestone.median_uptime_ns);
    const p95X = x(milestone.p95_uptime_ns);

    const rowWash = svgEl("rect", {
      x: 0,
      y: rowY,
      width,
      height: rowH,
      fill: "transparent",
    });
    svg.append(rowWash);
    svg.append(
      svgEl(
        "text",
        {
          x: margin.left - 10,
          y: centerY + 4,
          "text-anchor": "end",
          fill: "var(--text-secondary)",
        },
        milestone.name
      )
    );
    if (p95X - medianX > 1) {
      svg.append(
        svgEl("line", {
          x1: medianX,
          x2: p95X,
          y1: centerY,
          y2: centerY,
          stroke: "var(--text-muted)",
          "stroke-width": 2,
          "stroke-linecap": "round",
        })
      );
    }
    svg.append(
      svgEl("circle", {
        cx: medianX,
        cy: centerY,
        r: 4,
        fill: color,
        stroke: "var(--surface-1)",
        "stroke-width": 2,
      })
    );

    const showTooltip = (event) => {
      rowWash.setAttribute("fill", "var(--gridline)");
      rowWash.setAttribute("opacity", "0.45");
      const point =
        event.clientX !== undefined
          ? event
          : { clientX: window.innerWidth / 2, clientY: rowY };
      tooltip.show(point.clientX, point.clientY, (root) => {
        root.append(htmlEl("div", "tooltip-title", milestone.name));
        const table = document.createElement("table");
        tooltipRow(
          table,
          "median uptime",
          formatSeconds(milestone.median_uptime_ns),
          color
        );
        tooltipRow(
          table,
          "p95 uptime",
          formatSeconds(milestone.p95_uptime_ns)
        );
        root.append(table);
      });
    };
    const hideTooltip = () => {
      rowWash.setAttribute("fill", "transparent");
      rowWash.removeAttribute("opacity");
      tooltip.hide();
    };
    rowWash.addEventListener("pointermove", showTooltip);
    rowWash.addEventListener("pointerleave", hideTooltip);
    rowWash.setAttribute("tabindex", "0");
    rowWash.addEventListener("focus", showTooltip);
    rowWash.addEventListener("blur", hideTooltip);
  }

  container.append(svg);
}

function renderMilestonesTable(entry) {
  const container = document.getElementById("milestones-table");
  container.replaceChildren();
  const milestones = (entry.trace && entry.trace.guest_milestones) || [];
  if (!milestones.length) return;
  const table = htmlEl("table", "data");
  const head = table.createTHead().insertRow();
  for (const [label, numeric] of [
    ["Milestone", false],
    ["Median boot uptime", true],
    ["p95 boot uptime", true],
  ]) {
    head.append(htmlEl("th", numeric ? "num" : null, label));
  }
  const body = table.createTBody();
  for (const milestone of [...milestones].sort(
    (a, b) => a.median_uptime_ns - b.median_uptime_ns
  )) {
    const row = body.insertRow();
    row.insertCell().textContent = milestone.name;
    for (const value of [
      formatSeconds(milestone.median_uptime_ns),
      formatSeconds(milestone.p95_uptime_ns),
    ]) {
      const cell = row.insertCell();
      cell.textContent = value;
      cell.className = "num";
    }
  }
  container.append(table);
}

/* -------------------------------------------------------- workload metrics */

function formatBytesPerSecond(value) {
  if (value == null) return "—";
  if (value >= 1e9) return `${(value / 1e9).toFixed(2)} GB/s`;
  if (value >= 1e6) return `${(value / 1e6).toFixed(1)} MB/s`;
  if (value >= 1e3) return `${(value / 1e3).toFixed(1)} kB/s`;
  return `${Math.round(value)} B/s`;
}

function renderWorkloadsTable(entry) {
  const card = document.getElementById("workloads-card");
  const container = document.getElementById("workloads-table");
  container.replaceChildren();
  if (!entry.metrics.length) {
    card.hidden = true;
    return;
  }
  card.hidden = false;
  const table = htmlEl("table", "data");
  const head = table.createTHead().insertRow();
  for (const [label, numeric] of [
    ["Workload", false],
    ["Cache state", false],
    ["Concurrency", true],
    ["Samples/trial", true],
    ["Median duration", true],
    ["Median throughput", true],
    ["Median ops/s", true],
  ]) {
    head.append(htmlEl("th", numeric ? "num" : null, label));
  }
  const body = table.createTBody();
  const metrics = [...entry.metrics].sort(
    (a, b) =>
      (a.workload || "").localeCompare(b.workload || "") ||
      (a.cache_state || "").localeCompare(b.cache_state || "")
  );
  for (const metric of metrics) {
    const row = body.insertRow();
    const cells = [
      [metric.workload || "—", false],
      [metric.cache_state || "single-vm", false],
      [metric.concurrency == null ? "1" : String(metric.concurrency), true],
      [
        metric.samples_per_trial == null ? "—" : String(metric.samples_per_trial),
        true,
      ],
      [metric.duration_ns ? formatNs(metric.duration_ns.median) : "—", true],
      [
        metric.bytes_per_second
          ? formatBytesPerSecond(metric.bytes_per_second.median)
          : "—",
        true,
      ],
      [
        metric.operations_per_second != null && metric.operations_per_second
          ? String(metric.operations_per_second.median)
          : "—",
        true,
      ],
    ];
    for (const [value, numeric] of cells) {
      const cell = row.insertCell();
      cell.textContent = value;
      if (numeric) cell.className = "num";
    }
    if (metric.cache_state) {
      row.cells[1].title = [metric.cache_scope, metric.cache_guarantee]
        .filter(Boolean)
        .join(" · ");
    }
  }
  container.append(table);
}

/* ----------------------------------------------------------- detail scope */

function selectedDetailEntry() {
  const select = document.getElementById("series-select");
  return (
    currentEntries.find((entry) => entry.file === select.value) ||
    currentEntries[currentEntries.length - 1]
  );
}

function populateSeriesSelect(entries) {
  const select = document.getElementById("series-select");
  select.replaceChildren();
  for (const entry of entries) {
    const option = document.createElement("option");
    option.value = entry.file;
    option.textContent =
      `${entry.measuredOn} · ${entry.shortId} · ${entry.host}` +
      (entry.trace ? "" : " (no trace)");
    select.append(option);
  }
  let preferred = entries[entries.length - 1];
  for (const entry of entries) {
    if (entry.trace) preferred = entry;
  }
  select.value = preferred.file;
  select.onchange = renderDetail;
}

function renderDetail() {
  const entry = selectedDetailEntry();
  const meta = document.getElementById("detail-meta");
  meta.textContent =
    `${entry.trials} trial(s) · ${entry.vcpus} vCPU / ${entry.memoryMib} MiB · ` +
    `${entry.profile} · ${entry.imageId || "image identity unrecorded"} · ${entry.file}`;
  renderTimeline(entry);
  renderStepsTable(entry);
  renderMilestones(entry);
  renderMilestonesTable(entry);
  renderWorkloadsTable(entry);
}

/* ------------------------------------------------------------------- main */

function renderSource(payload) {
  const source = document.getElementById("history-source");
  const count = payload.series.length;
  source.textContent =
    `${count} tracked series from ${payload.history_dir}` +
    (payload.errors.length ? ` — ${payload.errors.length} unreadable file(s)` : "");
}

let currentEntries = [];

function renderAll() {
  if (!currentEntries.length) return;
  renderKpis(currentEntries);
  renderTrendChart(currentEntries);
  renderTrendTable(currentEntries);
  renderDetail();
}

async function main() {
  const status = document.getElementById("load-status");
  try {
    const payload = await loadHistory();
    renderSource(payload);
    currentEntries = parseSeries(payload);
    if (!currentEntries.length) {
      status.textContent = "No benchmark series found.";
      return;
    }
    status.hidden = true;
    document.getElementById("overview").hidden = false;
    document.getElementById("detail").hidden = false;
    populateSeriesSelect(currentEntries);
    renderAll();
  } catch (error) {
    status.textContent = String(error);
  }
}

let resizeTimer = null;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(renderAll, 150);
});

main();
