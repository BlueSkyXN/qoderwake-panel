export type Role = 'admin' | 'maintainer' | 'observer' | 'viewer';
export interface Identity {
  ok: boolean;
  level: Role;
  csrf: string;
  version: string;
  userId: string | null;
  username: string | null;
  displayName: string | null;
  capabilities: string[];
  wakers: string[];
  transportSecure: boolean;
}
export interface User {
  id: string;
  username: string;
  displayName: string;
  role: Exclude<Role, 'viewer'>;
  wakers: string[];
  enabled: boolean;
  revision: number;
  createdAt: number;
  updatedAt: number;
}
export interface Model {
  id: string;
  name: string;
  provider?: string;
}
export interface Waker {
  id: string;
  name: string;
  preference?: string;
}
export interface PanelState {
  version: string;
  daemon: string;
  error?: string;
  models: Model[];
  wakers: Waker[];
  providers: Provider[];
  providerRevision: string | null;
  pluginWritesEnabled: boolean;
  telemetry?: boolean | null;
  guard?: boolean | null;
  whitelist?: { ip: string; label: string }[];
  [key: string]: unknown;
}
export interface Provider {
  name: string;
  baseUrl: string;
  models: string[];
  displayNames: string[];
  panelManaged: boolean;
  keyConfigured: boolean;
  readOnlyReason?: string;
}
export interface Skill {
  skillId: string;
  name: string;
  description?: string;
  enabled: boolean;
  source?: string;
  mutableByAgent: boolean;
  pinned: boolean;
  currentVersionId: string | null;
  updatedAt?: string;
}
export interface SkillContent {
  skill: Skill;
  content: string;
  contentAvailable: boolean;
  editable: boolean;
  readOnlyReason: string | null;
}
export interface SkillVersion {
  versionId: string;
  createdAt?: string;
  updatedAt?: string;
  reason?: string;
  source?: string;
}
export interface WakerDetail {
  id?: string;
  agentId?: string;
  name: string;
  description: string;
  configRevision: string;
  skills: Skill[];
}
export interface AuditRow {
  ts: string;
  principal: string;
  displayName?: string;
  role: Role;
  action: string;
  target: string;
  status: number;
  ok: boolean | number;
  wakerId?: string;
  beforeRevision?: string;
  afterRevision?: string;
}
export type JSONValue =
  string | number | boolean | null | JSONValue[] | { [key: string]: JSONValue };
