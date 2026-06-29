"use client";

import { useState, useCallback, useMemo, Suspense } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import Link from "next/link";
import {
  useServices,
  useTransactions,
  useStats,
  useInfo,
  updateService,
  deleteService,
} from "@/lib/api";
import styled from "@emotion/styled";
import { toast } from "@/components/Toast";
import type { AuthScheme } from "@/lib/types";
import { authSchemeLabels } from "@/lib/types";
import { formatAmount, unitLabel, baseUnitLabel } from "@/lib/currency";
import Button from "@/components/Button";
import StatTile from "@/components/StatTile";
import EmptyState from "@/components/EmptyState";
import ErrorBanner from "@/components/ErrorBanner";

const authOptions = ["on", "off", "freebie 1", "freebie 5", "freebie 10"];
const authSchemeOptions: AuthScheme[] = [
  "AUTH_SCHEME_L402",
  "AUTH_SCHEME_MPP",
  "AUTH_SCHEME_L402_MPP",
];

const Styled = {
  Breadcrumb: styled(Link)`
    color: ${(p) => p.theme.colors.gray};
    text-decoration: none;
    font-size: 13px;
    transition: color 0.15s;

    &:hover {
      color: ${(p) => p.theme.colors.offWhite};
    }
  `,
  Header: styled.div`
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    margin-bottom: 24px;
  `,
  Title: styled.h1`
    font-family: ${(p) => p.theme.fonts.work};
    font-size: ${(p) => p.theme.sizes.l}px;
    font-weight: 300;
    margin: 0;
  `,
  Subtitle: styled.p`
    color: ${(p) => p.theme.colors.gray};
    font-size: 13px;
    margin: 4px 0 0;
    font-family: monospace;
  `,
  StatGrid: styled.div`
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 16px;
    margin-bottom: 24px;
  `,
  ConfigGrid: styled.div`
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    margin-bottom: 24px;
  `,
  Card: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    padding: 24px;
  `,
  CardNopad: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    overflow: hidden;
  `,
  SectionTitle: styled.div`
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    text-transform: uppercase;
    letter-spacing: 0.8px;
    margin-bottom: 16px;
  `,
  ConfigRow: styled.div`
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 0;
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
  `,
  ConfigLabel: styled.span`
    font-size: 13px;
    color: ${(p) => p.theme.colors.gray};
  `,
  ConfigValue: styled.span<{ $mono?: boolean }>`
    font-size: 14px;
    color: ${(p) => p.theme.colors.offWhite};
    font-family: ${(p) => (p.$mono ? "monospace" : p.theme.fonts.open)};
  `,
  EditablePrice: styled.span`
    cursor: pointer;
    color: ${(p) => p.theme.colors.gold};
    border-bottom: 1px dashed ${(p) => p.theme.colors.blue};
    transition: border-color 0.15s;

    &:hover {
      border-color: ${(p) => p.theme.colors.gold};
    }
  `,
  EditableField: styled.span`
    cursor: pointer;
    color: ${(p) => p.theme.colors.offWhite};
    border-bottom: 1px dashed ${(p) => p.theme.colors.blue};
    transition: border-color 0.15s;

    &:hover {
      border-color: ${(p) => p.theme.colors.offWhite};
    }
  `,
  PriceInput: styled.input`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.gold};
    color: ${(p) => p.theme.colors.gold};
    padding: 4px 8px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 80px;
    text-align: right;
  `,
  FieldInput: styled.input`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.offWhite};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 4px 8px;
    font-size: 14px;
    font-family: monospace;
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 100%;
    text-align: left;
  `,
  AuthSelect: styled.select`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 4px 8px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    min-width: 100px;
  `,
  CodeBlock: styled.div`
    background-color: ${(p) => p.theme.colors.overlay};
    border-radius: 6px;
    padding: 16px 20px;
    font-family: monospace;
    font-size: 13px;
    color: ${(p) => p.theme.colors.lightningGray};
    line-height: 1.6;
    overflow-x: auto;
    white-space: pre;
  `,
  HelpText: styled.p`
    font-size: 12px;
    color: ${(p) => p.theme.colors.gray};
    margin-top: 12px;
    line-height: 1.5;
  `,
  Code: styled.code`
    background-color: ${(p) => p.theme.colors.overlay};
    padding: 1px 4px;
    border-radius: 3px;
    font-size: 12px;
  `,
  TableHeader: styled.div`
    padding: 16px 24px;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    display: flex;
    align-items: center;
    justify-content: space-between;
  `,
  ActionLink: styled(Link)`
    font-size: 13px;
    color: ${(p) => p.theme.colors.purple};
    text-decoration: none;
    font-weight: 600;
    transition: color 0.2s ease;

    &:hover {
      color: ${(p) => p.theme.colors.lightPurple};
    }
  `,
  Table: styled.table`
    width: 100%;
    border-collapse: collapse;
    font-family: ${(p) => p.theme.fonts.open};
    font-size: 14px;
  `,
  Th: styled.th`
    padding: 10px 16px;
    font-weight: 600;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: ${(p) => p.theme.colors.gray};
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
  Td: styled.td`
    padding: 14px 16px;
  `,
  IdCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.lightningGray};
    font-variant-numeric: tabular-nums;
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
  Loading: styled.div`
    padding: 60px 0;
    text-align: center;
    color: ${(p) => p.theme.colors.gray};
  `,
  BackLink: styled(Link)`
    color: ${(p) => p.theme.colors.purple};
    text-decoration: none;
    font-weight: 600;
  `,
};

function ServiceDetailContent() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const decodedName = decodeURIComponent(searchParams.get("name") ?? "");
  const {
    data: services,
    isLoading,
    error: servicesError,
    mutate: mutateServices,
  } = useServices();
  const {
    data: transactions,
    error: transactionsError,
    mutate: mutateTransactions,
  } = useTransactions({
    service: decodedName,
    limit: 50,
  });
  const { data: info, error: infoError, mutate: mutateInfo } = useInfo();
  const chain = info?.chain;
  const { data: stats } = useStats();

  const [editingPrice, setEditingPrice] = useState(false);
  const [priceValue, setPriceValue] = useState("");
  const [editingAddress, setEditingAddress] = useState(false);
  const [addressValue, setAddressValue] = useState("");
  const [editingProtocol, setEditingProtocol] = useState(false);
  const [protocolValue, setProtocolValue] = useState("");
  const [editingHostregexp, setEditingHostregexp] = useState(false);
  const [hostregexpValue, setHostregexpValue] = useState("");
  const [editingPathregexp, setEditingPathregexp] = useState(false);
  const [pathregexpValue, setPathregexpValue] = useState("");
  const [saving, setSaving] = useState(false);

  const svc = services?.find((s) => s.name === decodedName);

  // Use the stats endpoint for accurate total revenue instead of
  // summing from the limited transaction list.
  const totalRevenue = useMemo(
    () =>
      stats?.service_breakdown?.find((s) => s.service_name === decodedName)
        ?.total_revenue_sats ?? 0,
    [stats, decodedName]
  );

  const settledCount = useMemo(
    () => transactions?.filter((tx) => tx.state === "settled").length ?? 0,
    [transactions]
  );

  const handlePriceSave = useCallback(async () => {
    const price = parseInt(priceValue, 10);
    if (isNaN(price) || price < 0) return;
    setSaving(true);
    try {
      await updateService(decodedName, { price });
      toast(`Price updated to ${formatAmount(price, chain).value} ${unitLabel(chain)}`);
    } catch (e: unknown) {
      toast(e instanceof Error ? e.message : "Failed to update price", "error");
    }
    setSaving(false);
    setEditingPrice(false);
  }, [decodedName, priceValue]);

  const handleAddressSave = useCallback(async () => {
    const trimmed = addressValue.trim();
    if (!trimmed) return;
    setSaving(true);
    try {
      await updateService(decodedName, { address: trimmed });
      toast(`Address updated to "${trimmed}"`);
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to update address",
        "error"
      );
    }
    setSaving(false);
    setEditingAddress(false);
  }, [decodedName, addressValue]);

  const handleProtocolSave = useCallback(async () => {
    const trimmed = protocolValue.trim();
    if (!trimmed) return;
    setSaving(true);
    try {
      await updateService(decodedName, { protocol: trimmed });
      toast(`Protocol updated to "${trimmed}"`);
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to update protocol",
        "error"
      );
    }
    setSaving(false);
    setEditingProtocol(false);
  }, [decodedName, protocolValue]);

  const handleHostregexpSave = useCallback(async () => {
    const trimmed = hostregexpValue.trim();
    setSaving(true);
    try {
      await updateService(decodedName, { host_regexp: trimmed });
      toast(
        trimmed ? `Host regexp updated to "${trimmed}"` : "Host regexp cleared"
      );
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to update host regexp",
        "error"
      );
    }
    setSaving(false);
    setEditingHostregexp(false);
  }, [decodedName, hostregexpValue]);

  const handlePathregexpSave = useCallback(async () => {
    const trimmed = pathregexpValue.trim();
    setSaving(true);
    try {
      await updateService(decodedName, { path_regexp: trimmed });
      toast(
        trimmed ? `Path regexp updated to "${trimmed}"` : "Path regexp cleared"
      );
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to update path regexp",
        "error"
      );
    }
    setSaving(false);
    setEditingPathregexp(false);
  }, [decodedName, pathregexpValue]);

  const handleAuthChange = useCallback(
    async (auth: string) => {
      setSaving(true);
      try {
        await updateService(decodedName, { auth });
        toast(`Auth updated to "${auth}"`);
      } catch (e: unknown) {
        toast(
          e instanceof Error ? e.message : "Failed to update auth",
          "error"
        );
      }
      setSaving(false);
    },
    [decodedName]
  );

  const handleAuthSchemeChange = useCallback(
    async (scheme: AuthScheme) => {
      setSaving(true);
      try {
        await updateService(decodedName, { auth_scheme: scheme });
        toast(`Auth scheme updated to "${authSchemeLabels[scheme]}"`);
      } catch (e: unknown) {
        toast(
          e instanceof Error ? e.message : "Failed to update auth scheme",
          "error"
        );
      }
      setSaving(false);
    },
    [decodedName]
  );

  const handleDelete = useCallback(async () => {
    if (!confirm(`Delete service "${decodedName}"? This cannot be undone.`))
      return;
    setSaving(true);
    try {
      await deleteService(decodedName);
      toast(`Service "${decodedName}" deleted`);
      router.push("/services");
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to delete service",
        "error"
      );
      setSaving(false);
    }
  }, [decodedName]);

  const handleClearPayment = useCallback(async () => {
    if (
      !confirm(
        "Remove the per-service lnd override? This service will fall " +
          "back to the gateway's global lnd on the next prism restart."
      )
    ) {
      return;
    }
    setSaving(true);
    try {
      await updateService(decodedName, { clear_payment: true });
      toast("Payment override cleared. Restart prism to apply.");
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to clear payment",
        "error"
      );
    }
    setSaving(false);
  }, [decodedName]);

  const startPriceEdit = useCallback(() => {
    if (!svc) return;
    setEditingPrice(true);
    setPriceValue(String(svc.price));
  }, [svc]);

  const startAddressEdit = useCallback(() => {
    if (!svc) return;
    setEditingAddress(true);
    setAddressValue(svc.address);
  }, [svc]);

  const startProtocolEdit = useCallback(() => {
    if (!svc) return;
    setEditingProtocol(true);
    setProtocolValue(svc.protocol);
  }, [svc]);

  const startHostregexpEdit = useCallback(() => {
    setEditingHostregexp(true);
    setHostregexpValue(svc?.host_regexp ?? "");
  }, [svc]);

  const startPathregexpEdit = useCallback(() => {
    setEditingPathregexp(true);
    setPathregexpValue(svc?.path_regexp ?? "");
  }, [svc]);

  const testEndpoint = useMemo(() => {
    if (!info) return null;
    const scheme = info.insecure ? "http" : "https";
    const curlFlag = info.insecure ? "" : " -k";
    return `curl${curlFlag} ${scheme}://${info.listen_addr}/`;
  }, [info]);

  if (isLoading) {
    return <Styled.Loading>Loading...</Styled.Loading>;
  }

  if (!svc) {
    return (
      <div style={{ paddingTop: 40 }}>
        <EmptyState
          title="Service not found"
          description={`No service named "${decodedName}" exists.`}
          action={
            <Styled.BackLink href="/services">
              &larr; Back to Services
            </Styled.BackLink>
          }
        />
      </div>
    );
  }

  const hostregexp = svc.host_regexp || "";
  const pathregexp = svc.path_regexp || "";

  const {
    Breadcrumb,
    Header,
    Title,
    Subtitle,
    StatGrid,
    ConfigGrid,
    Card,
    CardNopad,
    SectionTitle,
    ConfigRow,
    ConfigLabel,
    ConfigValue,
    EditablePrice,
    EditableField,
    PriceInput,
    FieldInput,
    AuthSelect,
    CodeBlock,
    HelpText,
    Code,
    TableHeader,
    ActionLink,
    Table,
    Th,
    HeadRow,
    Row,
    Td,
    IdCell,
    AmountCell,
    HashCell,
    DateCell,
    Badge,
  } = Styled;

  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <Breadcrumb href="/services">&larr; Services</Breadcrumb>
      </div>

      {servicesError && (
        <ErrorBanner
          message="Failed to load services."
          onRetry={() => mutateServices()}
        />
      )}
      {transactionsError && (
        <ErrorBanner
          message="Failed to load transactions."
          onRetry={() => mutateTransactions()}
        />
      )}
      {infoError && (
        <ErrorBanner
          message="Failed to load server info."
          onRetry={() => mutateInfo()}
        />
      )}

      <Header>
        <div>
          <Title>{svc.name}</Title>
          <Subtitle>
            {svc.protocol}://{svc.address}
          </Subtitle>
        </div>
        <Button
          variant="ghost"
          compact
          disabled={saving}
          onClick={handleDelete}
          style={{
            color: "#EF4444",
            borderColor: "#EF4444",
            fontSize: 13,
          }}
        >
          Delete Service
        </Button>
      </Header>

      <StatGrid>
        <StatTile
          title="Revenue"
          text={formatAmount(totalRevenue, chain).value}
          suffix={unitLabel(chain)}
        />
        <StatTile title="Settled Payments" text={String(settledCount)} />
        <StatTile
          title="Price"
          text={formatAmount(svc.price, chain).value}
          suffix={unitLabel(chain)}
        />
      </StatGrid>

      <ConfigGrid>
        <Card>
          <SectionTitle>Configuration</SectionTitle>
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <ConfigRow>
              <ConfigLabel>Protocol</ConfigLabel>
              <ConfigValue>
                {editingProtocol ? (
                  <FieldInput
                    autoFocus
                    type="text"
                    value={protocolValue}
                    onChange={(e) => setProtocolValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleProtocolSave();
                      if (e.key === "Escape") setEditingProtocol(false);
                    }}
                    onBlur={handleProtocolSave}
                  />
                ) : (
                  <EditableField onClick={startProtocolEdit}>
                    {svc.protocol}
                  </EditableField>
                )}
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Address</ConfigLabel>
              <ConfigValue $mono>
                {editingAddress ? (
                  <FieldInput
                    autoFocus
                    type="text"
                    value={addressValue}
                    onChange={(e) => setAddressValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleAddressSave();
                      if (e.key === "Escape") setEditingAddress(false);
                    }}
                    onBlur={handleAddressSave}
                  />
                ) : (
                  <EditableField onClick={startAddressEdit}>
                    {svc.address}
                  </EditableField>
                )}
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Price</ConfigLabel>
              <ConfigValue>
                {editingPrice ? (
                  <PriceInput
                    autoFocus
                    type="number"
                    min={0}
                    value={priceValue}
                    onChange={(e) => setPriceValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handlePriceSave();
                      if (e.key === "Escape") setEditingPrice(false);
                    }}
                    onBlur={handlePriceSave}
                  />
                ) : (
                  <EditablePrice onClick={startPriceEdit}>
                    {formatAmount(svc.price, chain).value} {unitLabel(chain)}
                  </EditablePrice>
                )}
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Auth</ConfigLabel>
              <ConfigValue>
                <AuthSelect
                  value={svc.auth || "on"}
                  onChange={(e) => handleAuthChange(e.target.value)}
                >
                  {authOptions.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </AuthSelect>
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Auth Scheme</ConfigLabel>
              <ConfigValue>
                <AuthSelect
                  value={svc.auth_scheme || "AUTH_SCHEME_L402"}
                  onChange={(e) =>
                    handleAuthSchemeChange(e.target.value as AuthScheme)
                  }
                >
                  {authSchemeOptions.map((opt) => (
                    <option key={opt} value={opt}>
                      {authSchemeLabels[opt]}
                    </option>
                  ))}
                </AuthSelect>
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Host Regexp</ConfigLabel>
              <ConfigValue $mono>
                {editingHostregexp ? (
                  <FieldInput
                    autoFocus
                    type="text"
                    value={hostregexpValue}
                    placeholder="e.g. ^example\.com$"
                    onChange={(e) => setHostregexpValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleHostregexpSave();
                      if (e.key === "Escape") setEditingHostregexp(false);
                    }}
                    onBlur={handleHostregexpSave}
                  />
                ) : (
                  <EditableField onClick={startHostregexpEdit}>
                    {hostregexp || "(none)"}
                  </EditableField>
                )}
              </ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>Path Regexp</ConfigLabel>
              <ConfigValue $mono>
                {editingPathregexp ? (
                  <FieldInput
                    autoFocus
                    type="text"
                    value={pathregexpValue}
                    placeholder="e.g. ^/api/.*"
                    onChange={(e) => setPathregexpValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handlePathregexpSave();
                      if (e.key === "Escape") setEditingPathregexp(false);
                    }}
                    onBlur={handlePathregexpSave}
                  />
                ) : (
                  <EditableField onClick={startPathregexpEdit}>
                    {pathregexp || "(none)"}
                  </EditableField>
                )}
              </ConfigValue>
            </ConfigRow>
          </div>
        </Card>

        <Card>
          <SectionTitle>Test Endpoint</SectionTitle>
          <CodeBlock>
            {testEndpoint ? testEndpoint : "Loading server info..."}
          </CodeBlock>
          <HelpText>
            Aperture will respond with HTTP 402 and a{" "}
            <Code>WWW-Authenticate</Code> header. For L402, this contains a
            macaroon and Lightning invoice. For MPP (Payment scheme), it
            contains a JSON charge intent with a BOLT-11 invoice.
          </HelpText>
        </Card>
      </ConfigGrid>

      <Card style={{ marginBottom: 24 }}>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            marginBottom: 16,
          }}
        >
          <SectionTitle style={{ margin: 0 }}>Payment Backend</SectionTitle>
          {svc.payment?.lnd_host && (
            <Button
              variant="ghost"
              compact
              disabled={saving}
              onClick={handleClearPayment}
            >
              Clear override
            </Button>
          )}
        </div>
        {svc.payment?.lnd_host ? (
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <ConfigRow>
              <ConfigLabel>Merchant lnd</ConfigLabel>
              <ConfigValue $mono>{svc.payment.lnd_host}</ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>tls.cert</ConfigLabel>
              <ConfigValue $mono>{svc.payment.tls_path}</ConfigValue>
            </ConfigRow>
            <ConfigRow>
              <ConfigLabel>macaroon</ConfigLabel>
              <ConfigValue $mono>{svc.payment.mac_path}</ConfigValue>
            </ConfigRow>
            <HelpText>
              Invoices for this service are issued against the merchant&apos;s
              own lnd, so payments land in their wallet — the gateway never
              takes custody. Changes take effect on the next prism restart.
            </HelpText>
          </div>
        ) : (
          <HelpText style={{ marginTop: 0 }}>
            This service uses the gateway&apos;s global lnd. To route
            payments to a merchant&apos;s own lnd, set the{" "}
            <Code>payment</Code> block via the admin API or{" "}
            <Code>prismcli services update --payment-*</Code>.
          </HelpText>
        )}
      </Card>

      <CardNopad>
        <TableHeader>
          <SectionTitle style={{ marginBottom: 0 }}>
            Recent Transactions
          </SectionTitle>
          <ActionLink
            href={`/transactions?service=${encodeURIComponent(decodedName)}`}
          >
            View all &rarr;
          </ActionLink>
        </TableHeader>
        <div style={{ overflowX: "auto" }}>
          <Table>
            <thead>
              <HeadRow>
                <Th>ID</Th>
                <Th style={{ textAlign: "right" }}>Amount</Th>
                <Th>State</Th>
                <Th>Payment Hash</Th>
                <Th>Created</Th>
              </HeadRow>
            </thead>
            <tbody>
              {transactions?.length ? (
                transactions.slice(0, 10).map((tx) => (
                  <Row key={tx.id}>
                    <IdCell>{tx.id}</IdCell>
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
                  </Row>
                ))
              ) : (
                <tr>
                  <td colSpan={5}>
                    <EmptyState
                      title="No transactions yet"
                      description="Transactions for this service will appear here once clients start paying."
                    />
                  </td>
                </tr>
              )}
            </tbody>
          </Table>
        </div>
      </CardNopad>
    </div>
  );
}

export default function ServiceDetailPage() {
  return (
    <Suspense fallback={null}>
      <ServiceDetailContent />
    </Suspense>
  );
}
