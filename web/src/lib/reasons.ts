// Text of an allocation constraint verdict (SPEC §7.5): reason code +
// parameters from the API.
import { tk } from './i18n';

export function reasonText(reason: string | undefined, params: Record<string, string> = {}): string {
  if (!reason) return '';
  const vars: Record<string, string> = { ...params };
  vars.tier = params.tier ? ` (${params.tier})` : '';
  if (params.then) vars.then = reasonText(params.then, { ...params, then: '' });
  return tk(`reason.${reason}`, vars, reason);
}
