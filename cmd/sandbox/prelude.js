// --- Protocol setup ---
// Reserve real stdout (fd 1) for JSON protocol messages.
// Redirect console.log/warn/error to stderr so debug output does not corrupt the protocol.
const _fs = require('fs');
const _path = require('path');
const _http = require('http');
const _https = require('https');

const _protoOut = _fs.createWriteStream('', { fd: 1 });
const _origConsole = { ...console };

// Redirect all console methods to stderr.
console.log = (...args) => process.stderr.write(args.map(String).join(' ') + '\n');
console.warn = (...args) => process.stderr.write(args.map(String).join(' ') + '\n');
console.error = (...args) => process.stderr.write(args.map(String).join(' ') + '\n');
console.info = (...args) => process.stderr.write(args.map(String).join(' ') + '\n');
console.debug = (...args) => process.stderr.write(args.map(String).join(' ') + '\n');

let _finalResult = undefined;
let _outputFiles = [];

const _CALLBACK_URL = process.env._SANDBOX_CALLBACK_URL || '';
const _EXECUTION_ID = process.env._SANDBOX_EXECUTION_ID || '';

/**
 * Call an agent tool via the Oasis callback URL and return the result.
 *
 * Blocks (async) until the tool returns. Throws on tool failure.
 *
 * @param {string} name - Tool name
 * @param {object} [args={}] - Tool arguments
 * @returns {Promise<any>} Parsed tool result
 *
 * @example
 *   const data = await callTool('web_search', { query: 'latest Node.js release' });
 *   const content = await callTool('file_read', { path: 'config.yaml' });
 */
function callTool(name, args) {
    if (!_CALLBACK_URL) {
        throw new Error('callTool: no callback URL configured');
    }

    const payload = JSON.stringify({
        execution_id: _EXECUTION_ID,
        name: name,
        args: args || {},
    });

    return new Promise((resolve, reject) => {
        const url = new URL(_CALLBACK_URL);
        const mod = url.protocol === 'https:' ? _https : _http;
        const req = mod.request(url, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(payload),
            },
            timeout: 120000,
        }, (res) => {
            let body = '';
            res.on('data', (chunk) => { body += chunk; });
            res.on('end', () => {
                try {
                    const result = JSON.parse(body);
                    if (result.error) {
                        reject(new Error(`Tool '${name}' failed: ${result.error}`));
                        return;
                    }
                    let data = result.data || '';
                    if (typeof data === 'string' && data) {
                        try { data = JSON.parse(data); } catch (_) { /* use as-is */ }
                    }
                    resolve(data);
                } catch (e) {
                    reject(new Error(`Tool '${name}': invalid response: ${e.message}`));
                }
            });
        });
        req.on('error', (e) => reject(new Error(`Tool '${name}': request failed: ${e.message}`)));
        req.on('timeout', () => {
            req.destroy();
            reject(new Error(`Tool '${name}': request timed out`));
        });
        req.write(payload);
        req.end();
    });
}

/**
 * Call multiple tools in parallel. Returns results in the same order as input.
 *
 * @param {Array<[string, object]>} calls - Array of [name, args] tuples
 * @returns {Promise<any[]>} Array of results in order
 *
 * @example
 *   const [a, b] = await callToolsParallel([
 *       ['http_fetch', { url: 'https://example.com/a' }],
 *       ['http_fetch', { url: 'https://example.com/b' }],
 *   ]);
 */
function callToolsParallel(calls) {
    return Promise.all(calls.map(([name, args]) => callTool(name, args || {})));
}

/**
 * Set the structured result to return to the caller.
 *
 * Call this once at the end of your code. The data is JSON-serialized and
 * returned in the 'output' field of the HTTP response.
 *
 * @param {any} data - Any JSON-serializable value
 * @param {string[]} [files] - Optional file paths to include in response
 *
 * @example
 *   setResult({ summary: 'done', count: 42 }, ['chart.png']);
 */
function setResult(data, files) {
    _finalResult = data;
    if (files !== undefined) {
        _outputFiles = Array.isArray(files) ? files : [files];
    }
}

/**
 * Install an npm package at runtime.
 *
 * The container boundary provides isolation. Installed packages persist
 * for the lifetime of the container.
 *
 * @param {string} name - Package name (e.g. 'lodash', 'cheerio')
 *
 * @example
 *   await installPackage('cheerio');
 *   const cheerio = require('cheerio');
 */
function installPackage(name) {
    const { execSync } = require('child_process');
    console.log(`[sandbox] npm install ${name}...`);
    try {
        execSync(`npm install --no-save ${name}`, {
            stdio: ['pipe', 'pipe', 'pipe'],
            cwd: process.env._SANDBOX_WORKSPACE || process.cwd(),
        });
        console.log(`[sandbox] ${name} installed`);
    } catch (e) {
        throw new Error(`npm install ${name} failed: ${e.stderr ? e.stderr.toString() : e.message}`);
    }
}

// --- User code starts below ---
// The user code is wrapped in an async IIFE so top-level await works.
(async () => {
try {
