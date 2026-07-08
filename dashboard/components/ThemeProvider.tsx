"use client";

import React from "react";
import { ThemeProvider as EmotionThemeProvider } from "@emotion/react";
import { theme } from "@/lib/theme";

const ThemeProvider: React.FC<{ children: React.ReactNode }> = ({
  children,
}) => <EmotionThemeProvider theme={theme}>{children}</EmotionThemeProvider>;

export default ThemeProvider;
