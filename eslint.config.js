import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

// Shared quality rules for both JS and TS
const sharedRules = {
  "eqeqeq": ["warn", "smart"],
  "no-var": "warn",
  "prefer-const": "warn",
  "no-debugger": "error",
  "no-alert": "error",
  "no-eval": "error",
  "no-implied-eval": "error",
  "no-new-func": "error",
  "no-throw-literal": "warn",
  "no-self-compare": "error",
  "no-template-curly-in-string": "warn",
  "no-unmodified-loop-condition": "warn",
  "no-constant-binary-expression": "error",
};

export default [
  { ignores: ["web/**/*.cjs", "static/transit/transit-map.js"] },
  js.configs.recommended,
  {
    files: ["static/**/*.js"],
    languageOptions: {
      ecmaVersion: 2020,
      sourceType: "script",
      globals: {
        ...globals.browser,
        L: "readonly",               // Leaflet
        d3: "readonly",              // D3.js
        d3Sankey: "readonly",        // d3-sankey
        ThemeColors: "readonly",
        ThunderMapTiles: "readonly", // static/js/map-tiles.js
        isTouch: "readonly",
        isMobile: "readonly",
      },
    },
    rules: {
      ...sharedRules,
      "no-unused-vars": ["warn", { argsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" }],
      "no-undef": "error",
      "no-implicit-globals": "error",
    },
  },
  ...tseslint.configs.recommended.map(config => ({
    ...config,
    files: ["web/**/*.ts"],
  })),
  {
    files: ["web/**/*.ts"],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      ...sharedRules,
      "@typescript-eslint/no-explicit-any": "error",
      "no-console": ["warn", { allow: ["warn", "error"] }],
      "@typescript-eslint/no-unused-vars": ["warn", { argsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" }],
    },
  },
];
