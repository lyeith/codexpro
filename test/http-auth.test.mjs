import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { exportJWK, generateKeyPair, SignJWT } from 'jose';
import { createHttpAuthenticator } from '../dist/httpAuth.js';
import { principalIdFromAuthInfo } from '../dist/toolContext.js';

async function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      server.close(() => typeof address === 'object' && address ? resolve(address.port) : reject(new Error('no free port')));
    });
    server.on('error', reject);
  });
}

async function issuerFixture() {
  const issuer = 'https://unit.cloudflareaccess.com';
  const audience = 'unit-access-application-audience';
  const keyId = 'unit-rsa-key';
  const { publicKey, privateKey } = await generateKeyPair('RS256');
  const publicJwk = await exportJWK(publicKey);
  Object.assign(publicJwk, { kid: keyId, alg: 'RS256', use: 'sig' });
  const server = http.createServer((req, res) => {
    if (req.url !== '/certs') {
      res.writeHead(404).end();
      return;
    }
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ keys: [publicJwk] }));
  });
  await new Promise((resolve, reject) => {
    server.listen(0, '127.0.0.1', resolve);
    server.on('error', reject);
  });
  const address = server.address();
  if (!address || typeof address !== 'object') throw new Error('JWKS server has no address');

  const assertion = async ({
    subject = 'user-a',
    claimIssuer = issuer,
    claimAudience = audience,
    expiresAt = Math.floor(Date.now() / 1000) + 300,
    accessType = 'app'
  } = {}) => new SignJWT({ email: `${subject}@example.test`, scope: 'codexpro.read codexpro.write', type: accessType })
    .setProtectedHeader({ alg: 'RS256', kid: keyId, typ: 'JWT' })
    .setIssuer(claimIssuer)
    .setSubject(subject)
    .setAudience(claimAudience)
    .setIssuedAt()
    .setExpirationTime(expiresAt)
    .sign(privateKey);

  return {
    issuer,
    audience,
    jwksUri: `http://127.0.0.1:${address.port}/certs`,
    assertion,
    close: () => new Promise((resolve) => server.close(resolve))
  };
}

function cloudflareConfig(fixture, mode = 'cloudflare-access', credential) {
  return {
    authMode: mode,
    authToken: credential,
    cloudflareAccess: {
      teamDomain: fixture.issuer,
      audience: fixture.audience,
      jwksUri: fixture.jwksUri
    }
  };
}

function waitForListening(child) {
  return new Promise((resolve, reject) => {
    let stderr = '';
    const timer = setTimeout(() => reject(new Error(`timeout waiting for HTTP server\n${stderr}`)), 15_000);
    timer.unref();
    child.stderr.on('data', (chunk) => {
      stderr += String(chunk);
      if (stderr.includes('HTTP MCP listening')) {
        clearTimeout(timer);
        resolve();
      }
    });
    child.on('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`HTTP server exited before listening: ${code}\n${stderr}`));
    });
  });
}

async function stop(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 5_000))
  ]);
  if (child.exitCode === null) child.kill('SIGKILL');
}

function accessHeaders(assertion, sessionId) {
  return {
    accept: 'application/json, text/event-stream',
    'content-type': 'application/json',
    'cf-access-jwt-assertion': assertion,
    ...(sessionId ? { 'mcp-session-id': sessionId } : {})
  };
}

test('validates Cloudflare Access assertions and preserves static-token migration mode', async () => {
  const fixture = await issuerFixture();
  try {
    const verifier = createHttpAuthenticator(cloudflareConfig(fixture));
    const validAssertion = await fixture.assertion();
    const accepted = await verifier.authenticate({ headers: { 'cf-access-jwt-assertion': validAssertion } });
    assert.equal(accepted.authenticated, true);
    assert.equal(accepted.method, 'cloudflare-access');
    assert.equal(accepted.authInfo?.extra?.iss, fixture.issuer);
    assert.equal(accepted.authInfo?.extra?.sub, 'user-a');
    assert.deepEqual(accepted.authInfo?.scopes, ['codexpro.read', 'codexpro.write']);

    const connectorConfig = { connectorId: 'unit-connector' };
    const principal = principalIdFromAuthInfo(connectorConfig, accepted.authInfo);
    const repeated = principalIdFromAuthInfo(connectorConfig, accepted.authInfo);
    const anotherAssertion = await fixture.assertion({ subject: 'user-b' });
    const another = await verifier.authenticate({ headers: { 'cf-access-jwt-assertion': anotherAssertion } });
    assert.equal(principal, repeated);
    assert.notEqual(principal, principalIdFromAuthInfo(connectorConfig, another.authInfo));

    for (const invalidAssertion of [
      await fixture.assertion({ claimAudience: 'wrong-audience' }),
      await fixture.assertion({ claimIssuer: 'https://wrong.cloudflareaccess.com' }),
      await fixture.assertion({ expiresAt: Math.floor(Date.now() / 1000) - 60 }),
      await fixture.assertion({ accessType: 'org' })
    ]) {
      assert.deepEqual(
        await verifier.authenticate({ headers: { 'cf-access-jwt-assertion': invalidAssertion } }),
        { authenticated: false }
      );
    }

    const credential = ['static', '-credential-', 'x'.repeat(32)].join('');
    const staticAuthenticator = createHttpAuthenticator({ authMode: 'static-token', authToken: credential });
    assert.deepEqual(
      await staticAuthenticator.authenticate({ headers: { authorization: `Bearer ${credential}` } }),
      { authenticated: true, method: 'static-token' }
    );
    const queryName = ['codexpro', 'token'].join('_');
    assert.deepEqual(
      await staticAuthenticator.authenticate({ headers: {}, query: { [queryName]: credential } }),
      { authenticated: true, method: 'static-token' }
    );

    const eitherAuthenticator = createHttpAuthenticator(cloudflareConfig(fixture, 'either', credential));
    assert.equal((await eitherAuthenticator.authenticate({ headers: { authorization: `Bearer ${credential}` } })).method, 'static-token');
    assert.equal((await eitherAuthenticator.authenticate({ headers: { 'cf-access-jwt-assertion': validAssertion } })).method, 'cloudflare-access');
    assert.equal((await eitherAuthenticator.authenticate({
      headers: { authorization: `Bearer ${credential}`, 'cf-access-jwt-assertion': validAssertion }
    })).method, 'cloudflare-access');
  } finally {
    await fixture.close();
  }
});

test('Cloudflare mode protects HTTP and binds MCP sessions to one authenticated subject', async () => {
  const fixture = await issuerFixture();
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-cloudflare-access-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-cloudflare-home-'));
  const port = await freePort();
  const env = {
    ...process.env,
    CODEXPRO_ROOT: root,
    CODEXPRO_ALLOWED_ROOTS: root,
    CODEXPRO_HOST: '127.0.0.1',
    CODEXPRO_PORT: String(port),
    CODEXPRO_HOME: home,
    CODEXPRO_BASH_MODE: 'off',
    CODEXPRO_WRITE_MODE: 'off',
    CODEXPRO_AUTH_MODE: 'cloudflare-access',
    CODEXPRO_CF_ACCESS_TEAM_DOMAIN: fixture.issuer,
    CODEXPRO_CF_ACCESS_AUDIENCE: fixture.audience,
    CODEXPRO_CF_ACCESS_JWKS_URI: fixture.jwksUri
  };
  delete env.CODEXPRO_HTTP_TOKEN;
  delete env.CODEBASE_BRIDGE_HTTP_TOKEN;
  delete env.CODEXPRO_ALLOW_NO_HTTP_TOKEN;
  const child = spawn('node', ['dist/http.js'], {
    cwd: path.resolve('.'),
    env,
    stdio: ['ignore', 'pipe', 'pipe']
  });

  try {
    await waitForListening(child);
    const base = `http://127.0.0.1:${port}`;
    assert.equal((await fetch(`${base}/healthz`)).status, 401);

    const assertionA = await fixture.assertion({ subject: 'user-a' });
    const assertionB = await fixture.assertion({ subject: 'user-b' });
    const wrongAudience = await fixture.assertion({ claimAudience: 'wrong-audience' });
    const health = await fetch(`${base}/healthz`, { headers: { 'cf-access-jwt-assertion': assertionA } });
    assert.equal(health.status, 200);
    const healthBody = await health.json();
    assert.equal(healthBody.authMode, 'cloudflare-access');
    assert.deepEqual(healthBody.authMethods, ['cloudflare-access']);
    assert.equal(healthBody.authRequired, true);
    assert.equal((await fetch(`${base}/healthz`, { headers: { 'cf-access-jwt-assertion': wrongAudience } })).status, 401);
    const fakeQueryCredential = ['not', 'a', 'cloudflare', 'assertion'].join('-');
    const queryName = ['codexpro', 'token'].join('_');
    const queryUrl = new URL('/healthz', base);
    queryUrl.searchParams.set(queryName, fakeQueryCredential);
    assert.equal((await fetch(queryUrl)).status, 401);

    const initialize = await fetch(`${base}/mcp`, {
      method: 'POST',
      headers: accessHeaders(assertionA),
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          protocolVersion: '2024-11-05',
          capabilities: {},
          clientInfo: { name: 'cloudflare-auth-test', version: '1.0.0' }
        }
      })
    });
    const initializeBody = await initialize.text();
    assert.equal(initialize.status, 200, initializeBody);
    const sessionId = initialize.headers.get('mcp-session-id');
    assert.match(sessionId ?? '', /^[0-9a-f-]{36}$/i);

    const initialized = await fetch(`${base}/mcp`, {
      method: 'POST',
      headers: accessHeaders(assertionA, sessionId),
      body: JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' })
    });
    assert.ok([200, 202, 204].includes(initialized.status), await initialized.text());

    const samePrincipal = await fetch(`${base}/mcp`, {
      method: 'POST',
      headers: accessHeaders(assertionA, sessionId),
      body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} })
    });
    assert.equal(samePrincipal.status, 200, await samePrincipal.text());

    const differentPrincipal = await fetch(`${base}/mcp`, {
      method: 'POST',
      headers: accessHeaders(assertionB, sessionId),
      body: JSON.stringify({ jsonrpc: '2.0', id: 3, method: 'tools/list', params: {} })
    });
    assert.equal(differentPrincipal.status, 403);
    const rejected = await differentPrincipal.json();
    assert.equal(rejected.error?.code, -32003);
  } finally {
    await stop(child);
    await fixture.close();
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});
