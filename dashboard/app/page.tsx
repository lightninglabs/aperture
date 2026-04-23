"use client";

import { useState, useMemo, useCallback } from "react";
import Link from "next/link";
import { useStats, useServices, useTransactions, useInfo } from "@/lib/api";
import { formatAmount, unitLabel } from "@/lib/currency";
import styled from "@emotion/styled";

const sfp = {
  shouldForwardProp: (prop: string) => !prop.startsWith("$"),
};
import ActivityChart from "@/components/ActivityChart";
import RevenueChart from "@/components/RevenueChart";
import VolumeChart from "@/components/VolumeChart";
import StateChart from "@/components/StateChart";
import StatTile from "@/components/StatTile";
import PageHeader from "@/components/PageHeader";
import DateRangeFilter from "@/components/DateRangeFilter";
import ErrorBanner from "@/components/ErrorBanner";

const Styled = {
  StatGrid: styled.div`
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 16px;
    margin-bottom: 24px;
  `,
  ChartRow: styled.div`
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    margin-bottom: 24px;
  `,
  BottomRow: styled.div`
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 16px;
    margin-bottom: 24px;
  `,
  Spacer: styled.div`
    height: 16px;
  `,
  Section: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    padding: 24px;
    animation: fade-in-up 0.4s ease-out both;
  `,
  SectionHeader: styled.div`
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 16px;
  `,
  SectionTitle: styled.div`
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    text-transform: uppercase;
    letter-spacing: 0.8px;
  `,
  ActionLink: styled(Link)`
    font-size: 13px;
    font-weight: 600;
    text-decoration: none;
    color: ${(p) => p.theme.colors.purple};
    transition: color 0.2s ease;

    &:hover {
      color: ${(p) => p.theme.colors.lightPurple};
    }
  `,
  ServiceList: styled.div`
    display: flex;
    flex-direction: column;
  `,
  ServiceRow: styled(Link, sfp)<{ $hasBorder?: boolean }>`
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0;
    text-decoration: none;
    transition: opacity 0.15s ease;
    border-bottom: ${(p) =>
      p.$hasBorder ? `1px solid ${p.theme.colors.blue}` : "none"};

    &:hover {
      opacity: 0.85;
    }
  `,
  ServiceName: styled.span`
    color: ${(p) => p.theme.colors.white};
    font-weight: 600;
    font-size: 14px;
  `,
  ProtoBadge: styled.span`
    font-size: 12px;
    padding: 2px 8px;
    border-radius: 4px;
    background-color: ${(p) => p.theme.colors.overlay};
    color: ${(p) => p.theme.colors.lightningGray};
  `,
  Address: styled.span`
    font-size: 12px;
    color: ${(p) => p.theme.colors.gray};
    font-family: monospace;
  `,
  Price: styled.span`
    color: ${(p) => p.theme.colors.gold};
    font-size: 14px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  `,
  Earned: styled.span`
    font-size: 12px;
    color: ${(p) => p.theme.colors.lightningGreen};
  `,
  Chevron: styled.span`
    color: ${(p) => p.theme.colors.gray};
    font-size: 14px;
  `,
  Placeholder: styled.div`
    flex: 1;
    display: flex;
    justify-content: center;
    align-items: center;
    font-size: ${(p) => p.theme.sizes.xs}px;
    color: ${(p) => p.theme.colors.gray};
    padding: 40px 0;
  `,
  EmptyLink: styled(Link)`
    color: ${(p) => p.theme.colors.purple};
    text-decoration: none;
  `,
  WelcomeCard: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border: 1px solid ${(p) => p.theme.colors.blue};
    border-radius: 12px;
    padding: 48px;
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    animation: fade-in-up 0.4s ease-out both;
  `,
  WelcomeTitle: styled.h2`
    color: ${(p) => p.theme.colors.white};
    font-size: 28px;
    font-weight: 700;
    margin: 0 0 12px;
  `,
  WelcomeDescription: styled.p`
    color: ${(p) => p.theme.colors.gray};
    font-size: 15px;
    line-height: 1.6;
    max-width: 480px;
    margin: 0 0 32px;
  `,
  WelcomeButton: styled(Link)`
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 12px 32px;
    background-color: ${(p) => p.theme.colors.purple};
    color: ${(p) => p.theme.colors.white};
    font-size: 15px;
    font-weight: 600;
    border-radius: 8px;
    text-decoration: none;
    transition: background-color 0.2s ease;

    &:hover {
      background-color: ${(p) => p.theme.colors.lightPurple};
    }
  `,
};

export default function DashboardPage() {
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");

  const { data: info } = useInfo();
  const chain = info?.chain;

  const {
    data: stats,
    isLoading: statsLoading,
    error: statsError,
    mutate: mutateStats,
  } = useStats(dateFrom || undefined, dateTo || undefined);
  const {
    data: services,
    isLoading: servicesLoading,
    error: servicesError,
    mutate: mutateServices,
  } = useServices();
  const {
    data: transactions,
    error: transactionsError,
    mutate: mutateTransactions,
  } = useTransactions({
    limit: 200,
    ...(dateFrom && { from: dateFrom }),
    ...(dateTo && { to: dateTo }),
  });

  const handleDateChange = useCallback(
    ({ from, to }: { from: string; to: string }) => {
      setDateFrom(from);
      setDateTo(to);
    },
    []
  );

  const handleRetry = useCallback(() => {
    mutateStats();
    mutateServices();
    mutateTransactions();
  }, [mutateStats, mutateServices, mutateTransactions]);

  const successRate = useMemo(() => {
    if (!transactions) return 0;
    const settled = transactions.filter((tx) => tx.state === "settled").length;
    const total = transactions.length;
    return total > 0 ? Math.round((settled / total) * 100) : 0;
  }, [transactions]);

  const isFirstRun =
    !servicesLoading && services?.length === 0 && !transactions?.length;

  const hasError = !!(statsError || servicesError || transactionsError);

  const {
    StatGrid,
    ChartRow,
    BottomRow,
    Spacer,
    Section,
    SectionHeader,
    SectionTitle,
    ActionLink,
    ServiceList,
    ServiceRow,
    ServiceName,
    ProtoBadge,
    Address,
    Price,
    Earned,
    Chevron,
    Placeholder,
    EmptyLink,
    WelcomeCard,
    WelcomeTitle,
    WelcomeDescription,
    WelcomeButton,
  } = Styled;

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Overview of your L402 payment activity."
        action={
          <DateRangeFilter
            from={dateFrom}
            to={dateTo}
            onChange={handleDateChange}
          />
        }
      />

      {hasError && (
        <ErrorBanner
          message={
            statsError?.message ||
            servicesError?.message ||
            transactionsError?.message ||
            "An error occurred while loading dashboard data."
          }
          onRetry={handleRetry}
        />
      )}

      {isFirstRun ? (
        <WelcomeCard>
          <WelcomeTitle>Welcome to Aperture</WelcomeTitle>
          <WelcomeDescription>
            You don&apos;t have any services or transactions yet. Set up your
            first L402 paywalled service to start accepting Lightning payments
            and monetizing your API endpoints.
          </WelcomeDescription>
          <WelcomeButton href="/services">Get Started</WelcomeButton>
        </WelcomeCard>
      ) : (
        <>
          <StatGrid className="animate-in">
            <StatTile
              title="Total Revenue"
              text={
                statsLoading
                  ? "..."
                  : stats
                    ? formatAmount(stats.total_revenue_sats, chain).value
                    : "\u2014"
              }
              suffix={unitLabel(chain)}
            />
            <StatTile
              title="Transactions"
              text={
                statsLoading
                  ? "..."
                  : stats
                    ? stats.transaction_count.toLocaleString()
                    : "\u2014"
              }
            />
            <StatTile
              title="Success Rate"
              text={transactions ? `${successRate}%` : "..."}
            />
            <StatTile
              title="Active Services"
              text={
                servicesLoading
                  ? "..."
                  : services
                    ? String(services.length)
                    : "\u2014"
              }
            />
          </StatGrid>

          <Section style={{ animationDelay: "200ms" }}>
            <SectionHeader>
              <SectionTitle>Revenue Over Time</SectionTitle>
            </SectionHeader>
            {transactions ? (
              <ActivityChart transactions={transactions} chain={chain} />
            ) : (
              <Placeholder>Loading...</Placeholder>
            )}
          </Section>

          <Spacer />

          <ChartRow>
            <Section style={{ animationDelay: "300ms" }}>
              <SectionHeader>
                <SectionTitle>Transaction Volume</SectionTitle>
              </SectionHeader>
              {transactions ? (
                <VolumeChart transactions={transactions} />
              ) : (
                <Placeholder>Loading...</Placeholder>
              )}
            </Section>

            <Section style={{ animationDelay: "350ms" }}>
              <SectionHeader>
                <SectionTitle>Revenue by Service</SectionTitle>
              </SectionHeader>
              {statsLoading ? (
                <Placeholder>Loading...</Placeholder>
              ) : (
                <RevenueChart data={stats?.service_breakdown ?? []} chain={chain} />
              )}
            </Section>
          </ChartRow>

          <BottomRow>
            <Section style={{ animationDelay: "400ms" }}>
              <SectionHeader>
                <SectionTitle>Transaction States</SectionTitle>
              </SectionHeader>
              {transactions ? (
                <StateChart transactions={transactions} />
              ) : (
                <Placeholder>Loading...</Placeholder>
              )}
            </Section>

            <Section style={{ animationDelay: "450ms" }}>
              <SectionHeader>
                <SectionTitle>Services</SectionTitle>
                <ActionLink href="/services">Manage &rarr;</ActionLink>
              </SectionHeader>
              {servicesLoading ? (
                <Placeholder>Loading...</Placeholder>
              ) : services?.length ? (
                <ServiceList>
                  {services.map((svc, i) => {
                    const rev =
                      stats?.service_breakdown?.find(
                        (s) => s.service_name === svc.name
                      )?.total_revenue_sats ?? 0;
                    return (
                      <ServiceRow
                        key={svc.name}
                        href={`/services/detail?name=${encodeURIComponent(svc.name)}`}
                        $hasBorder={i < services.length - 1}
                      >
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 12,
                          }}
                        >
                          <ServiceName>{svc.name}</ServiceName>
                          <ProtoBadge>{svc.protocol}</ProtoBadge>
                          <Address>{svc.address}</Address>
                        </div>
                        <div
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: 16,
                          }}
                        >
                          <Price>{formatAmount(svc.price, chain).value} {unitLabel(chain)}</Price>
                          {rev > 0 && (
                            <Earned>{rev.toLocaleString()} earned</Earned>
                          )}
                          <Chevron>&rsaquo;</Chevron>
                        </div>
                      </ServiceRow>
                    );
                  })}
                </ServiceList>
              ) : (
                <Placeholder>
                  No services configured.{" "}
                  <EmptyLink href="/services">Add one &rarr;</EmptyLink>
                </Placeholder>
              )}
            </Section>
          </BottomRow>
        </>
      )}
    </div>
  );
}
