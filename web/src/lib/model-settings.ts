export interface ProviderConfig {
  name: string;
  configJSON: string;
  baseRevision: string;
  storage: 'legacy_plaintext';
}
export interface PreferenceEntry {
  modelId?: string;
  reasoningEffort?: string;
  contextWindow?: number;
}
export interface Preferences {
  noProject: PreferenceEntry;
  byProject: Record<string, PreferenceEntry>;
  revision: string;
}
export interface ModelSession {
  sessionId: string;
  status: string;
  currentModel: string | null;
  runtimeConfig?: { model?: string; reasoningEffort?: string; contextWindow?: number };
  runtimeRestartRequired: boolean | null;
  modelConfigRevision: number | null;
  lastActiveAt: string;
}
export interface WakerModels {
  wakerId: string;
  preferences: Preferences;
  sessions: ModelSession[];
  sessionsError?: string;
}
export interface SaveResult {
  status: 'verified' | 'saved_unverified' | 'changed_after_save' | 'unknown' | 'readback_mismatch';
  current?: ProviderConfig;
  preferences?: Preferences;
}
export const saveMessages: Record<SaveResult['status'], string> = {
  verified: '已保存并核对回读结果。',
  saved_unverified: '写入已返回成功，但回读失败。请重新读取后核对。',
  changed_after_save: '写入后配置又发生了变化，请重新读取后核对。',
  unknown: '未能确认写入结果，请重新读取后核对。',
  readback_mismatch: '回读结果与提交值不一致，请重新读取后核对。',
};
export function preferencePayload(
  wakerId: string,
  projectId: string,
  model: string,
  revision: string,
  effort: string,
  context: string,
) {
  if (!model.trim()) throw new Error('请选择模型。');
  const value = context === '' ? undefined : Number(context);
  if (value !== undefined && (!Number.isSafeInteger(value) || value <= 0))
    throw new Error('上下文窗口需要填写正整数。');
  return {
    wakerId,
    projectId,
    model: model.trim(),
    baseRevision: revision,
    ...(effort.trim() ? { reasoningEffort: effort.trim() } : {}),
    ...(value === undefined ? {} : { contextWindow: value }),
    confirm: '/api/waker/models/update',
  };
}
