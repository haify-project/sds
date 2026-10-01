import { type ApiResponse, type RequestFn } from './client';

// A channel's kind is the message envelope the far end demands. It is not
// cosmetic: a chat service refuses anything else, and Feishu, WeCom and
// DingTalk do it inside an HTTP 200 — so the wrong kind delivers nothing and
// looks fine. Hence the Test button.
export interface NotifyChannel {
  name: string;
  kind: string;
  url: string;
  minSeverity?: string;
  types?: string[];
  headers?: Record<string, string>;
  enabled?: boolean;
  hasSecret?: boolean;
  createdAt?: string;
  updatedAt?: string;
}

export interface NotifyChannelInput {
  name: string;
  kind: string;
  url: string;
  minSeverity?: string;
  types?: string[];
  headers?: Record<string, string>;
  enabled: boolean;
  secret?: string;
  clearSecret?: boolean;
}

export const notifyApi = (request: RequestFn) => ({
  // Notification channels. The signing secret is write-only end to end: the
  // list never returns it, so saving without one keeps whatever is stored and
  // clearSecret is the only way to remove it. Sending the empty string back
  // from a form the user did not touch would otherwise unsign the channel.
  listNotifyChannels: () =>
    request<{
      success: boolean;
      message: string;
      channels?: NotifyChannel[];
      kinds?: string[];
    }>('/notify-channels'),

  saveNotifyChannel: (data: NotifyChannelInput) =>
    request<{ success: boolean; message: string; channel?: NotifyChannel }>(
      '/notify-channels',
      { method: 'POST', body: JSON.stringify(data) }
    ),

  deleteNotifyChannel: (name: string) =>
    request<ApiResponse>(
      `/notify-channels/${encodeURIComponent(name)}`,
      { method: 'DELETE' }
    ),

  testNotifyChannel: (name: string) =>
    request<ApiResponse>(
      `/notify-channels/${encodeURIComponent(name)}/test`,
      { method: 'POST', body: JSON.stringify({}) }
    ),
});
