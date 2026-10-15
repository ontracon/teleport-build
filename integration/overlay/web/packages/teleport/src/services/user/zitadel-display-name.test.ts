// SPDX-License-Identifier: AGPL-3.0-or-later
import makeUserContext from './makeUserContext';

test('readable SSO labels preserve the internal username', () => {
  const userName = 'zitadel-stable-identity';
  for (const displayName of ['person@example.com', 'updated@example.com']) {
    const context = makeUserContext({ userName, displayName });
    expect(context.username).toBe(userName);
    expect(context.displayName).toBe(displayName);
  }
});

test('missing labels preserve existing username behavior', () => {
  const context = makeUserContext({ userName: 'local-user' });
  expect(context.username).toBe('local-user');
  expect(context.displayName).toBeUndefined();
});
