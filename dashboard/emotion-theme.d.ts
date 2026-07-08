import "@emotion/react";

declare module "@emotion/react" {
  export interface Theme {
    fonts: {
      open: string;
      work: string;
    };
    sizes: {
      xxs: number;
      xs: number;
      s: number;
      m: number;
      l: number;
      xl: number;
      xxl: number;
    };
    colors: {
      blue: string;
      gray: string;
      white: string;
      offWhite: string;
      gold: string;
      purple: string;
      darkPurple: string;
      lightningBlue: string;
      overlay: string;
      lightningGray: string;
      lightningYellow: string;
      lightningGreen: string;
      lightningOrange: string;
      lightningRed: string;
      lightningBlack: string;
      lightBlue: string;
      lightNavy: string;
      paleBlue: string;
      lightPurple: string;
    };
  }
}
