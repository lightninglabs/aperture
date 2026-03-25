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
    padding: 60px 24px;
    text-align: center;
  `,
  Title: styled.div`
    font-size: ${(p) => p.theme.sizes.s}px;
    font-weight: 600;
    color: ${(p) => p.theme.colors.offWhite};
    margin-bottom: 8px;
  `,
  Description: styled.div`
    font-size: ${(p) => p.theme.sizes.xs}px;
    color: ${(p) => p.theme.colors.gray};
    max-width: 360px;
    margin: 0 auto;
    line-height: 22px;
  `,
  Action: styled.div`
    margin-top: 20px;
  `,
};

const EmptyState: React.FC<Props> = ({ title, description, action }) => {
  const { Wrapper, Title, Description, Action } = Styled;
  return (
    <Wrapper>
      <Title>{title}</Title>
      <Description>{description}</Description>
      {action && <Action>{action}</Action>}
    </Wrapper>
  );
};

export default EmptyState;
