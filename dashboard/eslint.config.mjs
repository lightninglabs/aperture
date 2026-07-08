import nextConfig from "eslint-config-next";
import prettierConfig from "eslint-config-prettier";

// Disable React Compiler lint rules — the compiler is not enabled in this
// project, so these rules produce false positives for valid useCallback and
// setState-in-effect patterns.
const config = [
  ...nextConfig,
  prettierConfig,
  {
    rules: {
      "react-hooks/preserve-manual-memoization": "off",
      "react-hooks/set-state-in-effect": "off",
      "import/no-anonymous-default-export": "off",
    },
  },
];

export default config;
