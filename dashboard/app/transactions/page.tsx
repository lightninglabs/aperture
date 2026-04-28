"use client";

import { Fragment, useState, useCallback, useMemo } from "react";
import { useRouter } from "next/navigation";
import { useTransactions, useInfo } from "@/lib/api";
import { formatAmount, unitLabel } from "@/lib/currency";
import styled from "@emotion/styled";
import type { TransactionParams } from "@/lib/types";
import ActivityChart from "@/components/ActivityChart";
import Button from "@/components/Button";
import PageHeader from "@/components/PageHeader";
import EmptyState from "@/components/EmptyState";
import ErrorBanner from "@/components/ErrorBanner";
import Tooltip from "@/components/Tooltip";
import SortHeader, { useSort } from "@/components/SortHeader";
import DateRangeFilter from "@/components/DateRangeFilter";
import OverflowMenu from "@/components/OverflowMenu";
import { toast } from "@/components/Toast";

const PAGE_SIZE = 20;

const Styled = {
  Card: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    overflow: hidden;
  `,
  CardPadded: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    padding: 16px 24px;
    margin-bottom: 16px;
  `,
  SectionTitle: styled.div`
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    margin-bottom: 12px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
  `,
  LoadingBox: styled.div`
    height: 80px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: ${(p) => p.theme.colors.gray};
    font-size: ${(p) => p.theme.sizes.xs}px;
  `,
  FilterBar: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    padding: 16px 24px;
    margin-bottom: 16px;
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 16px;
  `,
  FilterLabel: styled.label`
    display: block;
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  `,
  Select: styled.select`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 8px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    min-width: 120px;
  `,
  Input: styled.input`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 8px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 200px;
  `,
  ClearBtn: styled.button`
    background: transparent;
    border: none;
    color: ${(p) => p.theme.colors.gray};
    font-size: 13px;
    font-family: ${(p) => p.theme.fonts.open};
    cursor: pointer;
    padding: 8px 0;
    transition: color 0.15s;

    &:hover {
      color: ${(p) => p.theme.colors.offWhite};
    }
  `,
  Table: styled.table`
    width: 100%;
    border-collapse: collapse;
    font-family: ${(p) => p.theme.fonts.open};
    font-size: 14px;
  `,
  HeadRow: styled.tr`
    text-align: left;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
  `,
  Row: styled.tr`
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
    transition: background-color 0.15s;

    &:hover {
      background-color: ${(p) => p.theme.colors.overlay};
    }
  `,
  ClickableRow: styled.tr`
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
    transition: background-color 0.15s;
    cursor: pointer;

    &:hover {
      background-color: ${(p) => p.theme.colors.overlay};
    }
  `,
  DetailRow: styled.tr`
    background-color: ${(p) => p.theme.colors.overlay};
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
  `,
  DetailGrid: styled.div`
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    padding: 16px 24px;
  `,
  DetailLabel: styled.div`
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    text-transform: uppercase;
    letter-spacing: 0.5px;
    margin-bottom: 4px;
  `,
  DetailValue: styled.div`
    font-size: 13px;
    color: ${(p) => p.theme.colors.offWhite};
    word-break: break-all;
  `,
  DetailMono: styled.div`
    font-size: 13px;
    color: ${(p) => p.theme.colors.offWhite};
    font-family: monospace;
    word-break: break-all;
  `,
  SkeletonRow: styled.tr`
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
  `,
  Td: styled.td`
    padding: 14px 16px;
  `,
  IdCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.lightningGray};
    font-variant-numeric: tabular-nums;
  `,
  ServiceCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.white};
    font-weight: 600;
  `,
  AmountCell: styled.td`
    padding: 14px 16px;
    text-align: right;
    color: ${(p) => p.theme.colors.gold};
    font-variant-numeric: tabular-nums;
  `,
  HashCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.gray};
    font-size: 12px;
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: monospace;
  `,
  DateCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.lightningGray};
    font-size: 13px;
    white-space: nowrap;
  `,
  MenuCell: styled.td`
    padding: 14px 16px;
    text-align: right;
  `,
  Skeleton: styled.div<{ $width?: number }>`
    height: 14px;
    width: ${(p) => p.$width || 80}px;
    background-color: ${(p) => p.theme.colors.blue};
    border-radius: 4px;
  `,
  Pagination: styled.div`
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 16px;
    border-top: 1px solid ${(p) => p.theme.colors.lightBlue};
  `,
  PageBtn: styled.button`
    background: transparent;
    border: none;
    font-family: ${(p) => p.theme.fonts.open};
    font-size: 13px;
    padding: 4px 8px;
    cursor: pointer;
    color: ${(p) => p.theme.colors.offWhite};
    transition: color 0.15s ease;

    &:hover {
      color: ${(p) => p.theme.colors.white};
    }

    &:disabled {
      color: ${(p) => p.theme.colors.lightBlue};
      cursor: not-allowed;
      opacity: 0.4;
    }
  `,
  PageInfo: styled.span`
    color: ${(p) => p.theme.colors.gray};
    font-size: 12px;
  `,
  Badge: styled.span<{ $settled?: boolean }>`
    display: inline-block;
    padding: 2px 10px;
    border-radius: 100px;
    font-size: 12px;
    font-weight: 600;
    color: ${(p) =>
      p.$settled
        ? p.theme.colors.lightningGreen
        : p.theme.colors.lightningYellow};
    background-color: ${(p) =>
      p.$settled ? "rgba(16,185,129,0.1)" : "rgba(245,158,11,0.1)"};
  `,
};

export default function TransactionsPage() {
  const router = useRouter();
  const [stateFilter, setStateFilter] = useState("");
  const [serviceFilter, setServiceFilter] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [offset, setOffset] = useState(0);
  const [expandedId, setExpandedId] = useState<number | null>(null);

  const params: TransactionParams = {
    limit: PAGE_SIZE,
    offset,
    ...(stateFilter && { state: stateFilter }),
    ...(serviceFilter && { service: serviceFilter }),
    ...(dateFrom && { from: dateFrom }),
    ...(dateTo && { to: dateTo }),
  };

  const {
    data: transactions,
    isLoading,
    error,
    mutate,
  } = useTransactions(params);
  const { data: info } = useInfo();
  const chain = info?.chain;
  const { sorted, sortField, sortDir, onSort } = useSort(
    transactions,
    "id",
    "desc"
  );

  const page = Math.floor(offset / PAGE_SIZE) + 1;
  const hasNext = transactions?.length === PAGE_SIZE;
  const hasFilters = stateFilter || serviceFilter || dateFrom || dateTo;

  const applyFilters = useCallback(() => setOffset(0), []);

  const clearFilters = useCallback(() => {
    setStateFilter("");
    setServiceFilter("");
    setDateFrom("");
    setDateTo("");
    setOffset(0);
  }, []);

  const handleDateChange = useCallback(
    ({ from, to }: { from: string; to: string }) => {
      setDateFrom(from);
      setDateTo(to);
      setOffset(0);
    },
    []
  );

  const toggleExpanded = useCallback((id: number) => {
    setExpandedId((prev) => (prev === id ? null : id));
  }, []);

  const exportCSV = useCallback(() => {
    const rows = transactions ?? sorted ?? [];
    if (!rows.length) return;
    const headers = [
      "ID",
      "Service",
      `Amount (${unitLabel(chain)})`,
      "State",
      "Payment Hash",
      "Created",
    ];
    const csvRows = [
      headers.join(","),
      ...rows.map((tx) =>
        [
          tx.id,
          `"${tx.service_name.replace(/"/g, '""')}"`,
          tx.price_sats,
          `"${tx.state.replace(/"/g, '""')}"`,
          `"${tx.payment_hash.replace(/"/g, '""')}"`,
          `"${new Date(tx.created_at).toISOString()}"`,
        ].join(",")
      ),
    ];
    const blob = new Blob([csvRows.join("\n")], { type: "text/csv" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `transactions-${new Date().toISOString().slice(0, 10)}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  }, [transactions, sorted]);

  const handlePrev = useCallback(
    () => setOffset((o) => Math.max(0, o - PAGE_SIZE)),
    []
  );
  const handleNext = useCallback(() => setOffset((o) => o + PAGE_SIZE), []);

  const {
    Card,
    CardPadded,
    SectionTitle,
    LoadingBox,
    FilterBar,
    FilterLabel,
    Select,
    Input,
    ClearBtn,
    Table,
    HeadRow,
    Row,
    ClickableRow,
    DetailRow,
    DetailGrid,
    DetailLabel,
    DetailValue,
    DetailMono,
    SkeletonRow,
    Td,
    IdCell,
    ServiceCell,
    AmountCell,
    HashCell,
    DateCell,
    MenuCell,
    Skeleton,
    Pagination,
    PageBtn,
    PageInfo,
    Badge,
  } = Styled;

  return (
    <div>
      <PageHeader
        title="Transactions"
        description="View all Lightning payment activity across your services."
        action={
          <Button
            variant="ghost"
            compact
            onClick={exportCSV}
            disabled={isLoading || (!transactions?.length && !sorted?.length)}
          >
            Export CSV
          </Button>
        }
      />

      {error && (
        <ErrorBanner
          message="Failed to load transactions."
          onRetry={() => mutate()}
        />
      )}

      {/* Activity sparkline */}
      <CardPadded>
        <SectionTitle>Activity</SectionTitle>
        {transactions ? (
          <ActivityChart transactions={transactions} chain={chain} />
        ) : (
          <LoadingBox>Loading...</LoadingBox>
        )}
      </CardPadded>

      {/* Filters */}
      <FilterBar>
        <div>
          <FilterLabel>Date range</FilterLabel>
          <DateRangeFilter
            from={dateFrom}
            to={dateTo}
            onChange={handleDateChange}
          />
        </div>
        <div>
          <FilterLabel>State</FilterLabel>
          <Select
            value={stateFilter}
            onChange={(e) => setStateFilter(e.target.value)}
          >
            <option value="">All</option>
            <option value="pending">Pending</option>
            <option value="settled">Settled</option>
          </Select>
        </div>
        <div>
          <FilterLabel>Service</FilterLabel>
          <Input
            type="text"
            value={serviceFilter}
            onChange={(e) => setServiceFilter(e.target.value)}
            placeholder="Filter by service..."
          />
        </div>
        <Button variant="primary" compact onClick={applyFilters}>
          Apply
        </Button>
        {hasFilters && (
          <ClearBtn onClick={clearFilters}>Clear filters</ClearBtn>
        )}
      </FilterBar>

      {/* Table */}
      <Card>
        <div style={{ overflowX: "auto" }}>
          <Table>
            <thead>
              <HeadRow>
                <SortHeader
                  label="ID"
                  field="id"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Service"
                  field="service_name"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Amount"
                  field="price_sats"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                  align="right"
                  tooltip={
                    <Tooltip text="Amount in satoshis paid via Lightning invoice for this L402 token." />
                  }
                />
                <SortHeader
                  label="State"
                  field="state"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                  tooltip={
                    <Tooltip text="Pending = invoice created but unpaid. Settled = invoice paid, access granted." />
                  }
                />
                <SortHeader
                  label="Payment Hash"
                  field="payment_hash"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Created"
                  field="created_at"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <th style={{ padding: "10px 16px", width: 48 }} />
              </HeadRow>
            </thead>
            <tbody>
              {isLoading ? (
                Array.from({ length: 5 }).map((_, i) => (
                  <SkeletonRow key={i}>
                    {Array.from({ length: 7 }).map((_, j) => (
                      <Td key={j}>
                        <Skeleton $width={j === 6 ? 24 : 80} />
                      </Td>
                    ))}
                  </SkeletonRow>
                ))
              ) : sorted?.length ? (
                sorted.map((tx) => (
                  <Fragment key={tx.id}>
                    <ClickableRow onClick={() => toggleExpanded(tx.id)}>
                      <IdCell>{tx.id}</IdCell>
                      <ServiceCell>{tx.service_name}</ServiceCell>
                      <AmountCell>
                        {formatAmount(tx.price_sats, chain).value} {unitLabel(chain)}
                      </AmountCell>
                      <Td>
                        <Badge $settled={tx.state === "settled"}>
                          {tx.state}
                        </Badge>
                      </Td>
                      <HashCell>{tx.payment_hash}</HashCell>
                      <DateCell>
                        {new Date(tx.created_at).toLocaleString()}
                      </DateCell>
                      <MenuCell onClick={(e) => e.stopPropagation()}>
                        <OverflowMenu
                          items={[
                            {
                              label: "Copy payment hash",
                              onClick: () => {
                                navigator.clipboard.writeText(tx.payment_hash);
                                toast("Payment hash copied");
                              },
                            },
                            {
                              label: "Copy token ID",
                              onClick: () => {
                                navigator.clipboard.writeText(tx.token_id);
                                toast("Token ID copied");
                              },
                            },
                            {
                              label: "View service",
                              onClick: () => {
                                router.push(
                                  `/services/detail?name=${encodeURIComponent(tx.service_name)}`
                                );
                              },
                            },
                          ]}
                        />
                      </MenuCell>
                    </ClickableRow>
                    {expandedId === tx.id && (
                      <DetailRow>
                        <td colSpan={7}>
                          <DetailGrid>
                            <div>
                              <DetailLabel>Payment Hash</DetailLabel>
                              <DetailMono>{tx.payment_hash}</DetailMono>
                            </div>
                            <div>
                              <DetailLabel>Token ID</DetailLabel>
                              <DetailMono>{tx.token_id}</DetailMono>
                            </div>
                            <div>
                              <DetailLabel>Created At</DetailLabel>
                              <DetailValue>
                                {new Date(tx.created_at).toISOString()}
                              </DetailValue>
                            </div>
                            <div>
                              <DetailLabel>Settled At</DetailLabel>
                              <DetailValue>
                                {tx.settled_at
                                  ? new Date(tx.settled_at).toISOString()
                                  : "\u2014"}
                              </DetailValue>
                            </div>
                          </DetailGrid>
                        </td>
                      </DetailRow>
                    )}
                  </Fragment>
                ))
              ) : (
                <tr>
                  <td colSpan={7}>
                    <EmptyState
                      title="No transactions found"
                      description="Transactions will appear here once clients start paying for your services via L402."
                    />
                  </td>
                </tr>
              )}
            </tbody>
          </Table>
        </div>

        <Pagination>
          <PageBtn onClick={handlePrev} disabled={offset === 0}>
            &larr; Prev
          </PageBtn>
          <PageInfo>Page {page}</PageInfo>
          <PageBtn onClick={handleNext} disabled={!hasNext}>
            Next &rarr;
          </PageBtn>
        </Pagination>
      </Card>
    </div>
  );
}
