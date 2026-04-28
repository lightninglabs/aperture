/**
 * Chain-aware currency formatting.
 *
 * Prism stores all amounts in lnd's native base unit (an int64). On
 * bitcoin-backed lnd that unit is the satoshi (1 BTC = 10^8 sats). On
 * the Sui-adapted lnd, the adapter maps `btcutil.Amount` (int64) to
 * MIST (1 SUI = 10^9 MIST). So the same numeric column in the admin
 * API means different currencies depending on the `chain` field from
 * admin GetInfo.
 *
 * Display policy:
 *   - bitcoin: show the raw integer + "sats" (native unit is already
 *     small enough for readable micropayments).
 *   - sui: convert MIST → SUI with up to 9 decimals, strip trailing
 *     zeros, and render as "<n> SUI" (MIST is too small for a human
 *     label, SUI reads naturally since SUI is not a high-value coin).
 *   - unknown / empty chain: fall back to "sats" (bitcoin default).
 */

export type ChainKind = "bitcoin" | "sui" | "unknown";

const MIST_PER_SUI = 1_000_000_000;

/** Normalize the raw chain string from admin GetInfo. */
export function chainKind(chain?: string | null): ChainKind {
  switch ((chain || "").toLowerCase()) {
    case "sui":
      return "sui";
    case "bitcoin":
    case "btc":
      return "bitcoin";
    default:
      return "unknown";
  }
}

/** Short unit label for DISPLAY contexts (already scaled by formatAmount):
 *  - bitcoin → "sats"
 *  - sui     → "SUI"   (display is in SUI, not MIST)
 */
export function unitLabel(chain?: string | null): string {
  return chainKind(chain) === "sui" ? "SUI" : "sats";
}

/** Base-unit label for FORM inputs, where the user types a raw integer:
 *  - bitcoin → "sats"   (1 sat = 10^-8 BTC; the raw stored integer)
 *  - sui     → "MIST"   (1 MIST = 10^-9 SUI; the raw stored integer)
 *  Keep the form honest — show what the number actually represents, so
 *  there's no hidden 10^9 factor. Display contexts elsewhere scale into
 *  SUI for readability.
 */
export function baseUnitLabel(chain?: string | null): string {
  return chainKind(chain) === "sui" ? "MIST" : "sats";
}

/**
 * Format a raw base-unit amount for display. Accepts number or string
 * (admin API returns sats/mist as strings because the proto type is
 * int64). Returns value + unit split so the caller can style them
 * independently (e.g. grey-out the unit suffix in a KPI card).
 */
export function formatAmount(
  amount: number | string | null | undefined,
  chain?: string | null,
): { value: string; unit: string } {
  const raw = toNumber(amount);
  const unit = unitLabel(chain);

  if (chainKind(chain) !== "sui") {
    return { value: formatInt(raw), unit };
  }

  // MIST → SUI, up to 9 decimals, trimmed.
  const sui = raw / MIST_PER_SUI;
  return { value: formatSui(sui), unit };
}

/** Convenience one-liner: "100 sats" or "0.000001 SUI". */
export function formatAmountString(
  amount: number | string | null | undefined,
  chain?: string | null,
): string {
  const { value, unit } = formatAmount(amount, chain);
  return `${value} ${unit}`;
}

function toNumber(v: number | string | null | undefined): number {
  if (typeof v === "number") return Number.isFinite(v) ? v : 0;
  if (typeof v === "string") {
    const n = Number(v);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function formatInt(n: number): string {
  return Math.round(n).toLocaleString("en-US");
}

function formatSui(n: number): string {
  if (n === 0) return "0";
  // Up to 9 decimals, trim trailing zeros. Avoid scientific notation
  // for very small amounts by using toFixed first.
  const fixed = n.toFixed(9);
  const trimmed = fixed.replace(/\.?0+$/, "");
  return trimmed === "" || trimmed === "-" ? "0" : trimmed;
}
