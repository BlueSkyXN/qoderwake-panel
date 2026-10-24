import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { preferencePayload, saveMessages } from '../src/lib/model-settings.ts';

test('preference payload preserves project scope and omits untouched parameters', () => {
  const value = preferencePayload('w1', 'project-a', 'custom/model', 'revision', '', '');
  assert.equal(value.projectId, 'project-a');
  assert.equal(value.model, 'custom/model');
  assert.equal('contextWindow' in value, false);
  assert.equal('reasoningEffort' in value, false);
  assert.equal(value.confirm, '/api/waker/models/update');
});
test('context window must remain an exact positive integer', () => {
  for (const input of ['abc', '0', '-1', '1.5', '9007199254740993']) {
    assert.throws(() => preferencePayload('w1', '', 'auto', 'r', '', input));
  }
  assert.equal(preferencePayload('w1', '', 'auto', 'r', 'high', '500000').contextWindow, 500000);
});
test('uncertain saves always require readback instead of claiming a model switch', () => {
  for (const state of [
    'unknown',
    'saved_unverified',
    'changed_after_save',
    'readback_mismatch',
  ] as const) {
    assert.match(saveMessages[state], /重新读取/);
  }
});
