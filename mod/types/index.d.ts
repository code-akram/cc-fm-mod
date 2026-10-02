// What the cc-fm player reports (GET /v1/status and the stream's S lines),
// plus `offline` when no player answers on the socket.
export type FmStatus = {
  state: 'offline' | 'stopped' | 'connecting' | 'playing' | 'retrying'
  source?: string
  title?: string
  volume?: number
  output?: string
  error?: string
  listeners?: number
}

declare module 'claude-code' {
  interface PluginState {
    'cc-fm-mod': { status: FmStatus; isKeysOpen: boolean; isControlling: boolean }
  }
}
