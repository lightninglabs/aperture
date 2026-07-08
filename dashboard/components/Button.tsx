"use client";

import React from "react";
import styled from "@emotion/styled";

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "tertiary" | "ghost" | "danger";
  compact?: boolean;
  block?: boolean;
}

const Styled = {
  Button: styled.button<{
    $variant: string;
    $compact?: boolean;
    $block?: boolean;
  }>`
    font-family: ${(p) => p.theme.fonts.open};
    font-size: ${(p) => (p.$compact ? "13px" : `${p.theme.sizes.s}px`)};
    font-weight: 600;
    min-width: ${(p) => (p.$compact ? "0" : p.$block ? "100%" : "120px")};
    height: ${(p) => (p.$compact ? "auto" : "40px")};
    padding: ${(p) => (p.$compact ? "6px 16px" : "8px 24px")};
    text-align: center;
    border-radius: 6px;
    cursor: pointer;
    transition: all 0.15s ease;

    /* primary */
    ${(p) =>
      p.$variant === "primary" &&
      `
      color: ${p.theme.colors.white};
      background-color: ${p.theme.colors.purple};
      border: none;
      &:hover:not(:disabled) {
        background-color: ${p.theme.colors.darkPurple};
        box-shadow: 0 0 12px rgba(93, 95, 239, 0.3);
      }
    `}

    /* secondary */
    ${(p) =>
      p.$variant === "secondary" &&
      `
      color: ${p.theme.colors.lightningBlack};
      background-color: ${p.theme.colors.white};
      border: none;
      &:hover:not(:disabled) {
        background-color: ${p.theme.colors.lightningGray};
      }
    `}

    /* tertiary */
    ${(p) =>
      p.$variant === "tertiary" &&
      `
      color: ${p.theme.colors.white};
      background-color: ${p.theme.colors.paleBlue};
      border: none;
      &:hover:not(:disabled) {
        background-color: ${p.theme.colors.lightBlue};
      }
    `}

    /* ghost */
    ${(p) =>
      p.$variant === "ghost" &&
      `
      color: ${p.theme.colors.lightningGray};
      background-color: transparent;
      border: 1px solid ${p.theme.colors.lightBlue};
      &:hover:not(:disabled) {
        background-color: ${p.theme.colors.overlay};
        border-color: ${p.theme.colors.gray};
      }
    `}

    /* danger */
    ${(p) =>
      p.$variant === "danger" &&
      `
      color: ${p.theme.colors.white};
      background-color: ${p.theme.colors.lightningRed};
      border: none;
      &:hover:not(:disabled) {
        background-color: ${p.theme.colors.lightningRed}cc;
      }
    `}

    &:active:not(:disabled) {
      transform: scale(0.97);
    }

    &:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }
  `,
};

const Button: React.FC<ButtonProps> = ({
  variant = "primary",
  compact,
  block,
  children,
  ...props
}) => (
  <Styled.Button
    $variant={variant}
    $compact={compact}
    $block={block}
    {...props}
  >
    {children}
  </Styled.Button>
);

export default Button;
