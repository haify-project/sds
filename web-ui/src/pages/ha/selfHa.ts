export const SELF_HA_RESOURCE = 'haify-meta';

/** Self-HA enable/disable failures often manifest as fetch errors while the
 * controller restarts under drbd-reactor. Surface those as informational. */
export function isRestartError(message: string): boolean {
  return message.includes('fetch') || message.includes('Failed');
}
