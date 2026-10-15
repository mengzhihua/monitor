
export interface AgentUser { name: string; token: string; role: string }
export interface AgentTarget { name: string; fields: string; url: string; address: string; listen: string; user: string }
export interface AgentSNMPTarget {
  name: string; address: string; profile: '' | 'network' | 'ups' | 'printer' | 'host'
  version: '' | '1' | '2c' | '3'; community?: string; user: string
  auth_protocol: string; auth_passphrase?: string; priv_protocol: string; priv_passphrase?: string; timeout: string
}
export interface AgentRedfishTarget {
  name: string; url: string; user: string; password?: string; timeout: string
  tls?: { ca_file?: string; insecure_skip_verify?: boolean }
}
export interface AgentRole { name: string; channels: string[] }
export interface AgentNotify {
  webhook_url: string; slack_webhook_url: string; slack_channel: string
  dingtalk_webhook_url: string; wecom_webhook_url: string
  feishu_webhook_url: string; feishu_webhook_url_env: string; feishu_secret: string; feishu_secret_env: string
  email_server: string; email_from: string; email_to: string[]
  telegram_token: string; telegram_chat_id: string; discord_webhook_url: string
  ntfy_url: string; ntfy_topic: string; ntfy_topic_env: string
  gotify_url: string; gotify_token: string; gotify_token_env: string
  bark_url: string; bark_device_key: string; bark_device_key_env: string
  roles: AgentRole[]
}
export interface AgentVisual {
  mode: 'agent' | 'hub'; hostname: string; update_every: number; data_dir: string
  web_enabled: 'default' | 'on' | 'off'; listen: string; allow_from: string[]; ticket_webhook: string
  users: AgentUser[]; collectors_enabled: string[]; collectors_disabled: string[]
  health_enabled: 'default' | 'on' | 'off'; health_silent: boolean
  stream_enabled: boolean; stream_destinations: string[]; stream_api_key: string; stream_protocol: string
  hub_api_keys: string[]; hub_peers: string[]; hub_storage: string; hub_space: string; hub_room: string
  notify: AgentNotify; targets: AgentTarget[]
  snmp_targets?: AgentSNMPTarget[]; redfish_targets?: AgentRedfishTarget[]
  snmp_legacy?: boolean; redfish_legacy?: boolean
}
export interface ManageConfig {
  writable?: boolean; path: string; yaml: string; updated: number; size?: number
  backup?: string; form?: AgentVisual; form_error?: string
}
