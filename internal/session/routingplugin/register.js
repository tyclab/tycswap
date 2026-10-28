let registered = '';
let owner = 0;
let unavailable = false;
let registration;

async function call($, operation, body) {
  const binary = await $.env.get('TYCSWAP_ROUTE_BINARY');
  const group = await $.env.get('TYCSWAP_GROUP');
  const launch = await $.env.get('TYCSWAP_ROUTE_LAUNCH');
  const pid = Number(await $.env.get('TYCSWAP_ROUTE_PID'));
  if (!binary || !launch || !['fable', 'opus'].includes(group) || !Number.isInteger(pid) || pid <= 1) {
    throw new Error('Account routing has no valid launcher context.');
  }
  const reply = await $.process.run([binary, 'routing', operation, '--group', group], {
    stdin: JSON.stringify({ ...body, launch, pid }), timeoutMs: 30000,
  });
  if (reply.exitCode !== 0) throw new Error('Account routing could not complete ' + operation + '.');
  return JSON.parse(reply.stdout);
}

async function ensureRegistered($) {
  const session = await $.session.id();
  if (registered !== session) {
    if (!registration) {
      registration = (async () => {
        const reply = await call($, 'register', { session, model: await $.session.model() });
        registered = session;
        owner = reply.pid;
      })();
    }
    try { await registration; } finally { registration = undefined; }
    if (registered !== session) return ensureRegistered($);
  }
  return session;
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    registered = '';
    await ensureRegistered($);
    return next(e);
  });

  on('turn.step', async function* ($, e, next) {
    if (unavailable) throw new Error('A previous routing request could not be released.');
    if (e.agentId && (await $.agent.list()).some(agent => agent.id === e.agentId && agent.type === 'teammate')) {
      throw new Error('Independent teammates require their own managed launcher.');
    }
    const session = await ensureRegistered($);
    const id = crypto.randomUUID();
    try {
      await call($, 'acquire', { id, session, main: await $.session.model(), model: e.model, agent: e.agentId });
      return yield* next(e);
    } finally {
      try {
        await call($, 'release', { id, session, pid: owner });
      } catch {
        unavailable = true;
      }
    }
  }).catch(async function* ($, e, next) {
    if (next.called) return yield* next(e);
    const answer = 'Account routing is unavailable or no eligible account is ready. No model request was sent.';
    yield { kind: 'text', index: 0, text: answer };
    return { turnId: e.turnId, index: e.index, answer, toolUses: [], stopReason: null, usage: null };
  });

  on('session.end', async ($, e, next) => {
    if (registered) await call($, 'unregister', { session: registered });
    registered = '';
    return next(e);
  });
}
