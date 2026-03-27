import http from 'k6/http';
import { check, sleep } from 'k6';
import { pickServiceName, requestParams, resolvedBaseUrl, releaseGateThresholds } from './targets.js';

const { maxFailureRate, wakeP95Ms, wakeP99Ms } = releaseGateThresholds();

export const options = {
  vus: Number(__ENV.E2E_WARMUP_VUS || 5),
  duration: __ENV.E2E_WARMUP_DURATION || '10s',
  thresholds: {
    http_req_failed: [`rate<=${maxFailureRate}`],
    http_req_duration: [`p(95)<=${wakeP95Ms}`, `p(99)<=${wakeP99Ms}`],
  },
};

const baseUrl = resolvedBaseUrl();

export default function () {
  const serviceName = pickServiceName();
  const res = http.get(baseUrl, requestParams(serviceName));

  check(res, {
    'warmup status is 200': (r) => r.status === 200,
  });

  sleep(1);
}
