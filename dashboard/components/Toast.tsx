"use client";

import React, { useCallback, useEffect, useState } from "react";
import styled from "@emotion/styled";

export interface ToastItem {
  id: string;
  message: string;
  type: "success" | "error";
}

let toastListeners: Array<(t: ToastItem) => void> = [];

export function toast(
  message: string,
  type: "success" | "error" = "success",
) {
  const item: ToastItem = { id: crypto.randomUUID(), message, type };
  toastListeners.forEach((fn) => fn(item));
}

const Styled = {
  Container: styled.div`
    position: fixed;
    bottom: 24px;
    right: 24px;
    z-index: 1000;
    display: flex;
    flex-direction: column;
    gap: 8px;
  `,
  Toast: styled.div<{ $type: "success" | "error" }>`
    padding: 12px 20px;
    border-radius: 8px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    font-weight: 500;
    color: ${(p) => p.theme.colors.white};
    background-color: ${(p) =>
      p.$type === "success"
        ? "rgba(16,185,129,0.15)"
        : "rgba(239,68,68,0.15)"};
    border: 1px solid
      ${(p) =>
        p.$type === "success"
          ? p.theme.colors.lightningGreen
          : p.theme.colors.lightningRed};
    backdrop-filter: blur(12px);
    min-width: 240px;
    animation: toast-in 0.2s ease-out;
  `,
  Icon: styled.span<{ $type: "success" | "error" }>`
    color: ${(p) =>
      p.$type === "success"
        ? p.theme.colors.lightningGreen
        : p.theme.colors.lightningRed};
    margin-right: 8px;
    font-weight: 600;
  `,
};

export const ToastContainer: React.FC = () => {
  const [items, setItems] = useState<ToastItem[]>([]);

  const add = useCallback((item: ToastItem) => {
    setItems((prev) => [...prev, item]);
    setTimeout(() => {
      setItems((prev) => prev.filter((i) => i.id !== item.id));
    }, 3000);
  }, []);

  useEffect(() => {
    toastListeners.push(add);
    return () => {
      toastListeners = toastListeners.filter((fn) => fn !== add);
    };
  }, [add]);

  if (items.length === 0) return null;

  const { Container, Toast, Icon } = Styled;
  return (
    <Container>
      {items.map((item) => (
        <Toast key={item.id} $type={item.type}>
          <Icon $type={item.type}>
            {item.type === "success" ? "\u2713" : "\u2717"}
          </Icon>
          {item.message}
        </Toast>
      ))}
    </Container>
  );
};
