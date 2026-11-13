import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  bucketTransactions,
  formatCompact,
  shortBucketLabel,
  tickIndexes,
  toSats,
  type TxLike,
} from "./charts.ts";

// Local-time timestamp helper so the tests do not depend on the runner's
// time zone.
const at = (m: number, d: number, h: number): string =>
  new Date(2026, m - 1, d, h, 30).toISOString();

// A fixture shaped like /api/admin/transactions after normalization: sparse
// hourly activity with a long gap and nothing settled.
const sparse: TxLike[] = [
  { price_sats: 0, state: "pending", created_at: at(11, 11, 20) },
  { price_sats: 0, state: "pending", created_at: at(11, 11, 22) },
  { price_sats: 0, state: "pending", created_at: at(11, 11, 23) },
  { price_sats: 0, state: "pending", created_at: at(11, 12, 11) },
];

describe("formatCompact", () => {
  it("never emits scientific notation or NaN", () => {
    for (const v of [0, NaN, Infinity, -Infinity, -5, 1.1e39]) {
      const s = formatCompact(v);
      assert.doesNotMatch(s, /e\+|NaN|Infinity/);
    }
    assert.equal(formatCompact(0), "0");
    assert.equal(formatCompact(1.1e39), "1B+");
  });

  it("abbreviates normal values", () => {
    assert.equal(formatCompact(999), "999");
    assert.equal(formatCompact(1500), "1.5k");
    assert.equal(formatCompact(2_000_000), "2M");
  });
});

describe("toSats", () => {
  it("coerces int64 strings and rejects junk", () => {
    assert.equal(toSats("21"), 21);
    assert.equal(toSats("0"), 0);
    assert.equal(toSats(undefined), 0);
    assert.equal(toSats("abc"), 0);
  });
});

describe("bucketTransactions", () => {
  it("fills empty hours between first and last bucket", () => {
    const { buckets, hourly } = bucketTransactions(sparse);
    assert.equal(hourly, true);
    // 11/11 20:00 through 11/12 11:00 inclusive.
    assert.equal(buckets.length, 16);
    const hours = buckets.map((b) => b.time.getHours());
    assert.deepEqual(hours.slice(0, 5), [20, 21, 22, 23, 0]);
    assert.equal(buckets[1].pending + buckets[1].settled, 0);
    assert.equal(buckets[15].pending, 1);
  });

  it("keeps all-zero revenue at exactly zero", () => {
    const { buckets } = bucketTransactions(sparse);
    assert.ok(buckets.every((b) => b.sats === 0));
  });

  it("sums prices numerically", () => {
    const { buckets } = bucketTransactions([
      { price_sats: 21, state: "settled", created_at: at(11, 11, 20) },
      { price_sats: 21, state: "settled", created_at: at(11, 11, 20) },
    ]);
    assert.equal(buckets.length, 1);
    assert.equal(buckets[0].sats, 42);
  });

  it("counts only settled prices as revenue", () => {
    const { buckets } = bucketTransactions([
      { price_sats: 21, state: "pending", created_at: at(11, 11, 20) },
      { price_sats: 21, state: "settled", created_at: at(11, 11, 20) },
      { price_sats: 5, state: "pending", created_at: at(11, 11, 20) },
    ]);
    assert.equal(buckets.length, 1);
    assert.equal(buckets[0].sats, 21);
    // Volume still counts every row.
    assert.equal(buckets[0].settled + buckets[0].pending, 3);
  });

  it("yields a flat zero series when everything is pending", () => {
    const { buckets } = bucketTransactions([
      { price_sats: 21, state: "pending", created_at: at(11, 11, 20) },
      { price_sats: 21, state: "pending", created_at: at(11, 11, 23) },
    ]);
    assert.equal(buckets.length, 4);
    assert.ok(buckets.every((b) => b.sats === 0));
  });

  it("does not concatenate if a price arrives as a string", () => {
    const { buckets } = bucketTransactions([
      { price_sats: "21", state: "settled", created_at: at(11, 11, 20) },
      { price_sats: "21", state: "settled", created_at: at(11, 11, 20) },
    ] as unknown as TxLike[]);
    assert.equal(buckets[0].sats, 42);
  });

  it("caps daily buckets for long ranges", () => {
    const txs: TxLike[] = [
      { price_sats: 1, state: "settled", created_at: at(1, 1, 5) },
      { price_sats: 1, state: "settled", created_at: at(11, 1, 5) },
    ];
    const { buckets, hourly } = bucketTransactions(txs, 60);
    assert.equal(hourly, false);
    assert.ok(buckets.length <= 60);
    assert.equal(
      buckets.reduce((n, b) => n + b.settled, 0),
      2
    );
  });

  it("handles empty and invalid input", () => {
    assert.equal(bucketTransactions([]).buckets.length, 0);
    const bad: TxLike = { price_sats: 1, state: "settled", created_at: "x" };
    assert.equal(bucketTransactions([bad]).buckets.length, 0);
  });
});

describe("tick labels", () => {
  it("thins ticks so labels fit", () => {
    const idx = tickIndexes(16, 300, 50);
    assert.ok(idx.length <= 6);
    assert.equal(idx[0], 0);
    assert.deepEqual(tickIndexes(3, 300, 50), [0, 1, 2]);
  });

  it("shortens hourly labels", () => {
    const t = new Date(2026, 10, 11, 20);
    assert.equal(shortBucketLabel(t, true, true), "11/11 20:00");
    assert.equal(shortBucketLabel(t, true, false), "20:00");
    assert.equal(
      shortBucketLabel(new Date(2026, 10, 12, 0), true, false),
      "11/12 00:00"
    );
  });
});
