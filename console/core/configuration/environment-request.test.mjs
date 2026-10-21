import { expect, test } from 'bun:test';
import { parseEnvironmentRequest } from './environment-request';

test('environment requests validate exact fields, names, kinds and byte limits', () => {
  const input = { type: 'app.environment.set', name: 'GH_TOKEN', value: 'token', secret: true };
  expect(parseEnvironmentRequest('set', input)).toEqual(input);
  for (const body of [{ ...input, app_id: 'foreign' }, { ...input, name: 'CANTELOP_INTEGRATION_TOKEN' }, { ...input, value: 'a\0b' }, { ...input, value: 'é'.repeat(32769) }, { ...input, secret: 'true' }, { type: 'app.environment.unset', name: 'TOKEN' }]) {
    expect(() => parseEnvironmentRequest('set', body)).toThrow();
  }
  for (const revision of [-1, '1', NaN]) expect(() => parseEnvironmentRequest('status', { type: 'app.environment.status', revision })).toThrow();
  expect(parseEnvironmentRequest('status', { type: 'app.environment.status', revision: 0 }).revision).toBe(0);
});
