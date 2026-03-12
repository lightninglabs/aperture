"use client";

import React, { useState, useRef, useEffect, useCallback } from "react";
import { createPortal } from "react-dom";
import styled from "@emotion/styled";

interface DateRange {
  from: string;
  to: string;
}

interface Preset {
  label: string;
  range: () => DateRange;
}

function toLocalDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}

const presets: Preset[] = [
  {
    label: "Last 7 days",
    range: () => {
      const to = new Date();
      const from = new Date();
      from.setDate(from.getDate() - 7);
      return { from: toLocalDate(from), to: toLocalDate(to) };
    },
  },
  {
    label: "Last 30 days",
    range: () => {
      const to = new Date();
      const from = new Date();
      from.setDate(from.getDate() - 30);
      return { from: toLocalDate(from), to: toLocalDate(to) };
    },
  },
  {
    label: "Last 90 days",
    range: () => {
      const to = new Date();
      const from = new Date();
      from.setDate(from.getDate() - 90);
      return { from: toLocalDate(from), to: toLocalDate(to) };
    },
  },
  {
    label: "This year",
    range: () => {
      const now = new Date();
      return { from: `${now.getFullYear()}-01-01`, to: toLocalDate(now) };
    },
  },
  {
    label: "All time",
    range: () => ({ from: "", to: "" }),
  },
];

interface Props {
  from: string;
  to: string;
  onChange: (range: DateRange) => void;
}

const Styled = {
  Trigger: styled.button`
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background-color: ${(p) => p.theme.colors.overlay};
    border: none;
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 8px 12px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px 4px 0 0;
    cursor: pointer;
    transition: border-color 0.15s;
    white-space: nowrap;

    &:hover {
      border-color: ${(p) => p.theme.colors.gray};
    }
  `,
  Caret: styled.span`
    font-size: 10px;
    color: ${(p) => p.theme.colors.gray};
  `,
  Panel: styled.div`
    position: absolute;
    background-color: ${(p) => p.theme.colors.lightNavy};
    border: 1px solid ${(p) => p.theme.colors.lightBlue};
    border-radius: 8px;
    z-index: 9999;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    width: 280px;
    overflow: hidden;
    animation: fade-in-up 0.15s ease-out;
  `,
  PresetList: styled.div`
    padding: 8px 0;
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
  `,
  PresetBtn: styled.button<{ $active?: boolean }>`
    display: block;
    width: 100%;
    text-align: left;
    background: ${(p) => (p.$active ? p.theme.colors.overlay : "transparent")};
    border: none;
    color: ${(p) =>
      p.$active ? p.theme.colors.white : p.theme.colors.offWhite};
    padding: 8px 16px;
    font-size: 13px;
    font-family: ${(p) => p.theme.fonts.open};
    font-weight: ${(p) => (p.$active ? 600 : 400)};
    cursor: pointer;
    transition: background-color 0.1s;

    &:hover {
      background-color: ${(p) => p.theme.colors.overlay};
    }
  `,
  CustomSection: styled.div`
    padding: 16px;
  `,
  CustomLabel: styled.div`
    font-size: 11px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.gray};
    text-transform: uppercase;
    letter-spacing: 0.5px;
    margin-bottom: 10px;
  `,
  DateRow: styled.div`
    display: flex;
    gap: 8px;
    margin-bottom: 12px;
  `,
  FieldLabel: styled.label`
    display: block;
    font-size: 11px;
    color: ${(p) => p.theme.colors.gray};
    margin-bottom: 4px;
  `,
  DateInput: styled.input`
    width: 100%;
    background-color: ${(p) => p.theme.colors.overlay};
    border: 1px solid ${(p) => p.theme.colors.blue};
    color: ${(p) => p.theme.colors.offWhite};
    padding: 6px 8px;
    font-size: 13px;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 4px;
    outline: none;
    color-scheme: dark;

    &:focus {
      border-color: ${(p) => p.theme.colors.lightBlue};
    }
  `,
  ApplyBtn: styled.button<{ $disabled?: boolean }>`
    width: 100%;
    background-color: ${(p) =>
      p.$disabled ? p.theme.colors.blue : p.theme.colors.purple};
    border: none;
    color: ${(p) => (p.$disabled ? p.theme.colors.gray : p.theme.colors.white)};
    padding: 8px 0;
    font-size: 13px;
    font-weight: 600;
    font-family: ${(p) => p.theme.fonts.open};
    border-radius: 6px;
    cursor: ${(p) => (p.$disabled ? "not-allowed" : "pointer")};
    transition: opacity 0.15s;

    &:hover:not([disabled]) {
      opacity: 0.9;
    }
  `,
};

const DateRangeFilter: React.FC<Props> = ({ from, to, onChange }) => {
  const [open, setOpen] = useState(false);
  const [customFrom, setCustomFrom] = useState(from);
  const [customTo, setCustomTo] = useState(to);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const activeLabel = getActiveLabel(from, to);

  const updatePos = useCallback(() => {
    if (!triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    setPos({
      top: rect.bottom + window.scrollY + 4,
      left: rect.left + window.scrollX,
    });
  }, []);

  const toggle = useCallback(() => setOpen((o) => !o), []);

  useEffect(() => {
    if (!open) return;
    updatePos();
    setCustomFrom(from);
    setCustomTo(to);

    function handleClick(e: MouseEvent) {
      if (
        triggerRef.current?.contains(e.target as Node) ||
        panelRef.current?.contains(e.target as Node)
      )
        return;
      setOpen(false);
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [open, from, to, updatePos]);

  const handlePreset = useCallback(
    (preset: Preset) => {
      onChange(preset.range());
      setOpen(false);
    },
    [onChange]
  );

  const handleApplyCustom = useCallback(() => {
    onChange({ from: customFrom, to: customTo });
    setOpen(false);
  }, [onChange, customFrom, customTo]);

  const {
    Trigger,
    Caret,
    Panel,
    PresetList,
    PresetBtn,
    CustomSection,
    CustomLabel,
    DateRow,
    FieldLabel,
    DateInput,
    ApplyBtn,
  } = Styled;

  const canApply = customFrom && customTo;

  return (
    <>
      <Trigger ref={triggerRef} onClick={toggle}>
        <svg
          width="14"
          height="14"
          viewBox="0 0 16 16"
          fill="none"
          style={{ flexShrink: 0 }}
        >
          <rect
            x="1"
            y="3"
            width="14"
            height="12"
            rx="2"
            stroke="currentColor"
            strokeWidth="1.5"
            fill="none"
            opacity={0.5}
          />
          <line
            x1="1"
            y1="7"
            x2="15"
            y2="7"
            stroke="currentColor"
            strokeWidth="1.5"
            opacity={0.5}
          />
          <line
            x1="5"
            y1="1"
            x2="5"
            y2="5"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            opacity={0.5}
          />
          <line
            x1="11"
            y1="1"
            x2="11"
            y2="5"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            opacity={0.5}
          />
        </svg>
        {activeLabel}
        <Caret>&#9662;</Caret>
      </Trigger>

      {open &&
        pos &&
        createPortal(
          <Panel ref={panelRef} style={{ top: pos.top, left: pos.left }}>
            <PresetList>
              {presets.map((preset) => (
                <PresetBtn
                  key={preset.label}
                  $active={activeLabel === preset.label}
                  onClick={() => handlePreset(preset)}
                >
                  {preset.label}
                </PresetBtn>
              ))}
            </PresetList>

            <CustomSection>
              <CustomLabel>Custom range</CustomLabel>
              <DateRow>
                <div style={{ flex: 1 }}>
                  <FieldLabel>From</FieldLabel>
                  <DateInput
                    type="date"
                    value={customFrom}
                    onChange={(e) => setCustomFrom(e.target.value)}
                  />
                </div>
                <div style={{ flex: 1 }}>
                  <FieldLabel>To</FieldLabel>
                  <DateInput
                    type="date"
                    value={customTo}
                    onChange={(e) => setCustomTo(e.target.value)}
                  />
                </div>
              </DateRow>
              <ApplyBtn
                $disabled={!canApply}
                disabled={!canApply}
                onClick={handleApplyCustom}
              >
                Apply
              </ApplyBtn>
            </CustomSection>
          </Panel>,
          document.body
        )}
    </>
  );
};

function getActiveLabel(from: string, to: string): string {
  if (!from && !to) return "All time";
  for (const preset of presets) {
    const range = preset.range();
    if (range.from === from && range.to === to) return preset.label;
  }
  if (from && to) return `${formatShort(from)} - ${formatShort(to)}`;
  if (from) return `From ${formatShort(from)}`;
  if (to) return `Until ${formatShort(to)}`;
  return "All time";
}

function formatShort(dateStr: string): string {
  const d = new Date(dateStr + "T00:00:00");
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export default DateRangeFilter;
