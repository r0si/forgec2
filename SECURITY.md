# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 2.7.x   | Yes       |
| 2.6.x   | Security fixes only |
| < 2.6   | No        |

## Reporting a Vulnerability

If you discover a security vulnerability in ForgeC2, please report it responsibly.

**Do not** open public GitHub issues for security vulnerabilities.

Use GitHub's private advisory form:
<https://github.com/r0si/forgec2/security/advisories/new>

Alternatively, email security details to: **security@forgec2.dev** (or the maintainer's private contact).

### What to include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response timeline

- **Acknowledgment**: within 48 hours
- **Initial assessment**: within 1 week
- **Fix or mitigation**: depends on severity, typically 2-4 weeks

## Security Features

ForgeC2 includes the following built-in security measures:

### Authentication & Authorization

- JWT + bcrypt password hashing
- Session cookie `forgec2_session` is HttpOnly + Secure (when TLS enabled) + `SameSite=Lax`
- CSRF double-submit cookie protection (`forgec2_csrf` + `X-CSRF-Token` header, HMAC-SHA256 of the session bound with independent `crypto.csrf_key`). Note: `forgec2_csrf` is intentionally readable by JS (`HttpOnly=false`) so the frontend can echo it; the session cookie itself stays HttpOnly.
- Per-route RBAC permission system (agents, listeners, settings, plugins, etc.)
- IP-based login lockout with progressive delay

### Transport Security

- TLS enforcement option (`require_tls_for_auth`)
- Configurable CORS origin allowlist (`allowed_origins`)
- Cookie domain restriction (`cookie_domain`)
- CSP, X-Content-Type-Options, X-Frame-Options, Referrer-Policy, Permissions-Policy headers

### Data Protection

- AES-GCM encrypted automatic database backups
- ECDH session key exchange for agent communications
- Loot encryption at rest
- Path traversal prevention on all file operations
- Request body size limits (2MB max JSON)

### Operational Security

- OPSEC guard pre-flight rule engine
- Circuit breaker for listener health monitoring
- Audit logging for all sensitive operations
- Auto-generated random admin password and JWT secret on first run
- Login lockout with cleanup goroutine

## Hardening Checklist

- Use a reverse proxy (Nginx/Caddy) to terminate TLS in production
- Set `allowed_origins` to restrict WebSocket/CORS access
- Rotate JWT secret via `POST /settings/jwt/regenerate`
- Review audit logs regularly (`AuditLog` table)
- Use `VACUUM` and DB backups via Settings UI
- Set `require_tls_for_auth: true` in production

## Known Limitations

- Multi-tenant isolation is enforced per-entity (tenant_id scoping with
  regression tests), but tenant 0 remains a legacy/unscoped global view —
  treat it as privileged.
- SQLite database (not suitable for high-concurrency production without careful tuning)
- No built-in WAF or DDoS protection (use external reverse proxy)
