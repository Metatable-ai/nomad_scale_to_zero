import http from 'k6/http';
import { check, sleep } from 'k6';
import { pickServiceName, requestParams, resolvedBaseUrl, releaseGateThresholds } from './targets.js';

const baseUrl = resolvedBaseUrl();
const { maxFailureRate, wakeP95Ms, wakeP99Ms } = releaseGateThresholds();

export const options = {
  vus: Number(__ENV.E2E_BURST_VUS || 50),
  duration: __ENV.E2E_BURST_DURATION || '20s',
  thresholds: {
    http_req_failed: [`rate<=${maxFailureRate}`],
    http_req_duration: [`p(95)<=${wakeP95Ms}`, `p(99)<=${wakeP99Ms}`],
  },
};

export default function () {
  const serviceName = pickServiceName();
  const res = http.get(baseUrl, requestParams(serviceName));

  check(res, {
    'rolling status is 200': (r) => r.status === 200,
  });

  sleep(Math.random());
}
