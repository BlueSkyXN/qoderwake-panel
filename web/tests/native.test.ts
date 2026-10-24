import test from 'node:test';
import assert from 'node:assert/strict';
import {
  matches,
  parameters,
  parameterValue,
  optionActive,
  fileParameter,
  isFile,
} from '../src/lib/native.ts';
import { requestID } from '../src/lib/ids.ts';
import type { Spec, Option } from '../src/lib/native.ts';
const option = (name: string): Option => ({
  name,
  aliases: [name],
  description: '',
  value: true,
  optionalValue: false,
  variadic: false,
  repeatable: false,
  placeholder: 'value',
  choices: [],
  secret: false,
});
const spec: Spec = {
  id: 'mcp.add',
  path: ['mcp', 'add'],
  group: 'mcp',
  groupName: 'MCP 服务器',
  description: 'add an MCP server',
  revision: 'example',
  effect: 'write',
  scope: 'waker',
  panelPage: null,
  panelOnly: false,
  arguments: [],
  options: [
    '--waker-id',
    '--name',
    '--command',
    '--args',
    '--http-url',
    '--json',
    '--json-file',
  ].map(option),
  help: '',
  available: true,
  contextKeys: [],
};
test('all supported natural queries locate the same operation', () => {
  for (const query of ['mcp.add', 'mcp add', '新增 MCP', 'MCP 新增', '  mcp   add  '])
    assert.equal(matches(spec, query), true);
  assert.equal(matches(spec, 'mcp delete'), false);
});
test('changing MCP input mode excludes hidden values while preserving scope', () => {
  const fields = Object.fromEntries(
    spec.options.map((field) => [field.name, { value: 'value:' + field.name, enabled: true }]),
  );
  let p = parameters(spec, {}, fields, 'fields', 'stdio');
  assert.ok(p.options['--command']);
  assert.equal(p.options['--http-url'], undefined);
  assert.equal(p.options['--json'], undefined);
  p = parameters(spec, {}, fields, 'fields', 'http');
  assert.ok(p.options['--http-url']);
  assert.equal(p.options['--command'], undefined);
  p = parameters(spec, {}, fields, 'json', 'http');
  assert.deepEqual(Object.keys(p.options), ['--waker-id', '--json']);
  p = parameters(spec, {}, fields, 'all', 'http');
  assert.equal(Object.keys(p.options).length, spec.options.length);
});
test('explicit empty strings and optional bare switches stay distinct', () => {
  const custom = { ...spec, options: [{ ...option('--name'), optionalValue: true }] };
  assert.deepEqual(
    parameters(custom, {}, { '--name': { value: '', enabled: true } }, 'all', 'stdio').options,
    { '--name': '' },
  );
  assert.deepEqual(
    parameters(custom, {}, { '--name': { value: '', enabled: true, bare: true } }, 'all', 'stdio')
      .options,
    { '--name': true },
  );
  assert.deepEqual(
    parameters(custom, {}, { '--name': { value: 'draft', enabled: false } }, 'all', 'stdio')
      .options,
    {},
  );
});
test('multivalue JSON preserves blank values and newlines and rejects mixed types', () => {
  assert.deepEqual(parameterValue({ variadic: true }, '["","a\\nb"]'), ['', 'a\nb']);
  assert.deepEqual(parameterValue({ variadic: false, repeatable: true }, 'first\nsecond'), [
    'first',
    'second',
  ]);
  assert.throws(() => parameterValue({ variadic: true }, '[1,"x"]'));
  assert.equal(optionActive('--oauth-client-id', 'fields', 'stdio'), false);
});
test('positional file parameters retain every uploaded reference', () => {
  const field = { name: 'file-path', required: true, variadic: true, description: '' };
  assert.equal(isFile(field), true);
  const value = fileParameter(field, '["first.txt"]', '@panel-file:synthetic');
  const custom = { ...spec, arguments: [field] };
  assert.deepEqual(parameters(custom, { 'file-path': value }, {}, 'all', '').arguments, {
    'file-path': ['first.txt', '@panel-file:synthetic'],
  });
  assert.equal(isFile({ ...option('--json'), value: false }), false);
});
test('request IDs also work on trusted HTTP LAN origins without randomUUID', () => {
  const source = {
    getRandomValues: <T extends ArrayBufferView | null>(array: T): T => {
      if (array instanceof Uint8Array) array.fill(42);
      return array;
    },
  };
  assert.equal(requestID(source), '2a'.repeat(16));
  assert.notEqual(requestID(), requestID());
});
