"use client";

import React from "react";
import { theme } from "@/lib/theme";

interface TabProps {
  active?: boolean;
  onClick?: () => void;
  children: React.ReactNode;
}

export const Tab: React.FC<TabProps> = ({ active, onClick, children }) => {
  return (
    <span
      onClick={onClick}
      style={{
        fontFamily: theme.fonts.open,
        fontWeight: 600,
        fontSize: theme.sizes.xs,
        display: "inline-block",
        padding: "4px 16px",
        marginRight: 16,
        textAlign: "center",
        lineHeight: "24px",
        color: active ? theme.colors.lightningBlack : theme.colors.white,
        backgroundColor: active ? theme.colors.white : theme.colors.blue,
        filter: "drop-shadow(0px 1px 2px rgba(0, 0, 0, 0.08))",
        borderRadius: 100,
        cursor: "pointer",
        transition: "all 0.3s ease",
      }}
    >
      {children}
    </span>
  );
};

interface TabListProps {
  children: React.ReactNode;
}

export const TabList: React.FC<TabListProps> = ({ children }) => {
  return (
    <div style={{ display: "flex", alignItems: "center" }}>{children}</div>
  );
};
