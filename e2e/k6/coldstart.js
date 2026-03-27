import exec from 'k6/execution';
import http from 'k6/http';
import { check } from 'k6';
import {
  configuredServiceCount,
  pickServiceName,
  requestParams,
  resolvedBaseUrl,
  releaseGateThresholds,
  selectedTargetMode,
  serviceNameFromIndex,
} from './targets.js';

const { maxFailureRate, wakeP95Ms, wakeP99Ms } = releaseGateThresholds();
const targetMode = selectedTargetMode();
const coldStartVUs = targetMode === 'fixed' ? 1 : configuredServiceCount();

export const options = {
  scenarios: {
    cold_start_probe: {
      executor: 'per-vu-iterations',
      vus: coldStartVUs,
      iterations: 1,
      maxDuration: __ENV.E2E_REQUEST_TIMEOUT || '45s',
      gracefulStop: '0s',
    },
  },
  thresholds: {
    http_req_failed: [`rate<=${maxFailureRate}`],
    http_req_duration: [`p(95)<=${wakeP95Ms}`, `p(99)<=${wakeP99Ms}`],
  },
};

const baseUrl = resolvedBaseUrl();

function coldStartServiceName() {
  if (targetMode === 'fixed') {
    return pickServiceName();
  }

  const vuIndex = exec.vu.idInTest ? exec.vu.idInTest - 1 : 0;
  return serviceNameFromIndex(vuIndex);
}

export default function () {
  const serviceName = coldStartServiceName();
  const res = http.get(baseUrl, requestParams(serviceName));

  check(res, {
    'coldstart status is 200': (r) => r.status === 200,
  });
}
