export interface StatsResponse {
  total_revenue_sats: number;
  transaction_count: number;
  service_breakdown: ServiceRevenueItem[];
}

export interface ServiceRevenueItem {
  service_name: string;
  total_revenue_sats: number;
}

export type AuthScheme =
  | "AUTH_SCHEME_L402"
  | "AUTH_SCHEME_MPP"
  | "AUTH_SCHEME_L402_MPP";

export const authSchemeLabels: Record<AuthScheme, string> = {
  AUTH_SCHEME_L402: "L402",
  AUTH_SCHEME_MPP: "MPP",
  AUTH_SCHEME_L402_MPP: "L402 + MPP",
};

export interface Service {
  name: string;
  address: string;
  protocol: string;
  host_regexp: string;
  path_regexp: string;
  price: number;
  auth: string;
  auth_scheme: AuthScheme;
}

export interface Transaction {
  id: number;
  token_id: string;
  payment_hash: string;
  service_name: string;
  price_sats: number;
  state: string;
  created_at: string;
  settled_at?: string;
}

export interface TransactionParams {
  limit?: number;
  offset?: number;
  service?: string;
  state?: string;
  from?: string;
  to?: string;
}

export interface ServiceCreateRequest {
  name: string;
  address: string;
  protocol?: string;
  hostregexp?: string;
  pathregexp?: string;
  price?: number;
  auth?: string;
  auth_scheme?: AuthScheme;
}

export interface InfoResponse {
  network: string;
  listen_addr: string;
  insecure: boolean;
  mpp_enabled: boolean;
  sessions_enabled: boolean;
  mpp_realm: string;
  /** Blockchain the connected lnd is on (e.g. "bitcoin", "sui"). May be
   *  empty if lnd was unreachable at prism startup. Drives the unit
   *  label shown in the UI (SUI vs sats). */
  chain?: string;
}

/**
 * MPP prepaid session snapshot returned from GET /api/admin/sessions.
 *
 * All *_sats fields are in the chain's base unit — satoshis for bitcoin,
 * MIST for sui. The UI layer pairs these with InfoResponse.chain to
 * decide display formatting (see lib/currency.ts).
 */
export interface MPPSession {
  session_id: string;
  payment_hash: string;
  deposit_sats: number;
  spent_sats: number;
  /** deposit_sats - spent_sats. Remaining prepaid balance on an open
   *  session; equal to what was refunded at close time on a closed one. */
  balance_sats: number;
  return_invoice: string;
  /** "open" or "closed". */
  status: string;
  created_at: string;
  updated_at: string;
}

export interface ListSessionsParams {
  /** "open" | "closed" | undefined (no filter). */
  status?: string;
  limit?: number;
  offset?: number;
}

export interface ListSessionsResponse {
  sessions: MPPSession[];
  /** Count matching status filter, ignoring pagination. */
  total: number;
}

export interface SessionStatsResponse {
  total_sessions: number;
  open_sessions: number;
  closed_sessions: number;
  /** Lifetime sum of deposits across all sessions. */
  total_deposit_sats: number;
  /** Actual revenue — satoshis/MIST consumed by bearer requests. */
  total_spent_sats: number;
  /** Prepaid balance still owed to clients on currently open sessions. */
  open_balance_sats: number;
}
