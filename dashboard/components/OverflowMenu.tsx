"use client";

import React, { useEffect, useRef, useState, useCallback } from "react";
import { createPortal } from "react-dom";
import styled from "@emotion/styled";

export interface MenuItem {
  label: string;
  onClick: () => void;
  danger?: boolean;
}

interface Props {
  items: MenuItem[];
}

const Styled = {
  Trigger: styled.button`
    background: transparent;
    border: none;
    color: ${(p) => p.theme.colors.gray};
    cursor: pointer;
    padding: 4px 8px;
    font-size: 18px;
    line-height: 1;
    border-radius: 4px;
    transition: color 0.15s ease;

    &:hover {
      color: ${(p) => p.theme.colors.white};
    }
  `,
  Panel: styled.div`
    position: absolute;
    transform: translateX(-100%);
    background-color: ${(p) => p.theme.colors.lightNavy};
    border: 1px solid ${(p) => p.theme.colors.lightBlue};
    border-radius: 8px;
    min-width: 140px;
    padding: 4px 0;
    z-index: 9999;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    animation: fade-in-up 0.15s ease-out;
  `,
  Item: styled.button<{ $danger?: boolean }>`
    display: block;
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    padding: 8px 16px;
    font-size: 14px;
    font-family: ${(p) => p.theme.fonts.open};
    cursor: pointer;
    color: ${(p) =>
      p.$danger ? p.theme.colors.lightningRed : p.theme.colors.offWhite};
    transition: background-color 0.1s ease;

    &:hover {
      background-color: ${(p) => p.theme.colors.overlay};
    }
  `,
};

const OverflowMenu: React.FC<Props> = ({ items }) => {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const updatePos = useCallback(() => {
    if (!triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    setPos({
      top: rect.bottom + window.scrollY + 4,
      left: rect.right + window.scrollX,
    });
  }, []);

  const toggle = useCallback(() => setOpen((o) => !o), []);

  const handleItemClick = useCallback(
    (onClick: () => void) => () => {
      setOpen(false);
      onClick();
    },
    [],
  );

  useEffect(() => {
    if (!open) return;
    updatePos();

    function handleClick(e: MouseEvent) {
      if (
        triggerRef.current?.contains(e.target as Node) ||
        menuRef.current?.contains(e.target as Node)
      )
        return;
      setOpen(false);
    }
    function handleScroll() {
      updatePos();
    }
    document.addEventListener("mousedown", handleClick);
    window.addEventListener("scroll", handleScroll, true);
    return () => {
      document.removeEventListener("mousedown", handleClick);
      window.removeEventListener("scroll", handleScroll, true);
    };
  }, [open, updatePos]);

  const { Trigger, Panel, Item } = Styled;
  return (
    <>
      <Trigger ref={triggerRef} onClick={toggle}>
        &middot;&middot;&middot;
      </Trigger>

      {open &&
        pos &&
        createPortal(
          <Panel ref={menuRef} style={{ top: pos.top, left: pos.left }}>
            {items.map((item) => (
              <Item
                key={item.label}
                $danger={item.danger}
                onClick={handleItemClick(item.onClick)}
              >
                {item.label}
              </Item>
            ))}
          </Panel>,
          document.body,
        )}
    </>
  );
};

export default OverflowMenu;
