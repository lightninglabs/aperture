"use client";

import "./globals.css";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useMemo, useCallback } from "react";
import styled from "@emotion/styled";
import ThemeProvider from "@/components/ThemeProvider";
import { ToastContainer } from "@/components/Toast";
import { useInfo } from "@/lib/api";

const baseNavItems: { href: string; label: string }[] = [
  { href: "/", label: "Dashboard" },
  { href: "/services", label: "Services" },
  { href: "/transactions", label: "Transactions" },
];

const sessionsNavItem = { href: "/sessions", label: "Sessions" };

const sfp = {
  shouldForwardProp: (prop: string) => !prop.startsWith("$"),
};

const Styled = {
  Body: styled.div`
    background: ${(p) => p.theme.colors.lightningBlack};
    color: ${(p) => p.theme.colors.white};
    font-family: ${(p) => p.theme.fonts.open};
    font-size: ${(p) => p.theme.sizes.m}px;
    font-weight: 500;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  `,
  Nav: styled.nav`
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 100;
    display: flex;
    align-items: center;
    padding: 16px 24px;
    background: ${(p) => p.theme.colors.lightningBlack};
    border-bottom: 1px solid ${(p) => p.theme.colors.blue};
  `,
  Brand: styled(Link)`
    flex: 1;
    display: flex;
    align-items: center;
    gap: 12px;
    font-family: ${(p) => p.theme.fonts.work};
    font-size: 20px;
    font-weight: 300;
    line-height: 24px;
    text-transform: uppercase;
    letter-spacing: 2px;
    color: ${(p) => p.theme.colors.white};
    text-decoration: none;
  `,
  NetworkBadge: styled.span<{ $network?: string }>`
    font-size: 11px;
    font-family: ${(p) => p.theme.fonts.open};
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 1px;
    padding: 2px 8px;
    border-radius: 4px;
    background-color: ${(p) =>
      p.$network === "mainnet"
        ? "rgba(16,185,129,0.15)"
        : "rgba(245,158,11,0.15)"};
    color: ${(p) =>
      p.$network === "mainnet"
        ? p.theme.colors.lightningGreen
        : p.theme.colors.lightningYellow};
    border: 1px solid
      ${(p) =>
        p.$network === "mainnet"
          ? p.theme.colors.lightningGreen
          : p.theme.colors.lightningYellow};
  `,
  NavLinks: styled.div`
    flex: 2;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 4px;
  `,
  NavLink: styled(Link, sfp)<{ $active?: boolean }>`
    display: flex;
    align-items: center;
    padding: 6px 14px;
    border-radius: 6px;
    font-size: 14px;
    font-weight: ${(p) => (p.$active ? 600 : 500)};
    text-decoration: none;
    line-height: 24px;
    color: ${(p) => (p.$active ? p.theme.colors.white : p.theme.colors.gray)};
    background-color: ${(p) =>
      p.$active ? p.theme.colors.overlay : "transparent"};
    transition: all 0.2s ease;

    &:hover {
      color: ${(p) => p.theme.colors.offWhite};
      background-color: ${(p) => p.theme.colors.overlay};
    }
  `,
  Status: styled.div`
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    font-size: 13px;
    color: ${(p) => p.theme.colors.gray};
  `,
  StatusDot: styled("span", sfp)<{ $connected?: boolean }>`
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: ${(p) =>
      p.$connected
        ? p.theme.colors.lightningGreen
        : p.theme.colors.lightningRed};
    box-shadow: 0 0 6px
      ${(p) =>
        p.$connected
          ? p.theme.colors.lightningGreen
          : p.theme.colors.lightningRed};
  `,
  Main: styled.main`
    flex: 1;
    padding-top: 96px;
    padding-left: 24px;
    padding-right: 24px;
    padding-bottom: 48px;
    max-width: 1140px;
    width: 100%;
    margin: 0 auto;
  `,
};

function LayoutInner({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { data: info, error: infoError, isLoading: infoLoading } = useInfo();

  const connected = !infoError && !infoLoading && !!info;
  const network = info?.network || "";

  const isActive = useCallback(
    (href: string) =>
      href === "/" ? pathname === "/" : pathname.startsWith(href),
    [pathname]
  );

  // Only show the Sessions tab when the server has MPP prepaid sessions
  // enabled — otherwise /sessions returns 501 and the page would render
  // an empty state for no reason.
  const navItems = useMemo(() => {
    const items = [...baseNavItems];
    if (info?.mpp_enabled && info?.sessions_enabled) {
      items.push(sessionsNavItem);
    }
    return items;
  }, [info?.mpp_enabled, info?.sessions_enabled]);

  const links = useMemo(
    () =>
      navItems.map((item) => (
        <Styled.NavLink
          key={item.href}
          href={item.href}
          $active={isActive(item.href)}
        >
          {item.label}
        </Styled.NavLink>
      )),
    [isActive, navItems]
  );

  const { Body, Nav, Brand, NetworkBadge, NavLinks, Status, StatusDot, Main } =
    Styled;

  return (
    <Body>
      <Nav>
        <Brand href="/">
          APERTURE
          {network && <NetworkBadge $network={network}>{network}</NetworkBadge>}
        </Brand>
        <NavLinks>{links}</NavLinks>
        <Status>
          <StatusDot $connected={connected} />
          {infoLoading
            ? "Connecting..."
            : connected
              ? "Connected"
              : "Disconnected"}
        </Status>
      </Nav>
      <Main>{children}</Main>
      <ToastContainer />
    </Body>
  );
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body style={{ margin: 0 }}>
        <ThemeProvider>
          <LayoutInner>{children}</LayoutInner>
        </ThemeProvider>
      </body>
    </html>
  );
}
