"use client";

import { useState, useMemo } from "react";
import styled from "@emotion/styled";
import { useSessions, useSessionStats, useInfo } from "@/lib/api";
import { formatAmount, unitLabel } from "@/lib/currency";
import PageHeader from "@/components/PageHeader";
import StatTile from "@/components/StatTile";
import EmptyState from "@/components/EmptyState";
import ErrorBanner from "@/components/ErrorBanner";

const PAGE_SIZE = 20;

const sfp = {
  shouldForwardProp: (prop: string) => !prop.startsWith("$"),
};

const Styled = {
  StatGrid: styled.div`
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 16px;
    margin-bottom: 24px;
  `,
  Filters: styled.div`
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 16px;
    font-size: 13px;
    color: ${(p) => p.theme.colors.white};
  `,
  FilterBtn: styled.button<{ $active?: boolean }>`
    background: ${(p) =>
      p.$active
        ? "rgba(93, 95, 239, 0.18)"
        : "rgba(245, 245, 245, 0.04)"};
    border: 1px solid
      ${(p) => (p.$active ? p.theme.colors.purple : "transparent")};
    color: ${(p) => (p.$active ? p.theme.colors.white : "#848a99")};
    padding: 6px 12px;
    border-radius: 4px;
    font-size: 13px;
    cursor: pointer;
    transition: all 0.15s ease;
    &:hover {
      color: ${(p) => p.theme.colors.white};
    }
  `,
  TableWrap: styled.div`
    background-color: #1d253a;
    border-radius: 8px;
    overflow: hidden;
  `,
  Table: styled.table`
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  `,
  Th: styled.th`
    padding: 12px 16px;
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: #848a99;
    border-bottom: 1px solid #252f4a;
  `,
  Td: styled.td`
    padding: 12px 16px;
    border-bottom: 1px solid #252f4a;
    color: ${(p) => p.theme.colors.offWhite};
  `,
  MonoCell: styled.td`
    padding: 12px 16px;
    border-bottom: 1px solid #252f4a;
    color: #848a99;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
  `,
  StatusBadge: styled.span<{ $open?: boolean }>`
    display: inline-block;
    padding: 2px 8px;
    border-radius: 10px;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.3px;
    color: ${(p) => (p.$open ? "#4ade80" : "#848a99")};
    background: ${(p) =>
      p.$open ? "rgba(74, 222, 128, 0.12)" : "rgba(132, 138, 153, 0.15)"};
  `,
  Pagination: styled.div`
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-top: 16px;
    color: #848a99;
    font-size: 13px;
  `,
  PageBtn: styled.button`
    background: rgba(245, 245, 245, 0.04);
    color: ${(p) => p.theme.colors.white};
    border: none;
    padding: 6px 14px;
    border-radius: 4px;
    font-size: 13px;
    cursor: pointer;
    &:disabled {
      opacity: 0.35;
      cursor: default;
    }
    &:not(:disabled):hover {
      background: rgba(245, 245, 245, 0.08);
    }
  `,
  Hint: styled.p`
    color: #848a99;
    font-size: 13px;
    margin: 24px 0;
    line-height: 1.6;
  `,
};

export default function SessionsPage() {
  const [status, setStatus] = useState<string>("");
  const [offset, setOffset] = useState(0);

  const { data: info } = useInfo();
  const chain = info?.chain;

  const {
    data: sessions,
    isLoading,
    error,
  } = useSessions({ status: status || undefined, limit: PAGE_SIZE, offset });
  const { data: stats } = useSessionStats();

  // Server responds 501 when sessions are disabled; the hook maps that
  // to `null`. Show a clear explanation rather than a broken UI.
  if (sessions === null || stats === null) {
    return (
      <>
        <PageHeader
          title="Sessions"
          description="MPP prepaid sessions are not enabled on this server."
        />
        <Styled.Hint>
          To enable sessions, set{" "}
          <code>authenticator.enablempp: true</code> and{" "}
          <code>authenticator.enablesessions: true</code> in your prism
          config and restart.
        </Styled.Hint>
      </>
    );
  }

  const total = sessions?.total ?? 0;
  const rows = sessions?.sessions ?? [];

  const prevDisabled = offset === 0;
  const nextDisabled = offset + PAGE_SIZE >= total;

  const statCards = useMemo(
    () => [
      {
        label: "Open Sessions",
        value: stats ? String(stats.open_sessions) : "\u2014",
      },
      {
        label: "Total Sessions",
        value: stats ? String(stats.total_sessions) : "\u2014",
      },
      {
        label: "Revenue (spent)",
        value: stats
          ? formatAmount(stats.total_spent_sats, chain).value
          : "\u2014",
        suffix: unitLabel(chain),
      },
      {
        label: "Open Balance (owed)",
        value: stats
          ? formatAmount(stats.open_balance_sats, chain).value
          : "\u2014",
        suffix: unitLabel(chain),
      },
    ],
    [stats, chain]
  );

  return (
    <>
      <PageHeader
        title="Sessions"
        description="MPP prepaid sessions — each client deposits upfront, requests debit balance, refund on close."
      />

      <Styled.StatGrid>
        {statCards.map((s) => (
          <StatTile
            key={s.label}
            title={s.label}
            text={s.value}
            suffix={s.suffix}
          />
        ))}
      </Styled.StatGrid>

      {error ? <ErrorBanner message={String(error)} /> : null}

      <Styled.Filters>
        <span>Filter:</span>
        <Styled.FilterBtn
          {...sfp}
          $active={status === ""}
          onClick={() => {
            setStatus("");
            setOffset(0);
          }}
        >
          All
        </Styled.FilterBtn>
        <Styled.FilterBtn
          {...sfp}
          $active={status === "open"}
          onClick={() => {
            setStatus("open");
            setOffset(0);
          }}
        >
          Open
        </Styled.FilterBtn>
        <Styled.FilterBtn
          {...sfp}
          $active={status === "closed"}
          onClick={() => {
            setStatus("closed");
            setOffset(0);
          }}
        >
          Closed
        </Styled.FilterBtn>
      </Styled.Filters>

      {isLoading ? (
        <EmptyState title="Loading…" description="Fetching sessions…" />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No sessions"
          description={
            status
              ? `No ${status} sessions recorded yet.`
              : "No sessions recorded yet. Once a client pays a deposit invoice and opens a session, it'll show up here."
          }
        />
      ) : (
        <>
          <Styled.TableWrap>
            <Styled.Table>
              <thead>
                <tr>
                  <Styled.Th>Session ID</Styled.Th>
                  <Styled.Th>Status</Styled.Th>
                  <Styled.Th>Deposit</Styled.Th>
                  <Styled.Th>Spent</Styled.Th>
                  <Styled.Th>Balance</Styled.Th>
                  <Styled.Th>Created</Styled.Th>
                </tr>
              </thead>
              <tbody>
                {rows.map((s) => (
                  <tr key={s.session_id}>
                    <Styled.MonoCell title={s.session_id}>
                      {s.session_id.slice(0, 16)}…
                    </Styled.MonoCell>
                    <Styled.Td>
                      <Styled.StatusBadge $open={s.status === "open"}>
                        {s.status}
                      </Styled.StatusBadge>
                    </Styled.Td>
                    <Styled.Td>
                      {formatAmount(s.deposit_sats, chain).value}{" "}
                      {unitLabel(chain)}
                    </Styled.Td>
                    <Styled.Td>
                      {formatAmount(s.spent_sats, chain).value}{" "}
                      {unitLabel(chain)}
                    </Styled.Td>
                    <Styled.Td>
                      {formatAmount(s.balance_sats, chain).value}{" "}
                      {unitLabel(chain)}
                    </Styled.Td>
                    <Styled.Td>
                      {new Date(s.created_at).toLocaleString()}
                    </Styled.Td>
                  </tr>
                ))}
              </tbody>
            </Styled.Table>
          </Styled.TableWrap>

          <Styled.Pagination>
            <span>
              Showing {offset + 1}–
              {Math.min(offset + PAGE_SIZE, total)} of {total}
            </span>
            <div style={{ display: "flex", gap: 8 }}>
              <Styled.PageBtn
                disabled={prevDisabled}
                onClick={() =>
                  setOffset(Math.max(0, offset - PAGE_SIZE))
                }
              >
                Prev
              </Styled.PageBtn>
              <Styled.PageBtn
                disabled={nextDisabled}
                onClick={() => setOffset(offset + PAGE_SIZE)}
              >
                Next
              </Styled.PageBtn>
            </div>
          </Styled.Pagination>
        </>
      )}
    </>
  );
}
