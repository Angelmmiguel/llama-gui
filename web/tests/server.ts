import { spawn, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const REPO_ROOT = resolve(import.meta.dirname, '../..');
const READY_TIMEOUT_MS = 120_000;

export interface TestServer {
	/** Origin, without the /ui prefix. */
	url: string;
	stop: () => Promise<void>;
}

function freePort(): Promise<number> {
	return new Promise((done, fail) => {
		const probe = createServer();
		probe.once('error', fail);
		probe.listen(0, '127.0.0.1', () => {
			const address = probe.address();
			const port = typeof address === 'object' && address ? address.port : 0;
			probe.close(() => done(port));
		});
	});
}

async function waitForHealth(url: string, child: ChildProcess, output: () => string) {
	const deadline = Date.now() + READY_TIMEOUT_MS;
	while (Date.now() < deadline) {
		if (child.exitCode !== null) {
			throw new Error(`server exited with ${child.exitCode}:\n${output()}`);
		}
		try {
			if ((await fetch(`${url}/api/v1/healthz`)).ok) return;
		} catch {
			// Not listening yet.
		}
		await new Promise((r) => setTimeout(r, 200));
	}
	throw new Error(`server not healthy after ${READY_TIMEOUT_MS} ms:\n${output()}`);
}

/**
 * Runs the real server on a free port with throwaway directories. It embeds
 * web/build as it stands, so build the app first.
 */
export async function startServer(): Promise<TestServer> {
	const root = mkdtempSync(join(tmpdir(), 'llama-gui-e2e-'));
	const port = await freePort();
	const url = `http://127.0.0.1:${port}`;

	// Its own process group, because `go run` leaves the server as a grandchild.
	const child = spawn(
		'go',
		[
			'run',
			'./cmd/llama-gui',
			'serve',
			'--addr',
			`127.0.0.1:${port}`,
			'--data',
			join(root, 'data'),
			'--builds',
			join(root, 'builds'),
			'--models',
			join(root, 'models')
		],
		{
			cwd: REPO_ROOT,
			detached: true,
			stdio: ['ignore', 'pipe', 'pipe'],
			env: { ...process.env, LLAMA_GUI_LLAMA_VERSION: 'none' }
		}
	);
	let log = '';
	child.stdout.on('data', (chunk) => (log += chunk));
	child.stderr.on('data', (chunk) => (log += chunk));

	const stop = async () => {
		if (child.pid !== undefined && child.exitCode === null) {
			const exited = new Promise((r) => child.once('exit', r));
			process.kill(-child.pid, 'SIGTERM');
			await exited;
		}
		rmSync(root, { recursive: true, force: true });
	};

	try {
		await waitForHealth(url, child, () => log);
	} catch (err) {
		await stop();
		throw err;
	}
	return { url, stop };
}
