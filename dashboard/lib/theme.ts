/**
 * Theme tokens matching Lightning Terminal Web design system.
 * Used as a centralized reference for all component styling.
 */

export const theme = {
  fonts: {
    open: "'Open Sans', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
    work: "'Work Sans', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif",
  },
  sizes: {
    xxs: 11,
    xs: 14,
    s: 16,
    m: 18,
    l: 22,
    xl: 27,
    xxl: 32,
  },
  colors: {
    blue: "#252f4a",
    gray: "#848a99",
    white: "#ffffff",
    offWhite: "#f5f5f5",
    gold: "#efa00b",
    purple: "#5D5FEF",
    darkPurple: "#5040ED",
    lightningBlue: "#3B82F6",
    overlay: "rgba(245,245,245,0.04)",
    lightningGray: "#B9BDC5",
    lightningYellow: "#F59E0B",
    lightningGreen: "#10B981",
    lightningOrange: "#FF6610",
    lightningRed: "#EF4444",
    lightningBlack: "#101727",
    lightBlue: "#384770",
    lightNavy: "#1D253A",
    paleBlue: "#2E3A5C",
    lightPurple: "#A5A6F6",
  },
} as const;

export type ThemeColors = typeof theme.colors;
