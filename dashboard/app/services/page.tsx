"use client";

import { useState, useCallback } from "react";
import Link from "next/link";
import {
  useServices,
  updateService,
  createService,
  deleteService,
} from "@/lib/api";
import styled from "@emotion/styled";
import { toast } from "@/components/Toast";
import type { ServiceCreateRequest } from "@/lib/types";
import Button from "@/components/Button";
import PageHeader from "@/components/PageHeader";
import EmptyState from "@/components/EmptyState";
import OverflowMenu from "@/components/OverflowMenu";
import Tooltip from "@/components/Tooltip";
import SortHeader, { useSort } from "@/components/SortHeader";
import ErrorBanner from "@/components/ErrorBanner";

const authOptions = ["on", "off", "freebie 1", "freebie 5", "freebie 10"];

const initialForm: ServiceCreateRequest = {
  name: "",
  address: "",
  protocol: "http",
  hostregexp: ".*",
  pathregexp: "",
  price: 0,
  auth: "on",
};

const Styled = {
  Card: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    overflow: hidden;
  `,
  FormCard: styled.div`
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-radius: 8px;
    padding: 24px 24px 20px;
    margin-bottom: 20px;
    border: 1px solid ${(p) => p.theme.colors.lightBlue};
  `,
  FormTitle: styled.div`
    font-size: 15px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.offWhite};
    margin-bottom: 20px;
  `,
  Grid3: styled.div`
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 16px;
    margin-bottom: 16px;
  `,
  Grid2: styled.div`
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    margin-bottom: 16px;
  `,
  Label: styled.label`
    display: flex;
    align-items: center;
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  `,
  Input: styled.input`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 10px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 100%;
    transition: border-color 0.15s;

    &:focus {
      border-color: ${(p) => p.theme.colors.gray};
    }
  `,
  Select: styled.select`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 10px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 100%;
    transition: border-color 0.15s;
  `,
  AdvancedToggle: styled.button`
    background: transparent;
    border: none;
    color: ${(p) => p.theme.colors.gray};
    font-size: 13px;
    font-family: ${(p) => p.theme.fonts.open};
    cursor: pointer;
    padding: 0;
    transition: color 0.15s;

    &:hover {
      color: ${(p) => p.theme.colors.offWhite};
    }
  `,
  FormActions: styled.div`
    display: flex;
    gap: 12px;
    margin-top: 20px;
    padding-top: 16px;
    border-top: 1px solid ${(p) => p.theme.colors.blue};
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
  Td: styled.td`
    padding: 14px 16px;
  `,
  NameLink: styled(Link)`
    color: ${(p) => p.theme.colors.white};
    text-decoration: none;
    border-bottom: 1px dashed transparent;
    transition: border-color 0.15s ease;

    &:hover {
      border-color: ${(p) => p.theme.colors.gray};
    }
  `,
  AddressCell: styled.td`
    padding: 14px 16px;
    color: ${(p) => p.theme.colors.lightningGray};
    font-size: 13px;
    font-family: monospace;
  `,
  ProtoBadge: styled.span`
    display: inline-block;
    padding: 2px 8px;
    border-radius: 4px;
    font-size: 12px;
    background-color: ${(p) => p.theme.colors.overlay};
    color: ${(p) => p.theme.colors.lightningGray};
  `,
  PriceCell: styled.td`
    padding: 14px 16px;
    text-align: right;
    font-variant-numeric: tabular-nums;
  `,
  EditablePrice: styled.span`
    cursor: pointer;
    color: ${(p) => p.theme.colors.gold};
    border-bottom: 1px dashed ${(p) => p.theme.colors.blue};
    padding-bottom: 1px;
    transition: border-color 0.15s ease;

    &:hover {
      border-color: ${(p) => p.theme.colors.gold};
    }
  `,
  PriceInput: styled.input`
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.gold};
    padding: 10px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    outline: none;
    width: 80px;
    text-align: right;
  `,
  AuthBadge: styled.span<{ $level: "on" | "off" | "freebie" }>`
    display: inline-block;
    padding: 2px 10px;
    border-radius: 100px;
    font-size: 12px;
    font-weight: 600;
    color: ${(p) =>
      p.$level === "off"
        ? p.theme.colors.gray
        : p.$level === "freebie"
          ? p.theme.colors.lightningYellow
          : p.theme.colors.lightningGreen};
    background-color: ${(p) =>
      p.$level === "off"
        ? p.theme.colors.overlay
        : p.$level === "freebie"
          ? "rgba(245,158,11,0.1)"
          : "rgba(16,185,129,0.1)"};
  `,
  Skeleton: styled.div<{ $width?: number }>`
    height: 14px;
    width: ${(p) => p.$width || 80}px;
    background-color: ${(p) => p.theme.colors.blue};
    border-radius: 4px;
  `,
};

export default function ServicesPage() {
  const {
    data: services,
    isLoading,
    error: servicesError,
    mutate: refreshServices,
  } = useServices();
  const { sorted, sortField, sortDir, onSort } = useSort(services, "name");
  const [editingPrice, setEditingPrice] = useState<string | null>(null);
  const [priceValue, setPriceValue] = useState("");
  const [showAdd, setShowAdd] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState<ServiceCreateRequest>({ ...initialForm });

  const handlePriceSave = useCallback(
    async (name: string) => {
      const price = parseInt(priceValue, 10);
      if (isNaN(price) || price < 0) return;
      setSaving(true);
      try {
        await updateService(name, { price });
        toast(`Price updated to ${price} sats`);
      } catch (e: unknown) {
        toast(
          e instanceof Error ? e.message : "Failed to update price",
          "error"
        );
      }
      setSaving(false);
      setEditingPrice(null);
    },
    [priceValue]
  );

  const handleAuthChange = useCallback(async (name: string, auth: string) => {
    setSaving(true);
    try {
      await updateService(name, { auth });
      toast(`Auth updated to "${auth}"`);
    } catch (e: unknown) {
      toast(e instanceof Error ? e.message : "Failed to update auth", "error");
    }
    setSaving(false);
  }, []);

  const handleDelete = useCallback(async (name: string) => {
    if (!confirm(`Delete service "${name}"? This cannot be undone.`)) return;
    setSaving(true);
    try {
      await deleteService(name);
      toast(`Service "${name}" deleted`);
    } catch (e: unknown) {
      toast(
        e instanceof Error ? e.message : "Failed to delete service",
        "error"
      );
    }
    setSaving(false);
  }, []);

  const handleCreate = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
      if (!form.name || !form.address) {
        toast("Name and address are required", "error");
        return;
      }
      setSaving(true);
      try {
        await createService(form);
        toast(`Service "${form.name}" created`);
        setForm({ ...initialForm });
        setShowAdd(false);
        setShowAdvanced(false);
      } catch (e: unknown) {
        toast(
          e instanceof Error ? e.message : "Failed to create service",
          "error"
        );
      }
      setSaving(false);
    },
    [form]
  );

  const toggleAdd = useCallback(() => {
    setShowAdd((s) => {
      if (s) setShowAdvanced(false);
      return !s;
    });
  }, []);

  const cancelAdd = useCallback(() => {
    setShowAdd(false);
    setShowAdvanced(false);
    setForm({ ...initialForm });
  }, []);

  const toggleAdvanced = useCallback(() => setShowAdvanced((s) => !s), []);

  function getAuthLevel(auth: string): "on" | "off" | "freebie" {
    if (auth === "off" || auth === "false") return "off";
    if (auth.startsWith("freebie")) return "freebie";
    return "on";
  }

  const {
    Card,
    FormCard,
    FormTitle,
    Grid3,
    Grid2,
    Label,
    Input,
    Select,
    AdvancedToggle,
    FormActions,
    Table,
    HeadRow,
    Row,
    Td,
    NameLink,
    AddressCell,
    ProtoBadge,
    PriceCell,
    EditablePrice,
    PriceInput,
    AuthBadge,
    Skeleton,
  } = Styled;

  return (
    <div>
      {servicesError && (
        <ErrorBanner
          message="Failed to load services. Check that Aperture is running."
          onRetry={() => refreshServices()}
        />
      )}

      <PageHeader
        title="Services"
        description="Manage the APIs behind your L402 paywall."
        action={
          <Button
            variant={showAdd ? "ghost" : "primary"}
            compact
            onClick={toggleAdd}
          >
            {showAdd ? "Cancel" : "+ Add Service"}
          </Button>
        }
      />

      {showAdd && (
        <FormCard>
          <FormTitle>New Service</FormTitle>
          <form onSubmit={handleCreate}>
            <Grid3>
              <div>
                <Label>
                  Name *
                  <Tooltip text="A unique identifier for this service. Used in API responses and transaction records." />
                </Label>
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="my-service"
                />
              </div>
              <div>
                <Label>
                  Address *
                  <Tooltip text="The IP and port of your backend service. Aperture will proxy authenticated requests here." />
                </Label>
                <Input
                  value={form.address}
                  onChange={(e) =>
                    setForm({ ...form, address: e.target.value })
                  }
                  placeholder="127.0.0.1:8080"
                />
              </div>
              <div>
                <Label>
                  Protocol
                  <Tooltip text="How Aperture connects to your backend. Use https if your backend has TLS enabled." />
                </Label>
                <Select
                  value={form.protocol}
                  onChange={(e) =>
                    setForm({ ...form, protocol: e.target.value })
                  }
                >
                  <option value="http">http</option>
                  <option value="https">https</option>
                </Select>
              </div>
            </Grid3>
            <Grid2>
              <div>
                <Label>
                  Price (sats)
                  <Tooltip text="Cost per request in satoshis. Clients pay a Lightning invoice for this amount to receive an L402 access token." />
                </Label>
                <Input
                  type="number"
                  min={0}
                  value={form.price ?? 0}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      price: parseInt(e.target.value, 10) || 0,
                    })
                  }
                />
              </div>
              <div>
                <Label>
                  Auth
                  <Tooltip
                    text={`"on" = payment required. "off" = no payment. "freebie N" = first N requests per IP are free, then payment is required.`}
                  />
                </Label>
                <Select
                  value={form.auth}
                  onChange={(e) => setForm({ ...form, auth: e.target.value })}
                >
                  {authOptions.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </Select>
              </div>
            </Grid2>
            <AdvancedToggle
              type="button"
              onClick={toggleAdvanced}
              style={{ marginBottom: showAdvanced ? 16 : 0 }}
            >
              {showAdvanced ? "\u25BE" : "\u25B8"} Advanced options
            </AdvancedToggle>
            {showAdvanced && (
              <Grid2>
                <div>
                  <Label>
                    Host Regexp
                    <Tooltip text="Regex matched against the HTTP Host header. Determines which incoming requests route to this service. Default .* matches all hosts." />
                  </Label>
                  <Input
                    value={form.hostregexp}
                    onChange={(e) =>
                      setForm({ ...form, hostregexp: e.target.value })
                    }
                    placeholder=".*"
                  />
                </div>
                <div>
                  <Label>
                    Path Regexp
                    <Tooltip text="Regex matched against the request URL path. Only requests with matching paths are routed to this service. Leave empty to match all paths." />
                  </Label>
                  <Input
                    value={form.pathregexp}
                    onChange={(e) =>
                      setForm({ ...form, pathregexp: e.target.value })
                    }
                    placeholder="^/api/.*$"
                  />
                </div>
              </Grid2>
            )}
            <FormActions>
              <Button type="submit" variant="primary" compact disabled={saving}>
                {saving ? "Creating..." : "Create Service"}
              </Button>
              <Button type="button" variant="ghost" compact onClick={cancelAdd}>
                Cancel
              </Button>
            </FormActions>
          </form>
        </FormCard>
      )}

      <Card>
        <div style={{ overflowX: "auto" }}>
          <Table>
            <thead>
              <HeadRow>
                <SortHeader
                  label="Name"
                  field="name"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Address"
                  field="address"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Protocol"
                  field="protocol"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                />
                <SortHeader
                  label="Price"
                  field="price"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                  align="right"
                  tooltip={
                    <Tooltip text="Cost in satoshis per L402 token. Click a value to edit it inline." />
                  }
                />
                <SortHeader
                  label="Auth"
                  field="auth"
                  sortField={sortField}
                  sortDir={sortDir}
                  onSort={onSort}
                  tooltip={
                    <Tooltip
                      text={`"on" = payment required. "off" = free access. "freebie N" = N free requests per IP before payment.`}
                    />
                  }
                />
                <th style={{ padding: "10px 16px", width: 48 }} />
              </HeadRow>
            </thead>
            <tbody>
              {isLoading ? (
                Array.from({ length: 3 }).map((_, i) => (
                  <Styled.Row key={i}>
                    {Array.from({ length: 6 }).map((_, j) => (
                      <Td key={j}>
                        <Skeleton $width={j === 5 ? 24 : 80} />
                      </Td>
                    ))}
                  </Styled.Row>
                ))
              ) : sorted?.length ? (
                sorted.map((svc) => (
                  <Row key={svc.name}>
                    <Td style={{ fontWeight: 600 }}>
                      <NameLink
                        href={`/services/detail?name=${encodeURIComponent(svc.name)}`}
                      >
                        {svc.name}
                      </NameLink>
                    </Td>
                    <AddressCell>{svc.address}</AddressCell>
                    <Td>
                      <ProtoBadge>{svc.protocol}</ProtoBadge>
                    </Td>
                    <PriceCell>
                      {editingPrice === svc.name ? (
                        <PriceInput
                          autoFocus
                          type="number"
                          min={0}
                          value={priceValue}
                          onChange={(e) => setPriceValue(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") handlePriceSave(svc.name);
                            if (e.key === "Escape") setEditingPrice(null);
                          }}
                          onBlur={() => handlePriceSave(svc.name)}
                        />
                      ) : (
                        <EditablePrice
                          onClick={() => {
                            setEditingPrice(svc.name);
                            setPriceValue(String(svc.price));
                          }}
                          title="Click to edit"
                        >
                          {svc.price.toLocaleString()} sats
                        </EditablePrice>
                      )}
                    </PriceCell>
                    <Td>
                      <AuthBadge $level={getAuthLevel(svc.auth || "on")}>
                        {svc.auth || "on"}
                      </AuthBadge>
                    </Td>
                    <Td style={{ textAlign: "right" }}>
                      <OverflowMenu
                        items={[
                          {
                            label: "Edit price",
                            onClick: () => {
                              setEditingPrice(svc.name);
                              setPriceValue(String(svc.price));
                            },
                          },
                          {
                            label: "Change auth",
                            onClick: () => {
                              const next =
                                authOptions[
                                  (authOptions.indexOf(svc.auth || "on") + 1) %
                                    authOptions.length
                                ];
                              handleAuthChange(svc.name, next);
                            },
                          },
                          {
                            label: "Delete",
                            danger: true,
                            onClick: () => handleDelete(svc.name),
                          },
                        ]}
                      />
                    </Td>
                  </Row>
                ))
              ) : (
                <tr>
                  <td colSpan={6}>
                    <EmptyState
                      title="No services configured"
                      description="Services are the APIs behind your paywall. Add one to start accepting Lightning payments."
                      action={
                        <Button
                          variant="primary"
                          compact
                          onClick={() => setShowAdd(true)}
                        >
                          + Add Service
                        </Button>
                      }
                    />
                  </td>
                </tr>
              )}
            </tbody>
          </Table>
        </div>
      </Card>
    </div>
  );
}
