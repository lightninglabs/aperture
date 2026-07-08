"use client";

import React, { useCallback, useMemo, useState } from "react";
import styled from "@emotion/styled";

export type SortDir = "asc" | "desc";

interface Props {
  label: string;
  field: string;
  sortField: string | null;
  sortDir: SortDir;
  onSort: (field: string) => void;
  align?: "left" | "right";
  tooltip?: React.ReactNode;
}

const Styled = {
  Th: styled.th<{ $active?: boolean; $align?: string }>`
    padding: 10px 16px;
    font-weight: 600;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.8px;
    color: ${(p) =>
      p.$active ? p.theme.colors.offWhite : p.theme.colors.gray};
    cursor: pointer;
    user-select: none;
    text-align: ${(p) => p.$align || "left"};
    white-space: nowrap;
    transition: color 0.15s;

    &:hover {
      color: ${(p) => p.theme.colors.offWhite};
    }
  `,
};

const SortHeader: React.FC<Props> = ({
  label,
  field,
  sortField,
  sortDir,
  onSort,
  align = "left",
  tooltip,
}) => {
  const active = sortField === field;
  const arrow = active ? (sortDir === "asc" ? " \u25B4" : " \u25BE") : "";

  const handleClick = useCallback(() => onSort(field), [onSort, field]);

  return (
    <Styled.Th onClick={handleClick} $active={active} $align={align}>
      {label}
      {arrow}
      {tooltip}
    </Styled.Th>
  );
};

export default SortHeader;

export function useSort<T>(
  data: T[] | undefined,
  defaultField: string | null = null,
  defaultDir: SortDir = "asc"
) {
  const [sortField, setSortField] = useState<string | null>(defaultField);
  const [sortDir, setSortDir] = useState<SortDir>(defaultDir);

  const onSort = useCallback(
    (field: string) => {
      if (sortField === field) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortField(field);
        setSortDir("asc");
      }
    },
    [sortField]
  );

  const sorted = useMemo(() => {
    if (!data || !sortField) return data;
    return [...data].sort((a, b) => {
      const aVal = (a as Record<string, unknown>)[sortField];
      const bVal = (b as Record<string, unknown>)[sortField];

      if (aVal == null && bVal == null) return 0;
      if (aVal == null) return 1;
      if (bVal == null) return -1;

      let cmp: number;
      if (typeof aVal === "number" && typeof bVal === "number") {
        cmp = aVal - bVal;
      } else {
        cmp = String(aVal).localeCompare(String(bVal));
      }
      return sortDir === "asc" ? cmp : -cmp;
    });
  }, [data, sortField, sortDir]);

  return { sorted, sortField, sortDir, onSort };
}
