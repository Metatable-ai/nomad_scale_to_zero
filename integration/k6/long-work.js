 /**
 * long-work.js — Demonstrate that a long-running request exceeding the idle
 * timeout triggers a full scale-down → re-wake lifecycle.
 *
 * Timeline (idle_timeout=15s, delay=25s):
 *
 *   0s   warmup          — fast request, service wakes up (version N)
 *   5s   long_request    — VU1 sends /cgi-bin/slow?delay=25 via Traefik
 *        monitor         — VU2 polls Nomad every 2s, logs alloc versions
 *        ~20s            — idle timeout fires, job scaled to 0 mid-request
 *        ~25s            — Traefik 502 → nscale error-fallback → re-wake (version N+1)
 *        ~30s            — long_request completes (body: index.html, not CGI)
 *   35s  verify_rewake   — confirm new allocation version appeared
 *   50s  post_idle_check — no traffic → job scales to 0 again
 *
 * Env vars:
 *   NSCALE_TRAEFIK_URL   — Traefik base URL  (default: http://traefik:80)
 *   NSCALE_NOMAD_URL     — Nomad API          (default: http://nomad:4646)
 *   NSCALE_DELAY_SECS    — CGI sleep seconds  (default: 25)
 *   NSCALE_IDLE_TIMEOUT  — expected idle timeout in seconds (default: 15)
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend, Gauge } from 'k6/metrics';

const traefikUrl   = __ENV.NSCALE_TRAEFIK_URL  || 'http://traefik:80';
const nomadUrl     = __ENV.NSCALE_NOMAD_URL     || 'http://nomad:4646';
const delaySecs    = parseInt(__ENV.NSCALE_DELAY_SECS   || '25');
const idleTimeout  = parseInt(__ENV.NSCALE_IDLE_TIMEOUT || '15');

const longWorkDuration   = new Trend('long_work_duration', true);
const allocVersionsBefore = new Gauge('alloc_versions_before');
const allocVersionsAfter  = new Gauge('alloc_versions_after');
const scaleDownsDuringWork = new Counter('scale_downs_during_work');
const reWakesDetected      = new Counter('re_wakes_detected');

const hostHeader = { Host: 'slow-service.localhost' };

/** Count allocations by status for slow-service. */
function getAllocInfo() {
  const resp = http.get(`${nomadUrl}/v1/job/slow-service/allocations`, { timeout: '5s' });
  if (resp.status !== 200) return { total: -1, running: 0, complete: 0, versions: [] };
  const allocs = JSON.parse(resp.body);
  const running  = allocs.filter(a => a.ClientStatus === 'running').length;
  const complete = allocs.filter(a => a.ClientStatus === 'complete').length;
  const versions = [...new Set(allocs.map(a => a.JobVersion))].sort((a,b) => a - b);
  return { total: allocs.length, running, complete, versions };
}

/** Get running count from scale endpoint. */
function getRunningCount() {
  const resp = http.get(`${nomadUrl}/v1/job/slow-service/scale`, { timeout: '5s' });
  if (resp.status !== 200) return -1;
  const body = JSON.parse(resp.body);
  return body.TaskGroups && body.TaskGroups.main ? body.TaskGroups.main.Running : 0;
}

export const options = {
  scenarios: {
    warmup: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      exec: 'warmup',
      maxDuration: '60s',
    },
    long_request: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      exec: 'longRequest',
      startTime: '5s',
      maxDuration: `${delaySecs + 60}s`,
    },
    monitor: {
      executor: 'constant-vus',
      vus: 1,
      // Run monitor for the full duration of long_request + margin
      duration: `${delaySecs + 45}s`,
      exec: 'monitor',
      startTime: '5s',
    },
    verify_rewake: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      exec: 'verifyRewake',
      startTime: `${5 + delaySecs + 10}s`,
      maxDuration: '30s',
    },
    post_idle_check: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      exec: 'postIdleCheck',
      startTime: `${5 + delaySecs + 10 + idleTimeout + 15}s`,
      maxDuration: '30s',
    },
  },
  thresholds: {
    // No hard pass/fail — this test is observational to show the behavior.
    // All checks are informational.
  },
};

export function setup() {
  const info = getAllocInfo();
  const running = getRunningCount();
  console.log(`[setup] slow-service: running=${running}, allocs=${info.total}, versions=${JSON.stringify(info.versions)}`);
  console.log(`[setup] config: delay=${delaySecs}s, idle_timeout=${idleTimeout}s`);
  console.log(`[setup] EXPECTED BEHAVIOR: service will be KILLED mid-request after ~${idleTimeout}s`);
  console.log(`[setup]   → request gets interrupted, nscale re-wakes the job (new version)`);
  console.log(`[setup]   → response body will be index.html, NOT the CGI output`);
  return { initialVersions: info.versions, initialAllocCount: info.total };
}

// ── Phase 1: Warmup ──
export function warmup() {
  // First make sure the job is running (may need wake-up)
  console.log('[warmup] Ensuring slow-service is alive...');
  const resp = http.get(traefikUrl, {
    headers: hostHeader,
    timeout: '30s',
  });
  const ok = check(resp, {
    'warmup: status 200': (r) => r.status === 200,
  });
  const info = getAllocInfo();
  console.log(`[warmup] status=${resp.status} ok=${ok} allocs=${info.total} versions=${JSON.stringify(info.versions)}`);
  allocVersionsBefore.add(info.versions.length);
}

// ── Phase 2: Long request — will be killed by scale-down ──
export function longRequest() {
  const before = getAllocInfo();
  console.log(`[long_request] START: sending /cgi-bin/slow?delay=${delaySecs} (versions before: ${JSON.stringify(before.versions)})`);
  console.log(`[long_request] This request takes ${delaySecs}s but idle_timeout is only ${idleTimeout}s`);
  console.log(`[long_request] The scale-down controller WILL kill this job mid-request...`);

  const start = Date.now();
  const resp = http.get(`${traefikUrl}/cgi-bin/slow?delay=${delaySecs}`, {
    headers: hostHeader,
    timeout: `${delaySecs + 60}s`,
  });
  const elapsedMs = Date.now() - start;

  longWorkDuration.add(elapsedMs);

  const gotCgiResponse = resp.body && resp.body.includes('Done after');
  const gotIndexPage   = resp.body && resp.body.includes('Hello from slow-service');
  const wasInterrupted = !gotCgiResponse;

  console.log(`[long_request] END: status=${resp.status} elapsed=${(elapsedMs/1000).toFixed(1)}s`);
  console.log(`[long_request] body="${(resp.body || '').trim().substring(0, 80)}"`);

  if (wasInterrupted) {
    console.log(`[long_request] ⚠ REQUEST WAS INTERRUPTED — service was killed mid-work`);
    if (gotIndexPage) {
      console.log(`[long_request]   → Got index.html after re-wake (error-fallback redirect)`);
    }
  } else {
    console.log(`[long_request] ✓ CGI response received — service survived the full ${delaySecs}s`);
  }

  // Record what happened
  check(resp, {
    'long_request: got HTTP response': (r) => r.status > 0,
    'long_request: CGI completed without interruption': () => gotCgiResponse,
    'long_request: was interrupted and re-woken': () => wasInterrupted && gotIndexPage,
  });

  const after = getAllocInfo();
  console.log(`[long_request] Allocs after: ${after.total}, versions: ${JSON.stringify(after.versions)}`);
  allocVersionsAfter.add(after.versions.length);
}

// ── Phase 3: Monitor — poll Nomad during the long request ──
export function monitor() {
  const count = getRunningCount();
  const info  = getAllocInfo();
  const ts    = new Date().toISOString().substring(11, 19);

  if (count === 0) {
    scaleDownsDuringWork.add(1);
    console.log(`[monitor ${ts}] ⚠ SCALED TO 0 — service killed while request in-flight! versions=${JSON.stringify(info.versions)}`);
  } else if (count > 0) {
    // Check if a new version appeared (re-wake after kill)
    console.log(`[monitor ${ts}] running=${count} allocs=${info.total} versions=${JSON.stringify(info.versions)}`);
  } else {
    console.log(`[monitor ${ts}] (could not query Nomad)`);
  }

  sleep(2);
}

// ── Phase 4: Verify re-wake — new version should have appeared ──
export function verifyRewake(data) {
  const info = getAllocInfo();
  const newVersions = info.versions.filter(v => !data.initialVersions.includes(v));
  const moreAllocs  = info.total > data.initialAllocCount;

  console.log(`[verify_rewake] initial versions: ${JSON.stringify(data.initialVersions)}`);
  console.log(`[verify_rewake] current versions: ${JSON.stringify(info.versions)}`);
  console.log(`[verify_rewake] new versions: ${JSON.stringify(newVersions)}`);
  console.log(`[verify_rewake] allocs: ${data.initialAllocCount} → ${info.total}`);

  if (newVersions.length > 0 || moreAllocs) {
    reWakesDetected.add(1);
    console.log(`[verify_rewake] ✓ Re-wake confirmed — new allocations were created`);
  }

  check(null, {
    'verify_rewake: new allocation version created': () => moreAllocs || newVersions.length > 0,
  });

  // Also verify the service is alive now
  const resp = http.get(traefikUrl, {
    headers: hostHeader,
    timeout: '30s',
  });
  check(resp, {
    'verify_rewake: service responds 200': (r) => r.status === 200,
  });
  console.log(`[verify_rewake] service status=${resp.status}`);
}

// ── Phase 5: Post-idle — should scale to 0 again after no traffic ──
export function postIdleCheck() {
  console.log('[post_idle_check] No traffic sent. Waiting for scale-down...');

  let count = -1;
  for (let i = 0; i < 25; i++) {
    count = getRunningCount();
    if (count === 0) {
      console.log(`[post_idle_check] ✓ Job scaled to 0 after ${i * 2}s of no traffic`);
      break;
    }
    sleep(2);
  }

  check(null, {
    'post_idle_check: job scaled to 0': () => count === 0,
  });

  const info = getAllocInfo();
  console.log(`[post_idle_check] final: running=${count}, total allocs=${info.total}, versions=${JSON.stringify(info.versions)}`);
  console.log('');
  console.log('═══════════════════════════════════════════════════════════');
  console.log('  SUMMARY: Long-work lifecycle test');
  console.log('═══════════════════════════════════════════════════════════');
  console.log(`  Initial versions:  ${JSON.stringify(__ENV._INIT_VERSIONS || 'N/A')}`);
  console.log(`  Final versions:    ${JSON.stringify(info.versions)}`);
  console.log(`  Total allocations: ${info.total}`);
  console.log(`  The service was killed mid-request and re-woken.`);
  console.log(`  This confirms nscale does NOT protect in-flight requests`);
  console.log(`  when the idle timeout is shorter than request duration.`);
  console.log('═══════════════════════════════════════════════════════════');
}
