// Every reading in the console is drawn in the viewer's own time zone: an
// operator matching a request against their own clock should not have to do
// the arithmetic first. Only the browser knows that zone, so components take
// their formatters from useClock() (lib/use-clock) rather than importing one
// bound to a zone at module load — see the note there on why the server pass
// cannot resolve it.
export interface Clock {
  // IANA name of the zone every reading below is drawn in.
  zone: string;
  // "Aug 21, 2026, 23:57:42 PDT" — the whole reading, named zone included, for
  // the title on a cell that only has room for the short form.
  formatDate(value: string): string;
  // "23:57:42" — for readings a heading or a neighbouring row already dates.
  formatTime(value: string): string;
  // "08-21 23:57:42" — record tables hold days of history, where a bare clock
  // reading names a different instant every 24 hours.
  formatStamp(value: string): string;
  // "Aug 21 23:57" — axis label for a window that crosses midnight.
  formatDayTime(value: string): string;
  // The zone's short name at that instant ("PDT", "GMT+2"), for a reading
  // printed away from any header that could carry the zone.
  formatZone(value: string): string;
  // The calendar day that instant falls on, for asking whether two of them
  // share one.
  dayOf(value: string): string;
}

const clocks = new Map<string, Clock>();

// hourCycle h23 rather than hour12: false — the latter renders midnight as
// "24:00:00" on some engines.
export function clockFor(zone: string): Clock {
  const cached = clocks.get(zone);
  if (cached) return cached;

  const dateTime = new Intl.DateTimeFormat("en", {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hourCycle: "h23", timeZoneName: "short", timeZone: zone,
  });
  const time = new Intl.DateTimeFormat("en", {
    hour: "2-digit", minute: "2-digit", second: "2-digit",
    hourCycle: "h23", timeZone: zone,
  });
  const stampDay = new Intl.DateTimeFormat("en", { month: "2-digit", day: "2-digit", timeZone: zone });
  const dayTime = new Intl.DateTimeFormat("en", {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
    hourCycle: "h23", timeZone: zone,
  });
  // en-CA is the ISO-ordered English locale: "2026-08-21", which compares.
  const day = new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit", timeZone: zone });

  const draw = (value: string, format: (date: Date) => string) => {
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? "—" : format(date);
  };
  const partOf = (formatter: Intl.DateTimeFormat, date: Date, type: Intl.DateTimeFormatPartTypes) =>
    formatter.formatToParts(date).find((part) => part.type === type)?.value ?? "";

  const clock: Clock = {
    zone,
    formatDate: (value) => draw(value, (date) => dateTime.format(date)),
    formatTime: (value) => draw(value, (date) => time.format(date)),
    formatStamp: (value) => draw(value, (date) =>
      `${partOf(stampDay, date, "month")}-${partOf(stampDay, date, "day")} ${time.format(date)}`),
    formatDayTime: (value) => draw(value, (date) => dayTime.format(date)),
    formatZone: (value) => draw(value, (date) => partOf(dateTime, date, "timeZoneName")),
    dayOf: (value) => draw(value, (date) => day.format(date)),
  };
  clocks.set(zone, clock);
  return clock;
}

export function shortBuild(build: string) {
  return build.startsWith("sha256:") ? `${build.slice(0, 15)}…` : build;
}

export function formatDuration(milliseconds?: number) {
  if (milliseconds === undefined || milliseconds < 0) return "—";
  if (milliseconds < 1) return `${Number(milliseconds.toFixed(3))}ms`;
  if (milliseconds < 1_000) return `${Math.round(milliseconds)}ms`;
  return `${(milliseconds / 1_000).toFixed(milliseconds < 10_000 ? 1 : 0)}s`;
}

export function formatElapsed(milliseconds?: number) {
  if (milliseconds === undefined || milliseconds < 0) return "—";
  const seconds = Math.floor(milliseconds / 1_000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

export function formatAgo(value?: string) {
  if (!value) return "—";
  const at = Date.parse(value);
  if (Number.isNaN(at)) return "—";
  const elapsed = Date.now() - at;
  if (elapsed < 0) return "just now";
  const seconds = Math.floor(elapsed / 1_000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function formatMemory(bytes?: number) {
  if (bytes === undefined || bytes < 0) return "—";
  const mib = bytes / (1024 * 1024);
  // Lowercase units match the console's voice ("vcpu", "mib") — uppercase
  // MiB was appearing in the same table row as lowercase mib.
  if (mib < 1024) return `${mib.toFixed(mib < 10 ? 1 : 0)} mib`;
  return `${(mib / 1024).toFixed(2)} gib`;
}

// Elastic sandboxes have no fixed ceiling worth reading against, so CPU is
// shown as vCPUs actually consumed rather than a share of the configured shape.
export function formatVCPU(value: number) {
  if (value === 0) return "0 vcpu";
  return `${value.toFixed(value < 0.1 ? 3 : value < 10 ? 2 : 1).replace(/\.?0+$/, "")} vcpu`;
}

export function initialsOf(email: string | null) {
  const local = (email ?? "").split("@")[0];
  const parts = local.split(/[^a-z0-9]+/i).filter(Boolean);
  const initials = parts.length >= 2 ? `${parts[0][0]}${parts[1][0]}` : local.slice(0, 2);
  return (initials || "??").toLowerCase();
}
