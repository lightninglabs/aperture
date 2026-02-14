"use client";

import React, { ReactNode } from "react";
import styled from "@emotion/styled";

interface Props {
  title: string;
  text?: ReactNode;
  suffix?: ReactNode;
  subText?: ReactNode;
  children?: ReactNode;
}

const Styled = {
  Tile: styled.div`
    min-height: 80px;
    padding: 24px;
    background-color: ${(p) => p.theme.colors.lightNavy};
    border: 1px solid ${(p) => p.theme.colors.lightningBlack};
    border-radius: 8px;
    transition: border-color 0.2s ease, transform 0.2s ease;

    &:hover {
      border-color: ${(p) => p.theme.colors.lightBlue};
      transform: translateY(-1px);
    }
  `,
  Title: styled.div`
    color: ${(p) => p.theme.colors.gray};
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    line-height: 16px;
  `,
  Value: styled.div`
    font-size: ${(p) => p.theme.sizes.xl}px;
    line-height: 32px;
    margin-top: 12px;
    color: ${(p) => p.theme.colors.white};
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  `,
  Suffix: styled.span`
    color: ${(p) => p.theme.colors.gray};
    font-size: ${(p) => p.theme.sizes.xs}px;
    font-weight: 400;
    margin-left: 4px;
  `,
  SubText: styled.div`
    color: ${(p) => p.theme.colors.gray};
    font-size: ${(p) => p.theme.sizes.xs}px;
    font-weight: 400;
    margin-top: 4px;
  `,
};

const StatTile: React.FC<Props> = ({
  title,
  text,
  suffix,
  subText,
  children,
}) => {
  const { Tile, Title, Value, Suffix, SubText } = Styled;
  return (
    <Tile>
      <Title>{title}</Title>
      {text ? (
        <>
          <Value>
            {text}
            {suffix && <Suffix>{suffix}</Suffix>}
          </Value>
          {subText && <SubText>{subText}</SubText>}
        </>
      ) : (
        children
      )}
    </Tile>
  );
};

export default StatTile;
