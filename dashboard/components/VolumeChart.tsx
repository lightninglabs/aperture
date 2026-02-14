"use client";

import { useCallback } from "react";
import { BarStack } from "@visx/shape";
import { Group } from "@visx/group";
import { scaleLinear, scaleBand, scaleOrdinal } from "@visx/scale";
import { AxisBottom, AxisLeft } from "@visx/axis";
import { GridRows } from "@visx/grid";
import { ParentSize } from "@visx/responsive";
import { localPoint } from "@visx/event";
import { useTooltip, useTooltipInPortal } from "@visx/tooltip";
import { theme } from "@/lib/theme";
import type { Transaction } from "@/lib/types";

interface Bucket {
  label: string;
  settled: number;
  pending: number;
}

const STATES = ["settled", "pending"] as const;
const STATE_COLORS: Record<string, string> = {
  settled: theme.colors.lightningGreen,
  pending: theme.colors.lightningYellow,
};

const margin = { top: 10, right: 16, bottom: 32, left: 48 };

function bucketByDay(transactions: Transaction[]): Bucket[] {
  if (!transactions.length) return [];

  const sorted = [...transactions].sort(
    (a, b) =>
      new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
  );

  const earliest = new Date(sorted[0].created_at);
  const latest = new Date(sorted[sorted.length - 1].created_at);
  const rangeMs = latest.getTime() - earliest.getTime();
  const useHours = rangeMs < 2 * 86400000;

  const buckets = new Map<string, { settled: number; pending: number }>();

  for (const tx of sorted) {
    const d = new Date(tx.created_at);
    const key = useHours
      ? `${d.getMonth() + 1}/${d.getDate()} ${d.getHours()}:00`
      : `${d.getMonth() + 1}/${d.getDate()}`;

    if (!buckets.has(key)) buckets.set(key, { settled: 0, pending: 0 });
    const b = buckets.get(key)!;
    if (tx.state === "settled") b.settled++;
    else b.pending++;
  }

  return Array.from(buckets.entries()).map(([label, counts]) => ({
    label,
    ...counts,
  }));
}

interface TooltipData {
  label: string;
  settled: number;
  pending: number;
}

function Chart({
  data,
  width,
  height,
}: {
  data: Bucket[];
  width: number;
  height: number;
}) {
  const {
    showTooltip,
    hideTooltip,
    tooltipData,
    tooltipLeft,
    tooltipTop,
    tooltipOpen,
  } = useTooltip<TooltipData>();

  const { containerRef, TooltipInPortal } = useTooltipInPortal({
    detectBounds: true,
    scroll: true,
  });

  const xMax = width - margin.left - margin.right;
  const yMax = height - margin.top - margin.bottom;

  const xScale = scaleBand<string>({
    domain: data.map((d) => d.label),
    range: [0, xMax],
    padding: 0.3,
  });

  const maxTotal = Math.max(...data.map((d) => d.settled + d.pending), 1);

  const yScale = scaleLinear<number>({
    domain: [0, maxTotal],
    range: [yMax, 0],
    nice: true,
  });

  const colorScale = scaleOrdinal<string, string>({
    domain: [...STATES],
    range: STATES.map((s) => STATE_COLORS[s]),
  });

  const handleTooltip = useCallback(
    (
      bar: Bucket,
      event: React.MouseEvent<SVGRectElement>,
    ) => {
      const point = localPoint(event);
      if (!point) return;
      showTooltip({
        tooltipData: { label: bar.label, settled: bar.settled, pending: bar.pending },
        tooltipLeft: point.x,
        tooltipTop: point.y,
      });
    },
    [showTooltip],
  );

  return (
    <div ref={containerRef} style={{ position: "relative" }}>
      <svg width={width} height={height}>
        <Group left={margin.left} top={margin.top}>
          <GridRows
            scale={yScale}
            width={xMax}
            stroke={theme.colors.blue}
            strokeOpacity={0.5}
            strokeDasharray="2,4"
            numTicks={4}
          />
          <BarStack<Bucket, string>
            data={data}
            keys={[...STATES]}
            x={(d) => d.label}
            xScale={xScale}
            yScale={yScale}
            color={colorScale}
          >
            {(barStacks) =>
              barStacks.map((barStack) =>
                barStack.bars.map((bar) => (
                  <rect
                    key={`bar-stack-${barStack.index}-${bar.index}`}
                    x={bar.x}
                    y={bar.y}
                    height={bar.height}
                    width={bar.width}
                    fill={bar.color}
                    rx={2}
                    onMouseMove={(e) => handleTooltip(data[bar.index], e)}
                    onMouseLeave={hideTooltip}
                    style={{ cursor: "pointer" }}
                  />
                )),
              )
            }
          </BarStack>
          <AxisLeft
            scale={yScale}
            stroke={theme.colors.blue}
            tickStroke="transparent"
            numTicks={4}
            tickLabelProps={{
              fill: theme.colors.gray,
              fontSize: 11,
              fontFamily: theme.fonts.open,
              textAnchor: "end",
              dy: "0.33em",
            }}
            hideAxisLine
          />
          <AxisBottom
            scale={xScale}
            top={yMax}
            stroke={theme.colors.blue}
            tickStroke="transparent"
            tickLabelProps={{
              fill: theme.colors.gray,
              fontSize: 10,
              fontFamily: theme.fonts.open,
              textAnchor: "middle",
              angle: data.length > 14 ? -45 : 0,
              dy: data.length > 14 ? -4 : 0,
              dx: data.length > 14 ? -10 : 0,
            }}
          />
        </Group>
      </svg>
      {tooltipOpen && tooltipData && (
        <TooltipInPortal
          left={tooltipLeft}
          top={tooltipTop}
          style={{
            backgroundColor: "rgba(0, 0, 0, 0.92)",
            border: `1px solid ${theme.colors.lightBlue}`,
            borderRadius: 6,
            padding: "8px 12px",
            fontSize: 12,
            fontFamily: theme.fonts.open,
            lineHeight: 1.6,
            boxShadow: "0 4px 20px rgba(0,0,0,0.8)",
            pointerEvents: "none",
            zIndex: 9999,
            whiteSpace: "nowrap",
          }}
        >
          <div style={{ color: theme.colors.offWhite, fontWeight: 600, marginBottom: 4 }}>
            {tooltipData.label}
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <span
              style={{
                width: 8,
                height: 8,
                borderRadius: "50%",
                backgroundColor: theme.colors.lightningGreen,
                display: "inline-block",
              }}
            />
            <span style={{ color: theme.colors.lightningGray }}>
              Settled: <strong style={{ color: theme.colors.lightningGreen }}>{tooltipData.settled}</strong>
            </span>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <span
              style={{
                width: 8,
                height: 8,
                borderRadius: "50%",
                backgroundColor: theme.colors.lightningYellow,
                display: "inline-block",
              }}
            />
            <span style={{ color: theme.colors.lightningGray }}>
              Pending: <strong style={{ color: theme.colors.lightningYellow }}>{tooltipData.pending}</strong>
            </span>
          </div>
        </TooltipInPortal>
      )}
    </div>
  );
}

interface Props {
  transactions: Transaction[];
}

export default function VolumeChart({ transactions }: Props) {
  const data = bucketByDay(transactions);

  if (!data.length) {
    return (
      <div
        style={{
          height: 200,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: theme.colors.gray,
          fontFamily: theme.fonts.open,
          fontSize: 14,
        }}
      >
        No transaction data yet.
      </div>
    );
  }

  return (
    <div style={{ height: 200 }}>
      <ParentSize>
        {({ width, height }) =>
          width > 0 && height > 0 ? (
            <Chart data={data} width={width} height={height} />
          ) : null
        }
      </ParentSize>
    </div>
  );
}
