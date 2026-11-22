function dashboardFixture() {
  const usage = {
    fiveHour: {pct: 64}, sevenDay: {pct: 87}, scoped: [{name: 'Fable', pct: 100}],
  };
  const windows = [usage.fiveHour, usage.sevenDay, usage.scoped[0]];
  for (const [i, w] of windows.entries()) w.resetsAt = new Date(Date.now() + (i + 2.27) * 86400000).toISOString();
  const accounts = Array.from({length: 8}, (_, i) => ({
    number: i+1, email: 'user' + (i+1) + '@demo.test', isActive: i === 0, atLimit: true, usage,
  }));
  const sessions = Array.from({length: 10}, (_, i) => ({
    sessionId: '11111111-1111-4111-8111-' + String(i).padStart(12,'0'), pid: i+2, cwd: '/work/shared',
    title: 'Review the weekly usage report and the reset time for the integration account ' + (i+1),
    status: 'busy', login: 'default login', accountNumber: '1',
  }));
  return {version: '0.21.3', updates: {available: true}, settings: [], accounts, sessions: {claude: sessions},
    fable: {state: 'on', number: 2, email: accounts[1].email},
    mythos: {number: 8, email: accounts[7].email, countedWindows: ['7d','Fable'], atLimit: true,
      limitingWindows: ['Fable'], resetClock: 'Jan 25 18:30', linkedSessions: 0}};
}
