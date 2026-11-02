function blocked(e, answer) {
  return { turnId: e.turnId, index: e.index, answer, toolUses: [], stopReason: null, usage: null };
}

async function call($, path, body) {
  const base = await $.env.get('TYCSWAP_ROUTING_PROBE_URL');
  if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(base || '')) throw new Error('Local test coordinator is unavailable.');
  const reply = await $.http.fetch(base + path, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  if (!reply.ok) throw new Error(reply.text);
  return JSON.parse(reply.text);
}

export function register(on) {
  on('turn.step', async function* ($, e, next) {
    const lease = await call($, '/acquire', {
      session: await $.session.id(), main: await $.session.model(), model: e.model, agent: e.agentId,
    });
    try {
      return yield* next(e);
    } finally {
      await call($, '/release', lease);
    }
  }).catch(async function* ($, e, next) {
    if (next.called) return yield* next(e);
    return blocked(e, 'Model routing is unavailable; no request was sent.');
  });
}
