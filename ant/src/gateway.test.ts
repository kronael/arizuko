// gateway.ts is the one reader of MODEL_GATEWAY_URL/KEY; three harnesses
// translate what it returns, so its shape is pinned here once.

import { expect, test } from 'bun:test';
import { modelGateway } from './gateway.js';

// No URL is no gateway — a key alone declares nothing.
test('modelGateway: no URL is no gateway, key alone does not count', () => {
  expect(modelGateway({})).toBeUndefined();
  expect(modelGateway({ MODEL_GATEWAY_KEY: 'virtual-key-abc123' })).toBeUndefined();
});

// The trailing slash is stripped so `${url}/v1` never doubles it; a missing
// key reads as '' so every translator can branch on it the same way.
test('modelGateway: returns the pair, trailing slash stripped, key optional', () => {
  expect(modelGateway({ MODEL_GATEWAY_URL: 'http://gw:4000/', MODEL_GATEWAY_KEY: 'virtual-key-abc123' }))
    .toEqual({ url: 'http://gw:4000', key: 'virtual-key-abc123' });
  expect(modelGateway({ MODEL_GATEWAY_URL: 'http://gw:4000' })).toEqual({ url: 'http://gw:4000', key: '' });
});
