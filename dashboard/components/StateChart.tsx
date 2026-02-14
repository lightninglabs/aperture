"use client";

import { Group } from "@visx/group";
import { Pie } from "@visx/shape";
import { Text } from "@visx/text";
import type { Transaction } from "@/lib/types";

interface StateCount {
  state: string;
  count: number;
}

const STATE_COLORS: Record<string, string> = {
  settled: "#10B981",
  pending: "#F59E0B",
};

const DEFAULT_COLOR = "#384770";

interface Props {
  transactions: Transaction[];
}

export default function StateChart({ transactions }: Props) {
  const size = 180;
  const half = size / 2;
  const outerRadius = half - 10;
  const innerRadius = half - 35;

  const counts: Record<string, number> = {};
  for (const tx of transactions) {
    counts[tx.state] = (counts[tx.state] || 0) + 1;
  }
  const data: StateCount[] = Object.entries(counts).map(([state, count]) => ({
    state,
    count,
  }));

  const total = transactions.length;

  if (!data.length) {
    return (
      <div
        style={{
          width: size,
          height: size,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          color: "#848a99",
          fontFamily: "Open Sans, sans-serif",
          fontSize: 14,
        }}
      >
        No data
      </div>
    );
  }

  return (
    <div>
      <svg width={size} height={size}>
        <Group top={half} left={half}>
          <Pie
            data={data}
            pieValue={(d) => d.count}
            outerRadius={outerRadius}
            innerRadius={innerRadius}
            padAngle={0.03}
            cornerRadius={3}
          >
            {(pie) =>
              pie.arcs.map((arc) => {
                const [centroidX, centroidY] = pie.path.centroid(arc);
                const color = STATE_COLORS[arc.data.state] || DEFAULT_COLOR;
                return (
                  <g key={arc.data.state}>
                    <path d={pie.path(arc) || ""} fill={color} />
                    <Text
                      x={centroidX}
                      y={centroidY}
                      textAnchor="middle"
                      verticalAnchor="middle"
                      fill="#ffffff"
                      fontSize={11}
                      fontFamily="Open Sans, sans-serif"
                      fontWeight={600}
                    >
                      {arc.data.count}
                    </Text>
                  </g>
                );
              })
            }
          </Pie>
          <Text
            textAnchor="middle"
            verticalAnchor="middle"
            fill="#f5f5f5"
            fontSize={24}
            fontFamily="Open Sans, sans-serif"
            fontWeight={700}
            dy={-4}
          >
            {total}
          </Text>
          <Text
            textAnchor="middle"
            verticalAnchor="middle"
            fill="#848a99"
            fontSize={10}
            fontFamily="Open Sans, sans-serif"
            dy={16}
          >
            total
          </Text>
        </Group>
      </svg>
      {/* Legend */}
      <div
        style={{
          display: "flex",
          gap: 16,
          justifyContent: "center",
          marginTop: 8,
        }}
      >
        {data.map((d) => (
          <div
            key={d.state}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              fontFamily: "Open Sans, sans-serif",
              fontSize: 12,
              color: "#B9BDC5",
            }}
          >
            <span
              style={{
                display: "inline-block",
                width: 8,
                height: 8,
                borderRadius: "50%",
                background: STATE_COLORS[d.state] || DEFAULT_COLOR,
              }}
            />
            {d.state}
          </div>
        ))}
      </div>
    </div>
  );
}
