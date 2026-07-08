"use client";

import { Group } from "@visx/group";
import { Bar } from "@visx/shape";
import { scaleLinear, scaleBand } from "@visx/scale";
import { AxisLeft, AxisBottom } from "@visx/axis";
import { GridColumns } from "@visx/grid";
import { ParentSize } from "@visx/responsive";
import { LinearGradient } from "@visx/gradient";
import { useTooltip, useTooltipInPortal } from "@visx/tooltip";
import { theme } from "@/lib/theme";
import type { ServiceRevenueItem } from "@/lib/types";

const margin = { top: 10, right: 40, bottom: 30, left: 120 };

interface Props {
  data: ServiceRevenueItem[];
}

function Chart({
  data,
  width,
  height,
}: Props & { width: number; height: number }) {
  const {
    showTooltip,
    hideTooltip,
    tooltipData,
    tooltipLeft,
    tooltipTop,
    tooltipOpen,
  } = useTooltip<ServiceRevenueItem>();

  const { containerRef, TooltipInPortal } = useTooltipInPortal({
    detectBounds: true,
    scroll: true,
  });

  const xMax = width - margin.left - margin.right;
  const yMax = height - margin.top - margin.bottom;

  const xScale = scaleLinear<number>({
    domain: [0, Math.max(...data.map((d) => d.total_revenue_sats), 1)],
    range: [0, xMax],
    nice: true,
  });

  const yScale = scaleBand<string>({
    domain: data.map((d) => d.service_name),
    range: [0, yMax],
    padding: 0.35,
  });

  return (
    <div ref={containerRef} style={{ position: "relative" }}>
      <svg width={width} height={height}>
        <LinearGradient id="bar-gradient" from="#5D5FEF" to="#3B82F6" />
        <Group left={margin.left} top={margin.top}>
          <GridColumns
            scale={xScale}
            height={yMax}
            stroke="#384770"
            strokeOpacity={0.5}
            strokeDasharray="2,4"
            numTicks={5}
          />
          {data.map((d) => {
            const barWidth = xScale(d.total_revenue_sats);
            const barHeight = yScale.bandwidth();
            const barY = yScale(d.service_name) ?? 0;
            return (
              <Bar
                key={d.service_name}
                x={0}
                y={barY}
                width={barWidth}
                height={barHeight}
                fill="url(#bar-gradient)"
                rx={3}
                onMouseMove={(e) => {
                  const svg = (e.target as SVGElement).ownerSVGElement;
                  if (!svg) return;
                  const point = svg.createSVGPoint();
                  point.x = e.clientX;
                  point.y = e.clientY;
                  const svgPoint = point.matrixTransform(
                    svg.getScreenCTM()?.inverse()
                  );
                  showTooltip({
                    tooltipData: d,
                    tooltipLeft: svgPoint.x,
                    tooltipTop: svgPoint.y - 10,
                  });
                }}
                onMouseLeave={hideTooltip}
                style={{ cursor: "pointer" }}
              />
            );
          })}
          <AxisLeft
            scale={yScale}
            stroke="#384770"
            tickStroke="transparent"
            tickLabelProps={{
              fill: "#B9BDC5",
              fontSize: 12,
              fontFamily: "Open Sans, sans-serif",
              textAnchor: "end",
              dy: "0.33em",
            }}
            hideAxisLine
          />
          <AxisBottom
            scale={xScale}
            top={yMax}
            stroke="#384770"
            tickStroke="#384770"
            numTicks={5}
            tickLabelProps={{
              fill: "#848a99",
              fontSize: 11,
              fontFamily: "Open Sans, sans-serif",
              textAnchor: "middle",
            }}
            label="sats"
            labelProps={{
              fill: "#848a99",
              fontSize: 11,
              fontFamily: "Open Sans, sans-serif",
              textAnchor: "middle",
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
            color: theme.colors.offWhite,
            fontFamily: theme.fonts.open,
            fontSize: 13,
            padding: "8px 12px",
            borderRadius: 6,
            boxShadow: "0 4px 20px rgba(0,0,0,0.8)",
            pointerEvents: "none",
            zIndex: 9999,
            whiteSpace: "nowrap",
          }}
        >
          <span style={{ color: theme.colors.gold, fontWeight: 600 }}>
            {tooltipData.total_revenue_sats.toLocaleString()}
          </span>{" "}
          sats
        </TooltipInPortal>
      )}
    </div>
  );
}

export default function RevenueChart({ data }: Props) {
  if (!data.length) {
    return (
      <div
        style={{
          height: 200,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: "#848a99",
          fontFamily: "Open Sans, sans-serif",
          fontSize: 14,
        }}
      >
        No revenue data yet.
      </div>
    );
  }

  return (
    <div style={{ height: 200, position: "relative" }}>
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
