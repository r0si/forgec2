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
  agent_id?: string;
}
