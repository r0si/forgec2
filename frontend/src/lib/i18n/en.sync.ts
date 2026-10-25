// Synchronous dictionary for dev and test.
//
// In a production build Vite aliases `@i18n-sync` to en.sync.prod.ts, so this
// module (and therefore the full dictionary) is not statically reachable from
// the entry chunk — the provider loads it through a dynamic import instead.
export { en } from "./en";
