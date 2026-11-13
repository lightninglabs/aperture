// Pure data-shaping and formatting helpers for the dashboard charts. This
// module deliberately has no React or visx imports so it can be unit tested
// with the plain Node test runner (see charts.test.ts).

export interface TxLike {
  price_sats: number;
  state: string;
  created_at: string;
}

export interface TimeBucket {
  // Start of the bucket in local time.
  time: Date;
  // Sum of price_sats for every transaction in the bucket.
  sats: number;
  settled: number;
  pending: number;
}

export interface Bucketing {
  buckets: TimeBucket[];
  // Whether each bucket spans an hour (true) or one or more days (false).
  hourly: boolean;
}

const HOUR_MS = 3_600_000;

// toSats coerces an untrusted value to a finite, non-negative number. The
// admin API serializes int64 fields as JSON strings, so a raw value can be
// "0" or "21" rather than a number.
export function toSats(v: unknown): number {
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

// formatCompact renders a sat amount with a k or M suffix. Non-finite and
// negative inputs render as "0" so the axis can never show NaN, Infinity or
// scientific notation.
export function formatCompact(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0";
  if (value >= 1_000_000_000) return "1B+";
  if (value >= 1_000_000) {
    const v = value / 1_000_000;
    return v % 1 === 0 ? `${v}M` : `${v.toFixed(1)}M`;
  }
  if (value >= 1_000) {
    const v = value / 1_000;
    return v % 1 === 0 ? `${v}k` : `${v.toFixed(1)}k`;
  }
  return value % 1 === 0 ? String(value) : value.toFixed(1);
}

// bucketTransactions groups transactions into a continuous series of time
// buckets: hourly when the data spans under two days, daily otherwise. Empty
// buckets between the first and last transaction are included with zero
// counts. When the daily series would exceed maxBuckets, whole-day buckets
// are widened so the series fits.
export function bucketTransactions(
  transactions: TxLike[],
  maxBuckets = Infinity
): Bucketing {
  const points = transactions
    .map((tx) => ({ tx, t: new Date(tx.created_at) }))
    .filter(({ t }) => !Number.isNaN(t.getTime()))
    .sort((a, b) => a.t.getTime() - b.t.getTime());
  if (!points.length) return { buckets: [], hourly: true };

  const first = points[0].t;
  const last = points[points.length - 1].t;
  const hourly = last.getTime() - first.getTime() < 2 * 86_400_000;

  // Start of the local day or hour containing d.
  const floor = (d: Date): Date => {
    const f = new Date(d);
    if (hourly) f.setMinutes(0, 0, 0);
    else f.setHours(0, 0, 0, 0);
    return f;
  };

  const start = floor(first);
  const end = floor(last);
  const days = Math.round((end.getTime() - start.getTime()) / 86_400_000) + 1;
  const stepDays = hourly ? 1 : Math.max(1, Math.ceil(days / maxBuckets));

  // Index of the bucket a timestamp falls in.
  const indexOf = (d: Date): number =>
    hourly
      ? Math.floor((floor(d).getTime() - start.getTime()) / HOUR_MS)
      : Math.floor(
          Math.round((floor(d).getTime() - start.getTime()) / 86_400_000) /
            stepDays
        );

  const count = indexOf(last) + 1;
  const buckets: TimeBucket[] = [];
  for (let i = 0; i < count; i++) {
    const time = hourly
      ? new Date(start.getTime() + i * HOUR_MS)
      : new Date(
          start.getFullYear(),
          start.getMonth(),
          start.getDate() + i * stepDays
        );
    buckets.push({ time, sats: 0, settled: 0, pending: 0 });
  }

  for (const { tx, t } of points) {
    const b = buckets[indexOf(t)];
    b.sats += toSats(tx.price_sats);
    if (tx.state === "settled") b.settled++;
    else b.pending++;
  }

  return { buckets, hourly };
}

const pad2 = (n: number): string => String(n).padStart(2, "0");

// fullBucketLabel is the unabbreviated label used in tooltips.
export function fullBucketLabel(time: Date, hourly: boolean): string {
  const date = `${time.getMonth() + 1}/${time.getDate()}`;
  return hourly ? `${date} ${pad2(time.getHours())}:00` : date;
}

// shortBucketLabel is the compact x-axis tick label. Hourly ticks show only
// the hour, with the date added on the first tick and at each midnight.
export function shortBucketLabel(
  time: Date,
  hourly: boolean,
  isFirst: boolean
): string {
  if (!hourly) return fullBucketLabel(time, false);
  if (isFirst || time.getHours() === 0) return fullBucketLabel(time, true);
  return `${pad2(time.getHours())}:00`;
}

// tickIndexes picks evenly spaced bucket indexes so that labels of
// labelWidth pixels do not overlap on an axis that is axisWidth pixels wide.
export function tickIndexes(
  count: number,
  axisWidth: number,
  labelWidth: number
): number[] {
  if (count <= 0) return [];
  const fit = Math.max(1, Math.floor(axisWidth / labelWidth));
  const step = Math.max(1, Math.ceil(count / fit));
  const out: number[] = [];
  for (let i = 0; i < count; i += step) out.push(i);
  return out;
}
