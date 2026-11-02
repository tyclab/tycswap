import { expect, mock, test } from 'claude-code/testing';

async function collect(stream) {
  let step = await stream.next();
  while (!step.done) step = await stream.next();
  return step.value;
}

function fixture(on, operation, agents = []) {
  on('session.id', () => ({ value: 'check-session' }));
  on('session.model', () => ({ value: 'fable' }));
  on('agent.list', () => ({ value: agents }));
  mock.env(on, {
    TYCSWAP_ROUTE_BINARY: '/fixture/tycswap', TYCSWAP_GROUP: 'fable',
    TYCSWAP_ROUTE_LAUNCH: 'check-launch', TYCSWAP_ROUTE_PID: '1234',
  });
  on('process.run', ($, e) => ({ value: operation(e.argv[2], JSON.parse(e.init.stdin)) }));
}

const request = { turnId: 'check', index: 0, model: 'claude-fable-5', messageCount: 1 };
const reply = (value) => ({ exitCode: 0, stdout: JSON.stringify(value), stderr: '' });
const answer = (e) => ({ turnId: e.turnId, index: e.index, answer: 'dispatched', toolUses: [], stopReason: null, usage: null });

test('missing routing context stops dispatch', async ($, on) => {
  let sent = 0;
  on('session.id', () => ({ value: 'missing-context' }));
  on('session.model', () => ({ value: 'fable' }));
  mock.env(on, {});
  on('turn.step', async function* ($, e) { sent++; return answer(e); });
  const result = await collect($.turn.step(request));
  expect(sent).toBe(0);
  expect(result.answer).toContain('No model request was sent');
});

test('acquire refusal stops dispatch', async ($, on) => {
  let sent = 0;
  fixture(on, (operation) => operation === 'acquire' ? { exitCode: 1, stdout: '{}', stderr: '' } : reply({ pid: 1234 }));
  on('turn.step', async function* ($, e) { sent++; return answer(e); });
  const result = await collect($.turn.step(request));
  expect(sent).toBe(0);
  expect(result.answer).toContain('No model request was sent');
});

test('requests release after dispatch and preserve child identity', async ($, on) => {
  const calls = [];
  let sent = 0;
  fixture(on, (operation, body) => { calls.push({ operation, body }); return reply({ pid: 1234, id: body.id, account: '2' }); });
  on('turn.step', async function* ($, e) { sent++; return answer(e); });
  const result = await collect($.turn.step({ ...request, agentId: 'child-1' }));
  expect(result.answer).toBe('dispatched');
  expect(sent).toBe(1);
  expect(calls.map((call) => call.operation)).toEqual(['register', 'acquire', 'release']);
  expect(calls[1].body.agent).toBe('child-1');
  expect(calls[1].body.pid).toBe(1234);
  expect(calls[2].body.id).toBe(calls[1].body.id);
});

test('release failure preserves the response and stops later requests', async ($, on) => {
  let sent = 0;
  fixture(on, (operation) => operation === 'release' ? { exitCode: 1, stdout: '{}', stderr: '' } : reply({ pid: 1234 }));
  on('turn.step', async function* ($, e) { sent++; return answer(e); });
  expect((await collect($.turn.step(request))).answer).toBe('dispatched');
  expect((await collect($.turn.step({ ...request, index: 1 }))).answer).toContain('No model request was sent');
  expect(sent).toBe(1);
});

test('closing a response stream releases its request', async ($, on) => {
  const operations = [];
  fixture(on, (operation) => { operations.push(operation); return reply({ pid: 1234 }); });
  on('turn.step', async function* ($, e) {
    yield { kind: 'text', index: 0, text: 'partial' };
    yield { kind: 'text', index: 0, text: 'tail' };
    return answer(e);
  });
  const stream = $.turn.step(request);
  await stream.next();
  await stream.return(undefined);
  expect(operations).toEqual(['register', 'acquire', 'release']);
});


test('unmanaged teammate requests are refused', async ($, on) => {
  let calls = 0;
  let sent = 0;
  fixture(on, () => { calls++; return reply({ pid: 1234 }); }, [{ id: 'team', type: 'teammate' }]);
  on('turn.step', async function* ($, e) { sent++; return answer(e); });
  const result = await collect($.turn.step({ ...request, agentId: 'team' }));
  expect(result.answer).toContain('No model request was sent');
  expect(calls).toBe(0);
  expect(sent).toBe(0);
});
