import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["out/**", "build/**", "public/js/**", "node_modules/**", "**/.tmp-*"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  reactHooks.configs.flat["recommended-latest"],
  {
    files: ["**/*.{ts,tsx,mts,mjs}"],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      // React 19 strictness: setState in useEffect is standard for data fetching
      "react-hooks/set-state-in-effect": "off",
      // React 19 strictness: accessing hook-returned refs during render is intentional in our hooks
      "react-hooks/refs": "off",
      // Enforce type safety: ban `as any` casts
      "@typescript-eslint/no-explicit-any": "error",
    },
  }
);
