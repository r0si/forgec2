// Production stand-in for en.sync.ts.
//
// A production build aliases `@i18n-sync` here, so the full dictionary is not
// part of the first-paint closure; the provider loads it with import("./en")
// and merges it in once it arrives. Keep this module free of side effects.
export const en: Record<string, string> | undefined = undefined;
