"use client";

import React, { useCallback } from "react";
import styled from "@emotion/styled";

interface Props {
  message: string;
  onRetry?: () => void;
}

const Styled = {
  Banner: styled.div`
    background-color: rgba(239, 68, 68, 0.1);
    border: 1px solid ${(p) => p.theme.colors.lightningRed};
    border-radius: 8px;
    padding: 14px 20px;
    margin-bottom: 16px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    animation: fade-in-up 0.3s ease-out;
  `,
  Message: styled.span`
    color: ${(p) => p.theme.colors.lightningRed};
    font-size: 14px;
    font-weight: 500;
  `,
  RetryBtn: styled.button`
    background: transparent;
    border: 1px solid ${(p) => p.theme.colors.lightningRed};
    color: ${(p) => p.theme.colors.lightningRed};
    padding: 4px 12px;
    border-radius: 4px;
    font-size: 13px;
    font-family: ${(p) => p.theme.fonts.open};
    font-weight: 600;
    cursor: pointer;
    white-space: nowrap;
    transition: background-color 0.15s;

    &:hover {
      background-color: rgba(239, 68, 68, 0.15);
    }
  `,
};

const ErrorBanner: React.FC<Props> = ({ message, onRetry }) => {
  const { Banner, Message, RetryBtn } = Styled;
  return (
    <Banner>
      <Message>{message}</Message>
      {onRetry && <RetryBtn onClick={onRetry}>Retry</RetryBtn>}
    </Banner>
  );
};

export default ErrorBanner;
