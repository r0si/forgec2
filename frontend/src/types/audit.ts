export interface AuditLog {
  id?: string;
  timestamp?: string;
  username?: string;
  action?: string;
  resource?: string;
  target?: string;
  status?: string;
  /** Whether the action succeeded; the server stores it and the failed-result filter is a query over it. */
  success?: boolean;
  details?: string;
  ip?: string;
  severity?: string;
  /** The name of the beacon the row belongs to (the server resolves it). */
  agent_hostname?: string;
  agent_id?: string;
}
