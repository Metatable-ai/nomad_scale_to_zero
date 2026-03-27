import http from 'k6/http';
import { check } from 'k6';
import { pickServiceName, requestParams, resolvedBaseUrl, releaseGateThresholds } from './targets.js';

const { maxFailureRate, wakeP95Ms, wakeP99Ms } = releaseGateThresholds();

export const options = {
  scenarios: {
    cold_start_storm: {
      executor: 'ramping-arrival-rate',
      startRate: Number(__ENV.E2E_STORM_START_RATE || 25),
      timeUnit: '1s',
      preAllocatedVUs: Number(__ENV.E2E_STORM_PREALLOCATED_VUS || 150),
      maxVUs: Number(__ENV.E2E_STORM_MAX_VUS || 400),
      stages: [
        { target: Number(__ENV.E2E_STORM_RATE || 250), duration: __ENV.E2E_STORM_DURATION || '30s' },
      ],
    },
  },
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
    'storm status is 200': (r) => r.status === 200,
  });
}
