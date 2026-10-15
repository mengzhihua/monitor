import { get, send, post } from '../transport'
import { nodeQuery } from '../selection'
import type { Alarm, SilenceState, AlertRulesSnapshot, AlertRuleChange, AlertRulePreview, AlarmLogEntry, AlarmsResponse } from './types'

/** Alarm data follows selection; rule administration and silences stay local. */
export const alarmsApi = {
  alertRules: (signal?: AbortSignal) => get<AlertRulesSnapshot>('/api/v1/alert_config', signal),
  previewAlertRule: (body: AlertRuleChange, signal?: AbortSignal) => send<AlertRulePreview>('POST', '/api/v1/manage/alert-rules/preview', body, signal),
  changeAlertRule: (body: AlertRuleChange) => post<AlertRulesSnapshot>('/api/v1/manage/alert-rules', body),
  alarms: (signal?: AbortSignal) => get<AlarmsResponse>(`/api/v1/alarms${nodeQuery({ all: 'true' })}`, signal),
  alarmLog: (after = 0, signal?: AbortSignal) => get<AlarmLogEntry[]>(`/api/v1/alarm_log${nodeQuery({ after })}`, signal),
  silence: (body: { all?: boolean; alarm?: string; chart?: string; until?: number; clear?: boolean } = {}) =>
    post<SilenceState>('/api/v1/alarms/silence', body),
  silenceState: () => get<SilenceState>('/api/v1/alarms/silence'),
  closeAlarm: (alarmId: number, comment: string) =>
    post<Alarm>(`/api/v1/alarms/close${nodeQuery({})}`, { alarm_id: alarmId, comment }),
  alarmSummary: () => get<{ status: Record<string, number>; classes?: Record<string, number> }>(`/api/v1/alarm_summary${nodeQuery({})}`),
}
