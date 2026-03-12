import useSWR, { mutate } from "swr";
import type {
  StatsResponse,
  Service,
  ServiceCreateRequest,
  Transaction,
  TransactionParams,
  InfoResponse,
} from "./types";

async function fetcher<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`API error: ${res.status}`);
  }
  return res.json();
}

export function useStats(from?: string, to?: string) {
  const params = new URLSearchParams();
  if (from) params.set("from", new Date(from + "T00:00:00").toISOString());
  if (to) params.set("to", new Date(to + "T23:59:59").toISOString());
  const qs = params.toString();
  const key = `/api/proxy/stats${qs ? `?${qs}` : ""}`;

  return useSWR<StatsResponse>(
    key,
    (path: string) =>
      fetcher<Record<string, unknown>>(path).then((r) => ({
        total_revenue_sats: Number(r.total_revenue_sats ?? 0),
        transaction_count: Number(r.transaction_count ?? 0),
        service_breakdown: (
          (r.service_breakdown as Array<Record<string, unknown>>) ?? []
        ).map((s) => ({
          service_name: String(s.service_name ?? ""),
          total_revenue_sats: Number(s.total_revenue_sats ?? 0),
        })),
      })),
    { refreshInterval: 10_000 }
  );
}

export function useInfo() {
  return useSWR<InfoResponse>("/api/proxy/info", fetcher, {
    refreshInterval: 60_000,
  });
}

export function useServices() {
  return useSWR<Service[]>(
    "/api/proxy/services",
    (path: string) =>
      fetcher<{ services: Array<Record<string, unknown>> }>(path).then((r) =>
        (r.services ?? []).map((s) => ({
          name: String(s.name ?? ""),
          address: String(s.address ?? ""),
          protocol: String(s.protocol ?? ""),
          host_regexp: String(s.host_regexp ?? ""),
          path_regexp: String(s.path_regexp ?? ""),
          price: Number(s.price ?? 0),
          auth: String(s.auth ?? ""),
        }))
      ),
    { refreshInterval: 30_000 }
  );
}

export function useTransactions(params: TransactionParams) {
  const sp = new URLSearchParams();
  if (params.limit) sp.set("limit", String(params.limit));
  if (params.offset) sp.set("offset", String(params.offset));
  if (params.service) sp.set("service", params.service);
  if (params.state) sp.set("state", params.state);
  if (params.from)
    sp.set("from", new Date(params.from + "T00:00:00").toISOString());
  if (params.to) sp.set("to", new Date(params.to + "T23:59:59").toISOString());
  const qs = sp.toString();
  const key = `/api/proxy/transactions${qs ? `?${qs}` : ""}`;

  return useSWR<Transaction[]>(
    key,
    (path: string) =>
      fetcher<{ transactions: Transaction[] }>(path).then(
        (r) => r.transactions ?? []
      ),
    { refreshInterval: 10_000 }
  );
}

const SERVICES_KEY = "/api/proxy/services";

export async function updateService(
  name: string,
  body: {
    address?: string;
    protocol?: string;
    hostregexp?: string;
    pathregexp?: string;
    price?: number;
    auth?: string;
  }
) {
  const res = await fetch(`/api/proxy/services/${encodeURIComponent(name)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: "request failed" }));
    throw new Error(err.error ?? `API error: ${res.status}`);
  }
  await mutate(SERVICES_KEY);
  return res.json();
}

export async function createService(body: ServiceCreateRequest) {
  const res = await fetch("/api/proxy/services", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: "request failed" }));
    throw new Error(err.error ?? `API error: ${res.status}`);
  }
  await mutate(SERVICES_KEY);
  return res.json();
}

export async function deleteService(name: string) {
  const res = await fetch(`/api/proxy/services/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: "request failed" }));
    throw new Error(err.error ?? `API error: ${res.status}`);
  }
  await mutate(SERVICES_KEY);
  return res.json();
}
