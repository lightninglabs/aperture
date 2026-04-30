"use client";

import { useCallback } from "react";
import { AreaClosed, LinePath, Bar } from "@visx/shape";
import { scaleLinear, scaleTime } from "@visx/scale";
import { LinearGradient } from "@visx/gradient";
import { ParentSize } from "@visx/responsive";
import { curveBasis } from "@visx/curve";
import { localPoint } from "@visx/event";
import { bisector } from "d3-array";
import { useTooltip, useTooltipInPortal } from "@visx/tooltip";
import { theme } from "@/lib/theme";
import type { Transaction } from "@/lib/types";
import { formatAmount, unitLabel } from "@/lib/currency";

interface Bucket {
  time: Date;
  sats: number;
}

function bucketTransactions(transactions: Transaction[]): Bucket[] {
  if (!transactions.length) return [];

  const sorted = [...transactions].sort(
    (a, b) =>
      new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
  );

  const earliest = new Date(sorted[0].created_at);
  const latest = new Date(sorted[sorted.length - 1].created_at);
  const rangeMs = latest.getTime() - earliest.getTime();
  const bucketMs = rangeMs < 2 * 86400000 ? 3600000 : 86400000;

  const buckets = new Map<number, number>();
  for (const tx of sorted) {
    const t = new Date(tx.created_at).getTime();
    const key = Math.floor(t / bucketMs) * bucketMs;
    buckets.set(key, (buckets.get(key) || 0) + tx.price_sats);
  }

  const startKey = Math.floor(earliest.getTime() / bucketMs) * bucketMs;
  const endKey = Math.floor(latest.getTime() / bucketMs) * bucketMs;
  const result: Bucket[] = [];
  for (let k = startKey; k <= endKey; k += bucketMs) {
    result.push({ time: new Date(k), sats: buckets.get(k) || 0 });
  }
  return result;
}

const bisectDate = bisector<Bucket, Date>((d) => d.time).left;

function formatCompact(value: number): string {
  if (value >= 1_000_000) {
    const v = value / 1_000_000;
    return v % 1 === 0 ? `${v}M` : `${v.toFixed(1)}M`;
  }
  if (value >= 1_000) {
    const v = value / 1_000;
    return v % 1 === 0 ? `${v}k` : `${v.toFixed(1)}k`;
  }
  return String(value);
}

const LEFT_PAD = 50;

function Chart({
  data,
  width,
  height,
  chain,
}: {
  data: Bucket[];
  width: number;
  height: number;
  chain?: string;
}) {
  const {
    showTooltip,
    hideTooltip,
    tooltipData,
    tooltipLeft = 0,
    tooltipTop = 0,
    tooltipOpen,
  } = useTooltip<Bucket>();

  const { containerRef, TooltipInPortal } = useTooltipInPortal({
    detectBounds: true,
    scroll: true,
  });

  const padding = 2;
  const chartLeft = LEFT_PAD;
  const xScale = scaleTime({
    domain: [data[0].time, data[data.length - 1].time],
    range: [chartLeft + padding, width - padding],
  });

  const maxSats = Math.max(...data.map((d) => d.sats), 1);
  const yScale = scaleLinear<number>({
    domain: [0, maxSats],
    range: [height - padding, padding],
    nice: true,
  });

  const niceMax = yScale.domain()[1];

  const getX = (d: Bucket) => xScale(d.time);
  const getY = (d: Bucket) => yScale(d.sats);

  const handleTooltip = useCallback(
    (
      event: React.TouchEvent<SVGRectElement> | React.MouseEvent<SVGRectElement>
    ) => {
      const point = localPoint(event);
      if (!point) return;

      const x0 = xScale.invert(point.x);
      const idx = bisectDate(data, x0, 1);
      const d0 = data[idx - 1];
      const d1 = data[idx];
      let d = d0;
      if (d1 && d1.time) {
        d =
          x0.getTime() - d0.time.getTime() > d1.time.getTime() - x0.getTime()
            ? d1
            : d0;
      }

      showTooltip({
        tooltipData: d,
        tooltipLeft: xScale(d.time),
        tooltipTop: yScale(d.sats),
      });
    },
    [xScale, yScale, data, showTooltip]
  );

  if (data.length < 2) {
    return (
      <svg width={width} height={height}>
        <text
          x={width / 2}
          y={height / 2}
          textAnchor="middle"
          fill="#848a99"
          fontSize={13}
          fontFamily="Open Sans, sans-serif"
        >
          Not enough data for sparkline
        </text>
      </svg>
    );
  }

  return (
    <div ref={containerRef} style={{ position: "relative" }}>
      <svg width={width} height={height}>
        <LinearGradient
          id="activity-gradient"
          from="#3B82F6"
          to="#3B82F6"
          fromOpacity={0.25}
          toOpacity={0.0}
        />
        {/* Y-axis labels */}
        <text
          x={chartLeft - 6}
          y={yScale(niceMax)}
          textAnchor="end"
          dominantBaseline="hanging"
          fill={theme.colors.gray}
          fontSize={10}
          fontFamily={theme.fonts.open}
        >
          {formatCompact(niceMax)}
        </text>
        <text
          x={chartLeft - 6}
          y={height - padding}
          textAnchor="end"
          dominantBaseline="auto"
          fill={theme.colors.gray}
          fontSize={10}
          fontFamily={theme.fonts.open}
        >
          0
        </text>
        <AreaClosed
          data={data}
          x={getX}
          y={getY}
          yScale={yScale}
          fill="url(#activity-gradient)"
          curve={curveBasis}
        />
        <LinePath
          data={data}
          x={getX}
          y={getY}
          stroke="#3B82F6"
          strokeWidth={1.5}
          strokeOpacity={0.8}
          curve={curveBasis}
        />
        <Bar
          x={chartLeft}
          y={padding}
          width={width - chartLeft - padding}
          height={height - padding * 2}
          fill="transparent"
          onTouchStart={handleTooltip}
          onTouchMove={handleTooltip}
          onMouseMove={handleTooltip}
          onMouseLeave={hideTooltip}
        />
        {tooltipOpen && tooltipData && (
          <>
            <line
              x1={tooltipLeft}
              x2={tooltipLeft}
              y1={padding}
              y2={height - padding}
              stroke={theme.colors.lightBlue}
              strokeWidth={1}
              strokeDasharray="3,3"
              pointerEvents="none"
            />
            <circle
              cx={tooltipLeft}
              cy={tooltipTop}
              r={4}
              fill="#3B82F6"
              stroke={theme.colors.lightNavy}
              strokeWidth={2}
              pointerEvents="none"
            />
          </>
        )}
      </svg>
      {tooltipOpen && tooltipData && (
        <TooltipInPortal
          top={tooltipTop - 8}
          left={tooltipLeft + 12}
          style={{
            backgroundColor: "rgba(0, 0, 0, 0.92)",
            border: `1px solid ${theme.colors.lightBlue}`,
            borderRadius: 6,
            padding: "6px 10px",
            fontSize: 12,
            fontFamily: theme.fonts.open,
            lineHeight: 1.5,
            boxShadow: "0 4px 20px rgba(0,0,0,0.8)",
            pointerEvents: "none",
            zIndex: 9999,
            whiteSpace: "nowrap",
          }}
        >
          <div
            style={{ color: theme.colors.gray, fontSize: 11, marginBottom: 2 }}
          >
            {tooltipData.time.toLocaleDateString(undefined, {
              month: "short",
              day: "numeric",
              hour: "numeric",
              minute: "2-digit",
            })}
          </div>
          <div style={{ color: theme.colors.gold, fontWeight: 600 }}>
            {formatAmount(tooltipData.sats, chain).value} {unitLabel(chain)}
          </div>
        </TooltipInPortal>
      )}
    </div>
  );
}

interface Props {
  transactions: Transaction[];
  chain?: string;
}

export default function ActivityChart({ transactions, chain }: Props) {
  const data = bucketTransactions(transactions);

  if (data.length === 0) return null;

  return (
    <div style={{ width: "100%", height: 100 }}>
      <ParentSize>
        {({ width, height }) =>
          width > 0 && height > 0 ? (
            <Chart data={data} width={width} height={height} chain={chain} />
          ) : null
        }
      </ParentSize>
    </div>
  );
}
