export interface StatsResponse {
  total_revenue_sats: number;
  transaction_count: number;
  service_breakdown: ServiceRevenueItem[];
}

export interface ServiceRevenueItem {
  service_name: string;
  total_revenue_sats: number;
}

export interface Service {
  name: string;
  address: string;
  protocol: string;
  host_regexp: string;
  path_regexp: string;
  price: number;
  auth: string;
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
}

export interface InfoResponse {
  network: string;
  listen_addr: string;
  insecure: boolean;
}
