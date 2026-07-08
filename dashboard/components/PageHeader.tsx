"use client";

import React from "react";
import styled from "@emotion/styled";

interface Props {
  title: string;
  description: string;
  action?: React.ReactNode;
}

const Styled = {
  Wrapper: styled.div`
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    margin-bottom: 32px;
    padding-top: 8px;
    animation: fade-in-up 0.4s ease-out both;
  `,
  Title: styled.h1`
    font-family: ${(p) => p.theme.fonts.work};
    font-size: ${(p) => p.theme.sizes.l}px;
    font-weight: 300;
    margin: 0;
    line-height: 32px;
  `,
  Description: styled.p`
    color: ${(p) => p.theme.colors.gray};
    font-size: ${(p) => p.theme.sizes.xs}px;
    margin: 4px 0 0;
    font-weight: 400;
  `,
  Action: styled.div`
    flex-shrink: 0;
    margin-left: 16px;
  `,
};

const PageHeader: React.FC<Props> = ({ title, description, action }) => {
  const { Wrapper, Title, Description, Action } = Styled;
  return (
    <Wrapper>
      <div>
        <Title>{title}</Title>
        <Description>{description}</Description>
      </div>
      {action && <Action>{action}</Action>}
    </Wrapper>
  );
};

export default PageHeader;
