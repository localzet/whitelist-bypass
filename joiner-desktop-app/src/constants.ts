export const IPC = {
  START: 'joiner:start',
  STOP: 'joiner:stop',
  LOG: 'joiner:log',
  STATUS: 'joiner:status',
  RUNNING: 'joiner:running',
  EGRESS_LIST: 'joiner:egress-list',
  EGRESS_PROBE: 'joiner:egress-probe',
  SERVICE_AUTH_STATUS: 'joiner:service-auth-status',
  SERVICE_AUTH_LOGIN: 'joiner:service-auth-login',
  SERVICE_AUTH_CLEAR: 'joiner:service-auth-clear',
} as const;

export interface EgressDescriptor {
  id: string;
  isDefault: boolean;
}

export interface EgressProbeResult {
  id: string;
  available: boolean;
  latencyMs: number;
  error?: string;
}

export type JoinerPlatform = 'wbstream' | 'telemost' | 'vk' | 'dion';

export interface JoinerSettings {
  platform: JoinerPlatform;
  link: string;
  displayName: string;
  socksPort: number;
  socksUser: string;
  socksPass: string;
  egressId: string;
  tunnelMode: 'video' | 'dc';
  vp8Fps: number;
  vp8Batch: number;
  resources: 'moderate' | 'default' | 'unlimited';
  dns: string;
  noTun: boolean;
  dualTrack: boolean;
  serviceControl: boolean;
  serviceWorkPlatform: JoinerPlatform;
}

export interface ServiceAuthStatus {
  authenticated: boolean;
  clientId: string;
}
