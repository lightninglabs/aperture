"use client";

import React, { useState, useRef, useEffect, useCallback } from "react";
import { createPortal } from "react-dom";
import styled from "@emotion/styled";

interface Props {
  text: string;
  children?: React.ReactNode;
}

const Styled = {
  Trigger: styled.span`
    position: relative;
    display: inline-flex;
    align-items: center;
  `,
  Icon: styled.span`
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    border: 1px solid ${(p) => p.theme.colors.lightBlue};
    font-size: 10px;
    font-weight: 700;
    color: ${(p) => p.theme.colors.gray};
    cursor: help;
    margin-left: 6px;
    flex-shrink: 0;
    line-height: 1;
  `,
  Bubble: styled.div`
    position: absolute;
    transform: translate(-50%, -100%);
    background-color: ${(p) => p.theme.colors.lightNavy};
    border: 1px solid ${(p) => p.theme.colors.lightBlue};
    border-radius: 6px;
    padding: 8px 12px;
    font-size: 12px;
    font-weight: 400;
    color: ${(p) => p.theme.colors.offWhite};
    white-space: normal;
    width: 240px;
    line-height: 1.5;
    z-index: 9999;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
    pointer-events: none;
    text-transform: none;
    letter-spacing: normal;
  `,
  Arrow: styled.span`
    position: absolute;
    bottom: -5px;
    left: 50%;
    transform: translateX(-50%) rotate(45deg);
    width: 8px;
    height: 8px;
    background-color: ${(p) => p.theme.colors.lightNavy};
    border-right: 1px solid ${(p) => p.theme.colors.lightBlue};
    border-bottom: 1px solid ${(p) => p.theme.colors.lightBlue};
  `,
};

const Tooltip: React.FC<Props> = ({ text, children }) => {
  const [show, setShow] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const triggerRef = useRef<HTMLSpanElement>(null);

  const updatePos = useCallback(() => {
    if (!triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    setPos({
      top: rect.top + window.scrollY - 8,
      left: rect.left + window.scrollX + rect.width / 2,
    });
  }, []);

  const handleShow = useCallback(() => setShow(true), []);
  const handleHide = useCallback(() => setShow(false), []);

  useEffect(() => {
    if (!show) return;
    updatePos();
  }, [show, updatePos]);

  const { Trigger, Icon, Bubble, Arrow } = Styled;
  return (
    <>
      <Trigger
        ref={triggerRef}
        onMouseEnter={handleShow}
        onMouseLeave={handleHide}
      >
        {children ?? <Icon>?</Icon>}
      </Trigger>
      {show &&
        pos &&
        createPortal(
          <Bubble style={{ top: pos.top, left: pos.left }}>
            {text}
            <Arrow />
          </Bubble>,
          document.body
        )}
    </>
  );
};

export default Tooltip;
