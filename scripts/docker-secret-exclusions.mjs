// Keep this independent of the repository scanner so the guard can be tested
// against unsafe policies without editing the real .dockerignore.
export const requiredSecretExclusions = Object.freeze([
  "**/.env", "**/.env.*", "**/*.key", "**/*.pem",
  "**/*.p12", "**/*.pfx", "**/.ssh", "**/.aws",
]);

export function unsafeSecretExclusions(source) {
  const patterns = source.split(/\r?\n/).map(line => line.trim())
    .filter(line => line && !line.startsWith("#"));
  const lastInclude = patterns.findLastIndex(line => line.startsWith("!"));
  // Deliberately conservative: do not guess whether a later inclusion glob
  // overlaps a protected path. Put the security exclusions after ALL includes.
  // This avoids pretending to implement Docker's entire pattern language.
  return requiredSecretExclusions.filter(pattern => patterns.lastIndexOf(pattern) <= lastInclude);
}
